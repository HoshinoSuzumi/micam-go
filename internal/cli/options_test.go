package cli

import (
	"os"
	"testing"
	"time"

	"micam/internal/creds"
)

// clearEnv hides every MICAM_* variable so a developer's environment cannot
// change what these tests assert.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"MICAM_USER", "MICAM_USER_ID", "MICAM_REGION", "MICAM_PASSWORD",
		"MICAM_PASS_TOKEN", "MICAM_PASS_TOKEN_FILE", "MICAM_BIND", "MICAM_PORT",
		"MICAM_QUALITY", "MICAM_RECONNECT",
		// Access control. These are all "set but empty" - an empty list - which
		// is a meaningful value for each of them.
		"MICAM_TOKEN", "MICAM_TOKEN_FILE", "MICAM_ALLOW_CIDR",
		"MICAM_TRUSTED_PROXY", "MICAM_NO_LOCAL_BYPASS", "MICAM_SHARE_TTL",
	} {
		t.Setenv(key, "")
	}
	// MICAM_TRUST_IFACE is the exception: "" means "trust no interface", while
	// unset means "trust the tailnet". The default test needs the second one.
	unsetEnv(t, "MICAM_TRUST_IFACE")
}

// unsetEnv removes a variable for the duration of one test, since t.Setenv can
// only make a variable empty.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	old, had := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unsetenv %s: %v", key, err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, old)
		}
	})
}

func TestParseDefaults(t *testing.T) {
	clearEnv(t)

	opts, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if opts.Bind != "127.0.0.1" {
		t.Fatalf("bind = %q, want 127.0.0.1", opts.Bind)
	}
	if opts.Port != 8554 {
		t.Fatalf("port = %d, want 8554", opts.Port)
	}
	if opts.Quality != 2 {
		t.Fatalf("quality = %d, want 2", opts.Quality)
	}
	if opts.Region != creds.DefaultRegion {
		t.Fatalf("region = %q, want %q", opts.Region, creds.DefaultRegion)
	}
	if opts.Reconnect != 0 {
		t.Fatalf("reconnect = %d, want 0 (disabled)", opts.Reconnect)
	}
	if opts.ListCameras {
		t.Fatal("--list-cameras must default to false")
	}
}

func TestParseNewFlags(t *testing.T) {
	clearEnv(t)

	opts, err := Parse([]string{"--list-cameras", "--reconnect", "3"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !opts.ListCameras {
		t.Fatal("--list-cameras was not picked up")
	}
	if opts.Reconnect != 3 {
		t.Fatalf("reconnect = %d, want 3", opts.Reconnect)
	}

	opts, err = Parse([]string{"--reconnect", "0"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if opts.Reconnect != 0 {
		t.Fatalf("reconnect = %d, want 0", opts.Reconnect)
	}
}

func TestParseDataDir(t *testing.T) {
	clearEnv(t)

	opts, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if opts.DataDir != "" {
		t.Fatalf("data dir = %q, want empty (default)", opts.DataDir)
	}

	opts, err = Parse([]string{"--data-dir", `D:\micam-data`})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if opts.DataDir != `D:\micam-data` {
		t.Fatalf("data dir = %q", opts.DataDir)
	}
}

func TestParseLAN(t *testing.T) {
	clearEnv(t)

	opts, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if opts.LAN != LANPrefer {
		t.Fatalf("lan = %q, want %q by default", opts.LAN, LANPrefer)
	}

	for _, tc := range []struct{ in, want string }{
		{"only", LANOnly},
		{"off", LANOff},
		{"OFF", LANOff},
		{" prefer ", LANPrefer},
	} {
		opts, err := Parse([]string{"--lan", tc.in})
		if err != nil {
			t.Fatalf("Parse(--lan %q): %v", tc.in, err)
		}
		if opts.LAN != tc.want {
			t.Fatalf("--lan %q became %q, want %q", tc.in, opts.LAN, tc.want)
		}
	}

	if _, err := Parse([]string{"--lan", "sometimes"}); err == nil {
		t.Fatal("an unknown --lan value must be rejected")
	}
}

func TestParseQualityRange(t *testing.T) {
	clearEnv(t)

	// The protocol carries 0-5. Newer cameras put full resolution at 3, so the
	// old 0-3 ceiling silently capped them at HD.
	for _, q := range []string{"0", "2", "3", "5"} {
		if _, err := Parse([]string{"--quality", q}); err != nil {
			t.Fatalf("--quality %s should be accepted: %v", q, err)
		}
	}
	for _, q := range []string{"-1", "6"} {
		if _, err := Parse([]string{"--quality", q}); err == nil {
			t.Fatalf("--quality %s should be rejected", q)
		}
	}
}

func TestParsePTSLead(t *testing.T) {
	clearEnv(t)

	opts, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if opts.PTSLeadMS != 100 {
		t.Fatalf("default pts lead = %d, want 100", opts.PTSLeadMS)
	}

	opts, err = Parse([]string{"--pts-lead", "0"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if opts.PTSLeadMS != 0 {
		t.Fatalf("pts lead = %d, want 0", opts.PTSLeadMS)
	}

	if _, err := Parse([]string{"--pts-lead", "5000"}); err == nil {
		t.Fatal("an out-of-range pts lead must be rejected")
	}
}

func TestParseRejectsNegativeReconnect(t *testing.T) {
	clearEnv(t)

	if _, err := Parse([]string{"--reconnect", "-1"}); err == nil {
		t.Fatal("a negative reconnect count must be rejected")
	}
}

func TestApplyFoldsUserIDIntoCredentials(t *testing.T) {
	clearEnv(t)

	opts, err := Parse([]string{
		"--user", "15023365994",
		"--user-id", "1234567890",
		"--pass-token", "  token-with-space\n",
		"--region", "cn",
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	c := &creds.Credentials{}
	opts.Apply(c)

	if c.UserID != "1234567890" {
		t.Fatalf("userId = %q, want the numeric id", c.UserID)
	}
	if c.PassToken != "token-with-space" {
		t.Fatalf("passToken = %q, want it trimmed", c.PassToken)
	}
	if c.User != "15023365994" || c.Region != "cn" {
		t.Fatalf("credentials = %+v", c)
	}
}

// The fnOS package and the container pass their settings as environment
// variables, so the merge has to work without any flag on the command line.
func TestParseAccessControlFromEnv(t *testing.T) {
	clearEnv(t)
	t.Setenv("MICAM_TOKEN", "living-room=TV-token-aaaaaaaaaaaa,phone-token-bbbbbbbbbbbb")
	t.Setenv("MICAM_ALLOW_CIDR", "192.168.0.0/24, 10.0.0.0/8")
	t.Setenv("MICAM_TRUST_IFACE", "")
	t.Setenv("MICAM_NO_LOCAL_BYPASS", "true")
	t.Setenv("MICAM_SHARE_TTL", "1h")

	opts, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(opts.Tokens) != 2 || opts.Tokens[0] != "living-room=TV-token-aaaaaaaaaaaa" {
		t.Fatalf("tokens = %q", opts.Tokens)
	}
	if len(opts.AllowCIDRs) != 2 || opts.AllowCIDRs[1] != "10.0.0.0/8" {
		t.Fatalf("allow-cidr = %q", opts.AllowCIDRs)
	}
	// Set-but-empty is not the same as unset: it means "trust no interface".
	if len(opts.TrustIfaces) != 0 {
		t.Fatalf("trust-iface = %q, want none", opts.TrustIfaces)
	}
	if !opts.NoLocalBypass {
		t.Fatal("no-local-bypass was not read from the environment")
	}
	if opts.ShareTTL != time.Hour {
		t.Fatalf("share-ttl = %s", opts.ShareTTL)
	}
}

// With nothing configured, a tailnet is trusted by interface: a second password
// on top of WireGuard and its ACLs is noise.
func TestParseDefaultsToTailscaleInterface(t *testing.T) {
	clearEnv(t)

	opts, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(opts.TrustIfaces) != 1 || opts.TrustIfaces[0] != "tailscale0" {
		t.Fatalf("trust-iface = %q", opts.TrustIfaces)
	}
	if len(opts.Tokens) != 0 {
		t.Fatalf("tokens = %q, want none by default", opts.Tokens)
	}
	if opts.ShareTTL != 12*time.Hour {
		t.Fatalf("share-ttl = %s", opts.ShareTTL)
	}
}
