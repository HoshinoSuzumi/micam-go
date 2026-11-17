package main

import (
	"context"
	"encoding/binary"
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
	"micam/internal/status"
	"micam/internal/stream"
)

// healthySession is how long a session must have lived before a drop counts as a
// new incident instead of a continuation of the one that spent the retry budget.
//
// Without this, hours of successful relaying are silently paid for by the next
// five failures: the counter only ever went up, so the first bad night ended in
// "giving up" and a container restart loop.
const healthySession = 60 * time.Second

// provisionTimeout bounds the cloud call that precedes every attempt. The HTTP
// client's own timeout is finer for a login than for a reconnect, and waiting out
// resolver retries is what turned a camera hiccup into minutes of dead air.
const provisionTimeout = 15 * time.Second

// runCamera keeps a session alive. When the stream ends on its own (the camera
// rebooted, the relay dropped the session), it reconnects instead of exiting and
// waiting for the orchestrator to restart the container.
//
// Reconnecting rebuilds everything, including the HTTP server: a viewer that
// reconnects gets a fresh transport stream starting at a keyframe, rather than a
// PTS discontinuity in the middle of one.
func runCamera(ctx context.Context, opts *cli.Options, client *cloud.Client,
	target cloud.Device, rep *status.Reporter, log *slog.Logger) int {

	pacer := &retryPacer{max: opts.Reconnect}
	bannerShown := false
	session := 0

	for {
		session++
		out := streamSession(ctx, opts, client, target, session, !bannerShown, rep, log)
		bannerShown = bannerShown || out.streaming

		if ctx.Err() != nil {
			rep.SetState(status.StateStopped, nil)
			return out.code
		}
		if !out.retryable {
			rep.SetState(status.StateError, out.err)
			return out.code
		}

		stop, delay := pacer.after(out)
		if stop {
			if opts.Reconnect > 0 {
				log.Error("giving up", "attempts", pacer.attempt, "max", opts.Reconnect+1)
			}
			rep.SetState(status.StateError, out.err)
			return out.code
		}
		rep.SetState(status.StateRetrying, out.err)

		switch {
		case out.cloud:
			log.Warn("the Mi cloud is unreachable, retrying the same session",
				"err", out.err, "delay", delay.String())
		case out.healthy():
			log.Warn("the camera dropped the stream, reconnecting",
				"after", out.duration.Round(time.Second).String(),
				"frames", out.frames, "err", out.err, "delay", delay.String())
		default:
			log.Warn("stream ended, reconnecting",
				"attempt", pacer.attempt, "of", opts.Reconnect,
				"frames", out.frames, "err", out.err, "delay", delay.String())
		}

		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return out.code
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

// retryPacer decides when the reconnect loop gives up and how long to wait.
//
// Two failures need different treatment. A camera that drops the stream is a
// session problem and spends the --reconnect budget. An unreachable Mi API is not
// about the camera at all: restarting the container cannot fix it, and it would
// only repeat the same failing login, so it is retried on its own ladder and
// never exhausts the budget.
type retryPacer struct {
	max     int // --reconnect: how many consecutive session failures are allowed
	attempt int // consecutive session failures
	cloud   int // consecutive cloud failures
}

// after records one attempt and reports whether to stop, and how long to wait
// before trying again.
func (p *retryPacer) after(o outcome) (stop bool, delay time.Duration) {
	if o.healthy() {
		p.attempt, p.cloud = 0, 0
	}
	if p.max == 0 {
		return true, 0 // documented: no retrying at all, let the orchestrator decide
	}
	if o.cloud {
		p.cloud++
		return false, reconnectDelay(p.cloud)
	}

	p.attempt++
	if p.attempt > p.max {
		return true, 0
	}
	return false, reconnectDelay(p.attempt)
}

// outcome is what one attempt achieved, and what the loop should infer from it.
type outcome struct {
	code      int
	retryable bool
	// cloud marks a failure before the camera was reached: the Mi API is down or
	// unreachable, which says nothing about the camera behind it.
	cloud bool
	// streaming is set once the local server was announced to the user.
	streaming bool
	// frames counts relayed video frames and duration how long the session lived.
	frames   int
	duration time.Duration
	err      error
}

// healthy reports whether the session ran long enough to prove that the camera
// and the network were fine.
func (o outcome) healthy() bool {
	return o.frames > 0 && o.duration >= healthySession
}

// streamSession runs exactly one attempt: provision, connect, authenticate,
// serve. Everything it opens is released by its own defers, so a retry starts
// from a clean slate.
func streamSession(ctx context.Context, opts *cli.Options, client *cloud.Client,
	target cloud.Device, session int, showBanner bool, rep *status.Reporter,
	log *slog.Logger) outcome {

	rep.BeginSession(session)
	rep.SetState(status.StateConnecting, nil)

	keys, err := miss.GenerateKeys()
	if err != nil {
		log.Error("cannot generate a key pair", "err", err)
		return outcome{code: exitSession, retryable: true, err: err}
	}

	provCtx, cancel := context.WithTimeout(ctx, provisionTimeout)
	prov, err := client.Provision(provCtx, target.DID, keys.PublicHex())
	cancel()
	if err != nil {
		log.Error("provisioning failed", "err", err)
		return outcome{code: exitSession, retryable: true, cloud: true, err: err}
	}
	if !prov.IsCS2() {
		log.Error("unsupported transport", "vendor", prov.VendorName(),
			"hint", "only cs2 is implemented")
		return outcome{code: exitNoCamera}
	}

	uid, err := pppp.ParseUID(prov.P2PID)
	if err != nil {
		log.Error("cannot parse the camera's P2P id", "err", err, "p2p_id", prov.P2PID)
		return outcome{code: exitSession}
	}

	link, local, err := dialCamera(ctx, opts, target, uid, prov, log)
	if err != nil {
		log.Error("cannot reach the camera", "err", err)
		return outcome{code: exitSession, retryable: true, err: err}
	}

	rep.Connected(target.Name, link.Describe())

	conn := pppp.NewConn(link, local, log)
	conn.Start()
	defer conn.Close()

	media := miss.NewClient(conn)
	if _, err := media.Authenticate(ctx, keys.PublicHex(), prov.Sign, 15*time.Second); err != nil {
		log.Error("MISS authentication failed", "err", err)
		return outcome{code: exitSession, retryable: true, err: err}
	}

	key, err := miss.SessionKey(keys.Private, prov.DevicePublic)
	if err != nil {
		log.Error("cannot derive the session key", "err", err)
		return outcome{code: exitSession}
	}
	media.UseSessionKey(key)

	log.Info("requesting video", "quality", opts.Quality, "pts_lead_ms", opts.PTSLeadMS)
	if err := media.StartVideo(opts.Quality, false); err != nil {
		log.Error("cannot start the video stream", "err", err)
		return outcome{code: exitSession, retryable: true, err: err}
	}
	defer func() { _ = media.StopVideo() }()

	// From here on the camera considers the session live, so this is where the
	// health clock starts.
	connectedAt := time.Now()

	if opts.Dump != "" {
		code, retryable := dumpStream(ctx, media, opts, log)
		return outcome{
			code:      code,
			retryable: retryable,
			duration:  time.Since(connectedAt),
			err:       conn.Err(),
		}
	}

	addr := net.JoinHostPort(opts.Bind, strconv.Itoa(opts.Port))
	server := stream.New(stream.Config{
		Addr:      addr,
		Path:      "/cam.ts",
		PTSLeadMS: opts.PTSLeadMS,
	}, log)
	if err := server.Start(); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "in use") {
			log.Error("port already in use", "port", opts.Port, "hint", "pass --port to pick another")
			return outcome{code: exitPort}
		}
		log.Error("cannot start the stream server", "err", err)
		return outcome{code: exitPort}
	}
	defer server.Close()

	rep.Bind(server.Addr())

	if showBanner {
		fmt.Println()
		fmt.Printf("  Listening on:  %s\n", server.Addr())
		fmt.Printf("  Local URL:     %s\n", server.URL())
		fmt.Println()
		fmt.Printf("    mpv    %s\n", server.URL())
		fmt.Printf("    vlc    %s\n", server.URL())
		fmt.Printf("    ffplay %s\n", server.URL())
		fmt.Println()
	} else {
		log.Info("streaming again", "listen", server.Addr(), "url", server.URL())
	}

	if server.Loopback() {
		// The single most confusing failure: everything looks healthy, but nothing
		// outside this machine can reach the stream.
		log.Warn("the stream listens on loopback only, so nothing outside this machine can open it",
			"listen", server.Addr(), "hint", "set --bind 0.0.0.0 (the fnOS package already does)")
	}

	frames := 0
	var streamErr error
	for ctx.Err() == nil {
		// The camera keeps talking on channel 0 while it streams. Nothing here
		// needs those messages, but consuming them keeps the bounded queue from
		// overflowing, and logging the ids shows what we are ignoring.
		if command, ok := conn.TryNextCommand(); ok && len(command) >= 4 {
			log.Debug("camera command", "cmd", fmt.Sprintf("0x%03x", binary.BigEndian.Uint32(command[:4])))
		}

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
		if frames == 1 {
			rep.SetState(status.StateStreaming, nil)
		}
		rep.Frame(packet.CodecName())
		rep.Sample(server.Viewers(), server.Resyncs())
		if frames%600 == 0 {
			log.Info("relaying", "frames", frames, "viewers", server.Viewers(),
				"resyncs", server.Resyncs(), "codec", packet.CodecName())
		}
	}

	out := outcome{
		code:      exitOK,
		streaming: true,
		frames:    frames,
		duration:  time.Since(connectedAt),
		err:       conn.Err(),
	}

	switch {
	case ctx.Err() != nil:
		log.Info("stopped", "frames", frames)
		out.retryable = false
	case streamErr != nil:
		log.Error("media error", "err", streamErr)
		out.retryable, out.err = true, streamErr
	case frames == 0:
		log.Error("the session was established but no media arrived", "err", out.err)
		out.code, out.retryable = exitNoMedia, true
	default:
		// conn.Err() is what actually happened: the camera hung up, went quiet,
		// or the transport failed. Without it this line only said "stopped".
		log.Warn("the camera stopped sending", "frames", frames, "err", out.err)
		out.retryable = true
	}
	return out
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
	prov *cloud.Provisioning, log *slog.Logger) (pppp.Link, pppp.Addr, error) {

	if opts.LAN != cli.LANOff && target.LocalIP != "" {
		ip, err := netip.ParseAddr(target.LocalIP)
		if err != nil {
			log.Debug("the camera reports no usable local address", "localip", target.LocalIP)
		} else {
			link, lanErr := pppp.LANHandshake(uid, ip, lanHandshakeTimeout, log)
			if lanErr == nil {
				log.Info("direct local session", "camera", ip.String(), "transport", link.Describe())
				return link, pppp.Addr{}, nil
			}
			if opts.LAN == cli.LANOnly {
				return nil, pppp.Addr{}, lanErr
			}
			log.Warn("no direct local session, using the relay",
				"camera", target.LocalIP, "err", lanErr)
		}
	}

	sock, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return nil, pppp.Addr{}, fmt.Errorf("cannot open a UDP socket: %w", err)
	}

	log.Info("connecting via the CS2 relay", "p2p_id", prov.P2PID, "servers", len(prov.Servers))
	sess, err := pppp.Dial(ctx, sock, uid, prov.Servers, opts.ConnectTimeout, log)
	if err != nil {
		sock.Close()
		return nil, pppp.Addr{}, fmt.Errorf("%w (if the Mi Home app is holding this camera, close it and retry)", err)
	}

	local := localEndpoint(sock, sess.Relay())
	return pppp.NewUDPLink(sock, sess.Relay()), local, nil
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
