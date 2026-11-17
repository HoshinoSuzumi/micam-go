package ctl

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "micam.env")

	want := Defaults()
	want.User = "tim"
	want.UserID = "2255231260"
	want.DID = "1221672311"
	want.Quality = 2
	want.PTSLead = 0
	want.LAN = "only"
	want.Reconnect = 9

	if err := os.WriteFile(path, want.Encode(), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got != want {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

// The file is sourced by cmd/main with `set -a; . micam.env`, so a value with a
// quote, a dollar sign or a space must survive both directions.
func TestConfigIsShellSafe(t *testing.T) {
	requirePOSIX(t)

	path := filepath.Join(t.TempDir(), "micam.env")

	cfg := Defaults()
	cfg.User = `we'ird $HOME "name"`
	cfg.UserID = "2255231260"
	cfg.DID = "1221672311"

	if err := os.WriteFile(path, cfg.Encode(), 0o600); err != nil {
		t.Fatal(err)
	}

	script := `. "$1"; printf '%s\n%s' "$MICAM_USER" "$MICAM_DID"`
	out, err := exec.Command("sh", "-c", script, "sh", path).Output()
	if err != nil {
		t.Fatalf("sourcing the file failed: %v", err)
	}

	lines := strings.Split(string(out), "\n")
	if lines[0] != cfg.User {
		t.Fatalf("MICAM_USER = %q, want %q", lines[0], cfg.User)
	}
	if lines[1] != cfg.DID {
		t.Fatalf("MICAM_DID = %q", lines[1])
	}

	// ...and the same bytes parse back.
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.User != cfg.User {
		t.Fatalf("reloaded user = %q, want %q", got.User, cfg.User)
	}
}

// Files written by older versions of the wizard were unquoted; those must still
// parse, otherwise an upgrade silently loses the configuration.
func TestLoadConfigAcceptsTheOldFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "micam.env")
	old := `MICAM_USER=tim
MICAM_USER_ID=2255231260
MICAM_DID=1221672311
MICAM_QUALITY=3
MICAM_LAN=prefer
`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.User != "tim" || cfg.DID != "1221672311" || cfg.Quality != 3 {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestValidate(t *testing.T) {
	valid := func() Config {
		cfg := Defaults()
		cfg.UserID = "2255231260"
		cfg.DID = "1221672311"
		return cfg
	}

	base := valid()
	if err := base.Validate(); err != nil {
		t.Fatalf("a valid config was rejected: %v", err)
	}

	cases := map[string]func(*Config){
		"unknown region": func(c *Config) { c.Region = "mars" },
		"bad lan mode":   func(c *Config) { c.LAN = "maybe" },
		"did not digits": func(c *Config) { c.DID = "12ab" },
		"user id text":   func(c *Config) { c.UserID = "tim" },
		"quality range":  func(c *Config) { c.Quality = 6 },
		"pts range":      func(c *Config) { c.PTSLead = 9000 },
		"no account":     func(c *Config) { c.User, c.UserID = "", "" },
		"token spaces":   func(c *Config) { c.PassToken = " abc" },
		"token newline":  func(c *Config) { c.PassToken = "a\nb" },
	}
	for name, mutate := range cases {
		cfg := valid()
		mutate(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestConfiguredNeedsACameraAndCredentials(t *testing.T) {
	paths := Paths{
		ETC: t.TempDir(),
		VAR: t.TempDir(),
	}

	if err := os.WriteFile(paths.EnvFile(), (Defaults()).Encode(), 0o600); err != nil {
		t.Fatal(err)
	}
	if paths.Configured() {
		t.Fatal("nothing configured yet")
	}

	cfg := Defaults()
	cfg.UserID = "1"
	cfg.DID = "2"
	if err := paths.WriteConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if paths.Configured() {
		t.Fatal("a camera without a token or session cannot start")
	}

	if err := os.WriteFile(paths.TokenFile(), []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !paths.Configured() {
		t.Fatal("a did plus a token is enough")
	}
}
