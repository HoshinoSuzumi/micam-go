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

	// --dump writes a file instead of serving, so there is nothing to listen on.
	if opts.Dump != "" {
		return streamSession(ctx, opts, client, target, 1, nil, rep, log).code
	}

	server, reachable, code := startStreamServer(opts, rep, log)
	if server == nil {
		return code
	}
	defer server.Close()

	printBanner(server, reachable, opts)

	pacer := &retryPacer{max: opts.Reconnect}
	session := 0

	for {
		// On-demand: the camera is only asked for a stream when somebody is
		// watching. Without this it encodes around the clock, viewer or not.
		if opts.IdleStop > 0 {
			rep.SetState(status.StateIdle, nil)
			if !server.WaitForViewer(ctx) {
				rep.SetState(status.StateStopped, nil)
				return exitOK
			}
		}

		session++
		out := streamSession(ctx, opts, client, target, session, server, rep, log)

		if ctx.Err() != nil {
			rep.SetState(status.StateStopped, nil)
			return out.code
		}
		if out.idle {
			log.Info("nobody is watching any more, releasing the camera",
				"after_min", opts.IdleStop)
			continue
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

// startStreamServer binds the HTTP server for the life of the process.
//
// It used to be built inside every session, which closed the port for the whole
// reconnect delay: a player then got "connection refused" instead of a stall, and
// on-demand streaming had nowhere to wait for its first viewer.
func startStreamServer(opts *cli.Options, rep *status.Reporter,
	log *slog.Logger) (*stream.Server, []stream.ReachableURL, int) {

	addr := net.JoinHostPort(opts.Bind, strconv.Itoa(opts.Port))
	server := stream.New(stream.Config{
		Addr:      addr,
		Path:      "/cam.ts",
		PTSLeadMS: opts.PTSLeadMS,
		HLS:       opts.HLS,
		HLSWindow: opts.HLSWindow,
		Audio:     opts.Audio,
		AudioRate: opts.AudioRate,
	}, log)
	if err := server.Start(); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "in use") {
			log.Error("port already in use", "port", opts.Port, "hint", "pass --port to pick another")
			return nil, nil, exitPort
		}
		log.Error("cannot start the stream server", "err", err)
		return nil, nil, exitPort
	}

	reachable := server.ReachableURLs()
	endpoints := make([]status.StreamURL, 0, len(reachable))
	for _, u := range reachable {
		endpoints = append(endpoints, status.StreamURL{URL: u.URL, Iface: u.Iface})
	}
	rep.Bind(server.Addr(), endpoints)

	// The playlist a Safari or iOS player needs. It is a different endpoint, not
	// a different address, so it is derived from the same list.
	if opts.HLS {
		hls := make([]status.StreamURL, 0, len(reachable)+1)
		for _, u := range reachable {
			hls = append(hls, status.StreamURL{URL: server.HLSURLOf(u.URL), Iface: u.Iface})
		}
		if len(hls) == 0 {
			hls = append(hls, status.StreamURL{URL: server.HLSURLOf(server.URL())})
		}
		rep.BindHLS(hls)
	}

	if opts.Audio {
		audio := make([]status.StreamURL, 0, len(reachable)+1)
		for _, u := range reachable {
			audio = append(audio, status.StreamURL{URL: server.AudioURLOf(u.URL), Iface: u.Iface})
		}
		if len(audio) == 0 {
			audio = append(audio, status.StreamURL{URL: server.AudioURLOf(server.URL())})
		}
		rep.BindAudio(audio)
	}

	if server.Loopback() {
		// The single most confusing failure: everything looks healthy, but nothing
		// outside this machine can reach the stream.
		log.Warn("the stream listens on loopback only, so nothing outside this machine can open it",
			"listen", server.Addr(), "hint", "set --bind 0.0.0.0 (the fnOS package already does)")
	}

	return server, reachable, exitOK
}

// printBanner is what a human sees once at startup: where it listens, and which
// addresses somebody else could use.
func printBanner(server *stream.Server, reachable []stream.ReachableURL, opts *cli.Options) {
	fmt.Println()
	fmt.Printf("  Listening on:  %s\n", server.Addr())
	fmt.Printf("  Local URL:     %s\n", server.URL())

	if len(reachable) > 0 {
		fmt.Println()
		fmt.Println("  Reachable at:")
		for _, u := range reachable {
			fmt.Printf("    %-40s (%s)\n", u.URL, u.Iface)
		}
	}

	player := server.URL()
	if len(reachable) > 0 {
		player = reachable[0].URL
	}
	fmt.Println()
	fmt.Printf("    mpv    %s\n", player)
	fmt.Printf("    vlc    %s\n", player)
	fmt.Printf("    ffplay %s\n", player)

	if opts.HLS {
		fmt.Println()
		fmt.Println("  HLS (Safari / iOS):")
		if len(reachable) > 0 {
			fmt.Printf("    %s\n", server.HLSURLOf(reachable[0].URL))
		} else {
			fmt.Printf("    %s\n", server.HLSURLOf(server.URL()))
		}
	}

	if opts.Audio {
		fmt.Println("  Audio (WAV):")
		if len(reachable) > 0 {
			fmt.Printf("    %s\n", server.AudioURLOf(reachable[0].URL))
		} else {
			fmt.Printf("    %s\n", server.AudioURLOf(server.URL()))
		}
	}
	fmt.Println()

	if opts.IdleStop > 0 {
		fmt.Printf("  无人观看 %d 分钟后释放摄像头，有人连上再重新拉流。\n\n", opts.IdleStop)
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
	// idle marks a session that ended on purpose because nobody was watching.
	idle bool
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
// streamSession runs one attempt against the server runCamera already started.
//
// server is nil only for --dump, which writes a file instead of serving.
func streamSession(ctx context.Context, opts *cli.Options, client *cloud.Client,
	target cloud.Device, session int, server *stream.Server, rep *status.Reporter,
	log *slog.Logger) outcome {

	rep.BeginSession(session)
	rep.SetState(status.StateConnecting, nil)
	rep.Stage(status.StageProvisioning)

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

	rep.Stage(status.StageDialing)
	link, local, err := dialCamera(ctx, opts, target, uid, prov, log)
	if err != nil {
		log.Error("cannot reach the camera", "err", err)
		return outcome{code: exitSession, retryable: true, err: err}
	}

	rep.Connected(target.Name, link.Describe())

	conn := pppp.NewConn(link, local, log)
	conn.Start()
	defer conn.Close()

	rep.Stage(status.StageAuthenticating)
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

	rep.Stage(status.StageStartingVideo)
	log.Info("requesting video", "quality", opts.Quality, "pts_lead_ms", opts.PTSLeadMS)
	if err := media.StartVideo(opts.Quality, opts.Audio); err != nil {
		log.Error("cannot start the video stream", "err", err)
		return outcome{code: exitSession, retryable: true, err: err}
	}
	defer func() { _ = media.StopVideo() }()

	// From here the camera is supposed to be sending. If it never does, the
	// watchdog must not sit on its "no frames yet" exemption forever.
	conn.ArmIdle()

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

	if session > 1 {
		log.Info("streaming again", "listen", server.Addr(), "url", server.URL())
	}
	frames := 0
	audioFrames := 0
	paused := false
	var streamErr error

	idleAfter := time.Duration(opts.IdleStop) * time.Minute

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

		// Audio goes to its own endpoint and its own listeners; the video side is
		// untouched by it.
		if packet.IsAudio() {
			server.Publish(packet)
			if audioFrames == 0 {
				log.Info("audio arrived", "codec", packet.CodecName(), "bytes", len(packet.Payload))
			}
			audioFrames++
			continue
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
		if frames%20 == 0 {
			rep.Connections(server.ViewersInfo())
			if idleAfter > 0 && server.IdleFor() > idleAfter {
				log.Info("nobody has watched for a while, releasing the camera",
					"idle", server.IdleFor().Round(time.Second).String())
				paused = true
				break
			}
		}
		if frames%600 == 0 {
			log.Info("relaying", "frames", frames, "viewers", server.Viewers(),
				"resyncs", server.Resyncs(), "codec", packet.CodecName())
		}
	}

	out := outcome{
		code:     exitOK,
		idle:     paused,
		frames:   frames,
		duration: time.Since(connectedAt),
		err:      conn.Err(),
	}

	switch {
	case paused:
		// Ending on purpose is not a failure: the next viewer brings the camera
		// back, and this must not spend the reconnect budget.
		out.retryable = false
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
