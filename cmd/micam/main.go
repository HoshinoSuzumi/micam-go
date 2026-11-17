// Command micam streams a Xiaomi Mi Home camera over the CS2 relay and
// republishes it as MPEG-TS over HTTP.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"micam/internal/cli"
	"micam/internal/cloud"
	"micam/internal/creds"
	"micam/internal/status"
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
	// Hidden subcommands. "ctl" is the CGI the fnOS app UI talks to; "write-config"
	// lets the configuration wizard reuse the same writer as the UI, so the two
	// entry points cannot disagree about the file format.
	if len(args) > 0 {
		switch args[0] {
		case "ctl":
			return runCtl()
		case "write-config":
			return runWriteConfig()
		}
	}

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
	store, err := creds.StoreAt(opts.DataDir)
	if err != nil {
		log.Error("cannot resolve the credentials directory", "err", err)
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

	// The account name is only needed to derive the userId cookie for a login. An
	// explicit --user-id already answers that, so asking for it afterwards would be
	// noise on the one command that should be copy-pasteable.
	if c.User == "" && c.UserID == "" {
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
	// so it must stay stable across runs. A write failure is fatal: running on
	// would silently rotate the device id on every start.
	if err := store.Save(c); err != nil {
		log.Error("cannot write the credentials",
			"path", store.Path,
			"hint", "pass --data-dir with a writable directory",
			"err", err)
		return exitLogin
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

	if opts.ListCameras {
		return printCameras(cameras)
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

	reporter := status.Open(opts.StatusFile, log)
	defer reporter.Close()

	return runCamera(ctx, opts, client, target, reporter, log)
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
				"err", err,
				"hint", "the token expired, or it belongs to a different account than --user-id; "+
					"copy a fresh one from a browser logged into that account, "+
					"or reuse the token the container already renewed in data/credentials.json")
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

func newLogger(verbose bool) *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	return slog.New(handler)
}
