package main

import (
	"fmt"
	"os"
	"strconv"

	"micam/internal/cli"
	"micam/internal/ctl"
)

// runCtl serves one management API request.
//
// fnOS runs it as a CGI behind its own gateway, which is also what keeps the
// stream's management surface authenticated without opening another port.
func runCtl() int {
	server := &ctl.Server{
		Paths:   ctl.FromEnv(),
		Version: cli.FullVersion(),
		Log:     newLogger(os.Getenv("MICAM_VERBOSE") != ""),
	}
	return server.Serve()
}

// runWriteConfig stores what the configuration wizard collected.
//
// The wizard passes its values as environment variables named after the fields, so
// this exists to reuse the same writer as the UI rather than write the file twice
// in two languages.
func runWriteConfig() int {
	paths := ctl.FromEnv()
	cfg := ctl.Config{
		User:      os.Getenv("wizard_user"),
		UserID:    os.Getenv("wizard_user_id"),
		Region:    os.Getenv("wizard_region"),
		DID:       os.Getenv("wizard_did"),
		Quality:   intOr(os.Getenv("wizard_quality"), 3),
		PTSLead:   intOr(os.Getenv("wizard_pts_lead"), 100),
		LAN:       os.Getenv("wizard_lan"),
		Reconnect: intOr(os.Getenv("MICAM_RECONNECT"), 5),
		HLS:       boolOr(os.Getenv("wizard_hls"), true),
		HLSWindow: intOr(os.Getenv("wizard_hls_window"), 6),
		PassToken: os.Getenv("wizard_pass_token"),
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "micam:", err)
		return exitLogin
	}
	if err := paths.WriteConfig(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "micam:", err)
		return exitLogin
	}
	return exitOK
}

func intOr(value string, fallback int) int {
	n, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return n
}

// boolOr treats an unset variable as the fallback: a wizard field that was never
// filled in must not silently turn a feature off.
func boolOr(value string, fallback bool) bool {
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}
