// Command micam streams a Xiaomi Mi Home camera over the CS2 relay and
// republishes it as MPEG-TS over HTTP.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"micam/internal/cli"
	"micam/internal/cloud"
	"micam/internal/creds"
	"micam/internal/miss"
	"micam/internal/pppp"
	"micam/internal/stream"
)

// Exit codes mirror the upstream tool so scripts keep working.
const (
	exitOK       = 0
	exitNoCamera = 1
	exitLogin    = 2
	exitSession  = 3
	exitOffline  = 4
	exitNoMedia  = 5
	exitPort     = 6
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	opts, err := cli.Parse(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		fmt.Fprintln(os.Stderr, "micam:", err)
		return exitLogin
	}
	if opts.ShowVersion {
		fmt.Println(cli.Version)
		return exitOK
	}

	log := newLogger(opts.Verbose)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return serve(ctx, opts, log)
}

func serve(ctx context.Context, opts *cli.Options, log *slog.Logger) int {
	store, err := creds.DefaultStore()
	if err != nil {
		log.Error("cannot locate the credentials file", "err", err)
		return exitLogin
	}
	if opts.Relogin {
		if err := store.Delete(); err != nil {
			log.Warn("cannot discard the cached session", "err", err)
		}
	}

	c, err := store.Load()
	if err != nil {
		log.Error("cannot read the credentials file", "err", err)
		return exitLogin
	}
	opts.Apply(c)

	if c.User == "" {
		user, err := cli.PromptRequired("Mi account (id, email or phone)")
		if err != nil {
			log.Error("no account given", "err", err)
			return exitLogin
		}
		c.User = user
	}

	client, err := cloud.New(c)
	if err != nil {
		log.Error("cannot prepare the cloud client", "err", err)
		return exitLogin
	}

	// Persist the device id right away: Xiaomi binds two-factor approval to it,
	// so it must stay stable across runs even when login fails.
	if err := store.Save(c); err != nil {
		log.Warn("cannot save the credentials", "err", err)
	}

	if !c.HasSession() {
		if code := authenticate(ctx, client, opts, store, log); code != exitOK {
			return code
		}
	} else {
		log.Info("using the cached session", "user", c.UserID)
	}

	devices, err := client.DeviceList(ctx)
	if err != nil {
		// Most likely an expired serviceToken: renew and retry once.
		log.Warn("the session was rejected, logging in again", "err", err)
		c.Ssecurity, c.ServiceToken = "", ""
		if err := client.Login(ctx, opts.Password); err != nil {
			log.Error("login failed", "err", err)
			return exitLogin
		}
		if err := store.Save(c); err != nil {
			log.Warn("cannot save the credentials", "err", err)
		}
		if devices, err = client.DeviceList(ctx); err != nil {
			log.Error("cannot list devices", "err", err)
			return exitLogin
		}
	}

	cameras := make([]cloud.Device, 0, len(devices))
	for _, d := range devices {
		if d.IsCamera() {
			cameras = append(cameras, d)
		}
	}
	if len(cameras) == 0 {
		log.Error("no cameras found on this account")
		for _, d := range devices {
			log.Info("device", "did", d.DID, "model", d.Model, "name", d.Name, "online", d.Online)
		}
		return exitNoCamera
	}

	target, err := pickCamera(cameras, opts.DID)
	if err != nil {
		log.Error("cannot select a camera", "err", err)
		return exitNoCamera
	}
	log.Info("selected camera", "name", target.Name, "did", target.DID, "model", target.Model)

	if !target.Online {
		log.Error("the cloud reports this camera offline",
			"hint", "an offline camera is not registered with the rendezvous servers")
		return exitOffline
	}

	keys, err := miss.GenerateKeys()
	if err != nil {
		log.Error("cannot generate a key pair", "err", err)
		return exitSession
	}
	prov, err := client.Provision(ctx, target.DID, keys.PublicHex())
	if err != nil {
		log.Error("provisioning failed", "err", err)
		return exitSession
	}
	if !prov.IsCS2() {
		log.Error("unsupported transport", "vendor", prov.VendorName(),
			"hint", "only cs2 is implemented")
		return exitNoCamera
	}

	uid, err := pppp.ParseUID(prov.P2PID)
	if err != nil {
		log.Error("cannot parse the camera's P2P id", "err", err, "p2p_id", prov.P2PID)
		return exitSession
	}

	sock, err := net.ListenUDP("udp4", nil)
	if err != nil {
		log.Error("cannot open a UDP socket", "err", err)
		return exitSession
	}
	defer sock.Close()

	log.Info("connecting via the CS2 relay", "p2p_id", prov.P2PID, "servers", len(prov.Servers))
	sess, err := pppp.Dial(ctx, sock, uid, prov.Servers, opts.ConnectTimeout, log)
	if err != nil {
		log.Error("relay session failed", "err", err,
			"hint", "if the Mi Home app is holding this camera, close it and retry")
		return exitSession
	}

	local := localEndpoint(sock, sess.Relay())
	conn := pppp.NewConn(sock, sess.Relay(), local, log)
	conn.Start()
	defer conn.Close()

	client2 := miss.NewClient(conn)
	if _, err := client2.Authenticate(ctx, keys.PublicHex(), prov.Sign, 15*time.Second); err != nil {
		log.Error("MISS authentication failed", "err", err)
		return exitSession
	}

	key, err := miss.SessionKey(keys.Private, prov.DevicePublic)
	if err != nil {
		log.Error("cannot derive the session key", "err", err)
		return exitSession
	}
	client2.UseSessionKey(key)

	if err := client2.StartVideo(opts.Quality, false); err != nil {
		log.Error("cannot start the video stream", "err", err)
		return exitSession
	}
	defer func() { _ = client2.StopVideo() }()

	addr := net.JoinHostPort(opts.Bind, strconv.Itoa(opts.Port))
	server := stream.New(stream.Config{Addr: addr, Path: "/cam.ts"}, log)
	if err := server.Start(); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "in use") {
			log.Error("port already in use", "port", opts.Port, "hint", "pass --port to pick another")
			return exitPort
		}
		log.Error("cannot start the stream server", "err", err)
		return exitPort
	}
	defer server.Close()

	fmt.Println()
	fmt.Printf("  Streaming at:  %s\n", server.URL())
	fmt.Println()
	fmt.Printf("    mpv    %s\n", server.URL())
	fmt.Printf("    vlc    %s\n", server.URL())
	fmt.Printf("    ffplay %s\n", server.URL())
	fmt.Println()

	frames := 0
	for ctx.Err() == nil {
		p, ok, err := client2.NextMedia(ctx)
		if err != nil {
			log.Error("media error", "err", err)
			break
		}
		if !ok {
			break
		}
		if !p.IsVideo() {
			continue
		}
		server.Publish(p)
		frames++
		if frames%600 == 0 {
			log.Info("relaying", "frames", frames, "viewers", server.Viewers(), "codec", p.CodecName())
		}
	}

	if frames == 0 {
		log.Error("the session was established but no media arrived")
		return exitNoMedia
	}
	log.Info("stopped", "frames", frames)
	return exitOK
}

func authenticate(ctx context.Context, client *cloud.Client, opts *cli.Options,
	store *creds.Store, log *slog.Logger) int {

	c := client.Credentials()

	password := opts.Password
	if password == "" && !c.CanRenew() {
		entered, err := cli.PromptSecret("Password (leave blank to paste a passToken)")
		if err != nil {
			log.Error("no password given", "err", err)
			return exitLogin
		}
		password = entered
		if password == "" {
			token, err := cli.PromptRequired("passToken")
			if err != nil {
				log.Error("no passToken given", "err", err)
				return exitLogin
			}
			c.PassToken = token
		}
	}

	if err := client.Login(ctx, password); err != nil {
		var twoFactor *cloud.TwoFactorError
		switch {
		case errors.As(err, &twoFactor):
			log.Error("two-factor verification required",
				"url", twoFactor.URL,
				"hint", "confirming that URL will not help; copy the passToken cookie from a browser")
		case errors.Is(err, cloud.ErrPassTokenRejected):
			log.Error("the passToken was rejected by the account service",
				"hint", "the userId cookie must be the numeric account id: pass --user-id 1234567890, or log in with --password")
		case errors.Is(err, cloud.ErrNoPassword):
			log.Error("no cached session and no password",
				"hint", "pass --pass-token or set MICAM_PASS_TOKEN")
		default:
			log.Error("login failed", "err", err)
		}
		return exitLogin
	}

	log.Info("logged in", "user", c.UserID)
	if err := store.Save(c); err != nil {
		log.Warn("cannot save the credentials", "err", err)
	}
	return exitOK
}

func pickCamera(cameras []cloud.Device, did string) (cloud.Device, error) {
	if did != "" {
		for _, d := range cameras {
			if d.DID == did {
				return d, nil
			}
		}
		return cloud.Device{}, fmt.Errorf("no such camera: %s", did)
	}

	labels := make([]string, len(cameras))
	for i, d := range cameras {
		labels[i] = fmt.Sprintf("%-34s %-26s %s", d.Name, d.Model, d.DID)
	}
	index, err := cli.Choose("Cameras:", labels)
	if err != nil {
		return cloud.Device{}, err
	}
	return cameras[index], nil
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

func newLogger(verbose bool) *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	return slog.New(handler)
}
