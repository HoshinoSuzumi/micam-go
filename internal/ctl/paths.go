// Package ctl is the management API behind the fnOS app UI.
//
// It runs as a CGI: one process per request, started by the gateway with the usual
// CGI environment. Keeping it out of the stream process means the UI cannot block
// or crash the relay, and it inherits the gateway's authentication instead of
// opening another port on the NAS.
package ctl

import (
	"os"
	"path/filepath"
)

// Paths is where the installed application keeps its files.
type Paths struct {
	// App is /var/apps/<appname>: the lifecycle scripts live in App/cmd.
	App  string
	Dest string // TRIM_APPDEST, the installed target directory
	ETC  string // TRIM_PKGETC, configuration
	VAR  string // TRIM_PKGVAR, runtime data
}

// FromEnv builds Paths from the variables fnOS exports to lifecycle scripts and
// CGIs. The defaults match the documented layout so a manual run still works.
func FromEnv() Paths {
	name := envOr("TRIM_APPNAME", "micam")
	dest := envOr("TRIM_APPDEST", filepath.Join("/var/apps", name, "target"))

	return Paths{
		App:  filepath.Dir(dest),
		Dest: dest,
		ETC:  envOr("TRIM_PKGETC", filepath.Join("/var/apps", name, "etc")),
		VAR:  envOr("TRIM_PKGVAR", filepath.Join("/var/apps", name, "var")),
	}
}

// Main is the lifecycle script that owns start/stop, so the UI and the app centre
// never disagree about how the service is run.
func (p Paths) Main() string { return filepath.Join(p.App, "cmd", "main") }

// Binary is the installed executable.
func (p Paths) Binary() string { return filepath.Join(p.Dest, "bin", "micam") }

func (p Paths) EnvFile() string    { return filepath.Join(p.ETC, "micam.env") }
func (p Paths) TokenFile() string  { return filepath.Join(p.ETC, "pass_token") }
func (p Paths) StatusFile() string { return filepath.Join(p.VAR, "status.json") }
func (p Paths) PIDFile() string    { return filepath.Join(p.VAR, "micam.pid") }
func (p Paths) LogFile() string    { return filepath.Join(p.VAR, "micam.log") }

// CredentialsFile is the cached Mi session; its presence means a login already
// happened and the service can start without a token.
func (p Paths) CredentialsFile() string {
	return filepath.Join(p.VAR, "data", "credentials.json")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
