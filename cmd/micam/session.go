package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"micam/internal/cli"
	"micam/internal/cloud"
	"micam/internal/miss"
	"micam/internal/pppp"
	"micam/internal/stream"
)

// runCamera keeps a session alive. When the stream ends on its own (the camera
// rebooted, the relay dropped the session), it reconnects instead of exiting and
// waiting for the orchestrator to restart the container.
//
// Reconnecting rebuilds everything, including the HTTP server: a viewer that
// reconnects gets a fresh transport stream starting at a keyframe, rather than a
// PTS discontinuity in the middle of one.
func runCamera(ctx context.Context, opts *cli.Options, client *cloud.Client,
	target cloud.Device, log *slog.Logger) int {

	attempt := 0
	for {
		code, retryable := streamSession(ctx, opts, client, target, attempt, log)
		if !retryable || ctx.Err() != nil || attempt >= opts.Reconnect {
			if retryable && opts.Reconnect > 0 && ctx.Err() == nil {
				log.Error("giving up", "attempts", attempt+1, "max", opts.Reconnect+1)
			}
			return code
		}

		attempt++
		delay := reconnectDelay(attempt)
		log.Warn("stream ended, reconnecting",
			"attempt", attempt, "of", opts.Reconnect, "delay", delay.String())

		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return code
		}
	}
}

// maxReconnectDelay caps the backoff so a camera that reboots comes back
// quickly instead of waiting minutes.
const maxReconnectDelay = 30 * time.Second

// reconnectDelay grows linearly and then plateaus.
func reconnectDelay(attempt int) time.Duration {
	delay := time.Duration(attempt) * 5 * time.Second
	if delay > maxReconnectDelay {
		return maxReconnectDelay
	}
	return delay
}

// streamSession runs exactly one attempt: provision, connect, authenticate,
// serve. Everything it opens is released by its own defers, so a retry starts
// from a clean slate.
//
// The boolean reports whether another attempt could plausibly help.
func streamSession(ctx context.Context, opts *cli.Options, client *cloud.Client,
	target cloud.Device, attempt int, log *slog.Logger) (int, bool) {

	keys, err := miss.GenerateKeys()
	if err != nil {
		log.Error("cannot generate a key pair", "err", err)
		return exitSession, true
	}

	prov, err := client.Provision(ctx, target.DID, keys.PublicHex())
	if err != nil {
		log.Error("provisioning failed", "err", err)
		return exitSession, true
	}
	if !prov.IsCS2() {
		log.Error("unsupported transport", "vendor", prov.VendorName(),
			"hint", "only cs2 is implemented")
		return exitNoCamera, false
	}

	uid, err := pppp.ParseUID(prov.P2PID)
	if err != nil {
		log.Error("cannot parse the camera's P2P id", "err", err, "p2p_id", prov.P2PID)
		return exitSession, false
	}

	sock, peer, err := dialCamera(ctx, opts, target, uid, prov, log)
	if err != nil {
		log.Error("cannot reach the camera", "err", err)
		return exitSession, true
	}
	defer sock.Close()

	local := localEndpoint(sock, peer)
	conn := pppp.NewConn(sock, peer, local, log)
	conn.Start()
	defer conn.Close()

	media := miss.NewClient(conn)
	if _, err := media.Authenticate(ctx, keys.PublicHex(), prov.Sign, 15*time.Second); err != nil {
		log.Error("MISS authentication failed", "err", err)
		return exitSession, true
	}

	key, err := miss.SessionKey(keys.Private, prov.DevicePublic)
	if err != nil {
		log.Error("cannot derive the session key", "err", err)
		return exitSession, false
	}
	media.UseSessionKey(key)

	if err := media.StartVideo(opts.Quality, false); err != nil {
		log.Error("cannot start the video stream", "err", err)
		return exitSession, true
	}
	defer func() { _ = media.StopVideo() }()

	addr := net.JoinHostPort(opts.Bind, strconv.Itoa(opts.Port))
	server := stream.New(stream.Config{Addr: addr, Path: "/cam.ts"}, log)
	if err := server.Start(); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "in use") {
			log.Error("port already in use", "port", opts.Port, "hint", "pass --port to pick another")
			return exitPort, false
		}
		log.Error("cannot start the stream server", "err", err)
		return exitPort, false
	}
	defer server.Close()

	if attempt == 0 {
		fmt.Println()
		fmt.Printf("  Streaming at:  %s\n", server.URL())
		fmt.Println()
		fmt.Printf("    mpv    %s\n", server.URL())
		fmt.Printf("    vlc    %s\n", server.URL())
		fmt.Printf("    ffplay %s\n", server.URL())
		fmt.Println()
	} else {
		log.Info("streaming again", "url", server.URL())
	}

	frames := 0
	var streamErr error
	for ctx.Err() == nil {
		packet, ok, err := media.NextMedia(ctx)
		if err != nil {
			streamErr = err
			break
		}
		if !ok {
			break
		}
		if !packet.IsVideo() {
			continue
		}
		server.Publish(packet)
		frames++
		if frames%600 == 0 {
			log.Info("relaying", "frames", frames, "viewers", server.Viewers(), "codec", packet.CodecName())
		}
	}

	switch {
	case ctx.Err() != nil:
		log.Info("stopped", "frames", frames)
		return exitOK, false
	case streamErr != nil:
		log.Error("media error", "err", streamErr)
		return exitSession, true
	case frames == 0:
		log.Error("the session was established but no media arrived")
		return exitNoMedia, true
	default:
		log.Warn("the camera stopped sending", "frames", frames)
		return exitOK, true
	}
}

// lanHandshakeTimeout bounds the local attempt. Waiting longer than this costs
// more than it saves: the relay is usually ready by then.
const lanHandshakeTimeout = 3 * time.Second

// dialCamera prefers a direct session on the local network and falls back to the
// relay.
//
// The camera's LAN address comes from the cloud device list, so the attempt is
// free when the camera is remote or behind another subnet: it fails in a few
// hundred milliseconds and the relay path runs unchanged.
func dialCamera(ctx context.Context, opts *cli.Options, target cloud.Device, uid pppp.UID,
	prov *cloud.Provisioning, log *slog.Logger) (*net.UDPConn, *net.UDPAddr, error) {

	if opts.LAN != cli.LANOff && target.LocalIP != "" {
		ip, err := netip.ParseAddr(target.LocalIP)
		if err != nil {
			log.Debug("the camera reports no usable local address", "localip", target.LocalIP)
		} else {
			sock, peer, lanErr := pppp.LANHandshake(ip, pppp.LANPort, lanHandshakeTimeout, log)
			if lanErr == nil {
				log.Info("direct local session", "camera", ip.String(), "peer", peer.String())
				return sock, peer, nil
			}
			if opts.LAN == cli.LANOnly {
				return nil, nil, lanErr
			}
			log.Warn("no direct local session, using the relay",
				"camera", target.LocalIP, "err", lanErr)
		}
	}

	sock, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot open a UDP socket: %w", err)
	}

	log.Info("connecting via the CS2 relay", "p2p_id", prov.P2PID, "servers", len(prov.Servers))
	sess, err := pppp.Dial(ctx, sock, uid, prov.Servers, opts.ConnectTimeout, log)
	if err != nil {
		sock.Close()
		return nil, nil, fmt.Errorf("%w (if the Mi Home app is holding this camera, close it and retry)", err)
	}
	return sock, sess.Relay(), nil
}

// localEndpoint reports the address we present to the relay in the 0x28 reply:
// the egress interface, with the session socket's port.
func localEndpoint(sock *net.UDPConn, relay *net.UDPAddr) pppp.Addr {
	la, ok := sock.LocalAddr().(*net.UDPAddr)
	if !ok {
		return pppp.Addr{}
	}
	fallback := pppp.AddrFromUDP(la)

	probe, err := net.DialUDP("udp4", nil, relay)
	if err != nil {
		return fallback
	}
	defer probe.Close()

	egress, ok := probe.LocalAddr().(*net.UDPAddr)
	if !ok {
		return fallback
	}
	if addr, err := pppp.NewAddr(egress.IP.String(), uint16(la.Port)); err == nil {
		return addr
	}
	return fallback
}
