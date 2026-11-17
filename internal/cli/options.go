// Package cli parses flags and runs the interactive prompts.
package cli

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"micam/internal/creds"
)

// Version is the release number, injected at build time:
//
//	go build -ldflags "-X 'micam/internal/cli.Version=0.6.2'"
//
// It is deliberately not a literal. A hardcoded value is wrong the moment the next
// tag is cut - 0.6.2 packages used to ship with a binary that said 0.5.0 - and the
// tag is the only place a release number actually lives.
var Version = "dev"

// FullVersion is what --version prints and what the management UI shows.
func FullVersion() string {
	if Version == "dev" {
		return "micam-go dev"
	}
	return "micam-go " + Version
}

// Options is the parsed command line.
type Options struct {
	User      string
	UserID    string
	Region    string
	Password  string
	PassToken string
	DID       string
	Bind      string
	DataDir   string
	Port      int
	Quality   int
	PTSLeadMS int

	ConnectTimeout time.Duration
	Relogin        bool
	Verbose        bool
	ShowVersion    bool
	ListCameras    bool
	Reconnect      int
	LAN            string
	Dump           string
	DumpSeconds    int
	StatusFile     string
}

// Values for --lan.
const (
	// LANPrefer tries a direct local session first and falls back to the relay.
	LANPrefer = "prefer"
	// LANOnly refuses to use the relay at all.
	LANOnly = "only"
	// LANOff skips the local attempt entirely.
	LANOff = "off"
)

// Parse reads the command line, falling back to environment variables for the
// values that a container deployment wants to supply without a TTY.
func Parse(args []string) (*Options, error) {
	opts := &Options{
		Bind:           envOr("MICAM_BIND", "127.0.0.1"),
		Port:           8554,
		Quality:        2,
		ConnectTimeout: 90 * time.Second,
		Region:         creds.DefaultRegion,
	}

	timeoutSeconds := int(opts.ConnectTimeout / time.Second)

	fs := flag.NewFlagSet("micam", flag.ContinueOnError)
	fs.StringVar(&opts.User, "user", envOr("MICAM_USER", ""), "Mi account id, email or phone")
	fs.StringVar(&opts.UserID, "user-id", envOr("MICAM_USER_ID", ""),
		"numeric Mi account id; required when logging in with a passToken")
	fs.StringVar(&opts.Region, "region", envOr("MICAM_REGION", creds.DefaultRegion),
		"account region: cn, de, sg, us, ru, i2")
	fs.StringVar(&opts.Password, "password", envOr("MICAM_PASSWORD", ""),
		"Mi account password (prefer a passToken)")
	fs.StringVar(&opts.PassToken, "pass-token", envOr("MICAM_PASS_TOKEN", ""),
		"passToken cookie copied from a logged-in browser")
	fs.StringVar(&opts.DID, "did", envOr("MICAM_DID", ""), "device id of the camera to stream")
	fs.StringVar(&opts.Bind, "bind", envOr("MICAM_BIND", "127.0.0.1"),
		"address the MPEG-TS server listens on")
	fs.StringVar(&opts.DataDir, "data-dir", envOr("MICAM_DATA_DIR", ""),
		"directory holding credentials.json (default: $HOME/.micam)")
	fs.IntVar(&opts.Port, "port", envIntOr("MICAM_PORT", 8554), "local HTTP port")
	fs.IntVar(&opts.Quality, "quality", envIntOr("MICAM_QUALITY", 2),
		"0 auto, 1 sd, 2 hd, 3 full; newer cameras may accept 4-5")
	fs.IntVar(&opts.PTSLeadMS, "pts-lead", envIntOr("MICAM_PTS_LEAD", 100),
		"presentation lead in ms: lower is snappier, higher is kinder to software decoders")
	fs.IntVar(&timeoutSeconds, "connect-timeout", 90,
		"seconds to keep retrying the relay handshake")
	fs.BoolVar(&opts.Relogin, "relogin", false, "discard the cached session and log in again")
	fs.BoolVar(&opts.Verbose, "verbose", envBoolOr("MICAM_VERBOSE", false),
		"trace every PPPP datagram")
	fs.BoolVar(&opts.ShowVersion, "version", false, "print the version and exit")
	fs.BoolVar(&opts.ListCameras, "list-cameras", false,
		"log in, print the cameras on the account and exit")
	fs.IntVar(&opts.Reconnect, "reconnect", envIntOr("MICAM_RECONNECT", 0),
		"retry the stream this many times when it ends (0 disables)")
	fs.StringVar(&opts.LAN, "lan", envOr("MICAM_LAN", LANPrefer),
		"prefer|only|off: try a direct local-network session before the relay")
	fs.StringVar(&opts.StatusFile, "status-file", envOr("MICAM_STATUS_FILE", ""),
		"publish the stream state to this JSON file, for the management UI")
	fs.StringVar(&opts.Dump, "dump", "",
		"write the raw elementary stream to this file instead of serving it")
	fs.IntVar(&opts.DumpSeconds, "dump-seconds", 5,
		"how many seconds to capture when using --dump")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	opts.ConnectTimeout = time.Duration(timeoutSeconds) * time.Second

	if file := os.Getenv("MICAM_PASS_TOKEN_FILE"); file != "" && opts.PassToken == "" {
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("cannot read MICAM_PASS_TOKEN_FILE: %w", err)
		}
		opts.PassToken = strings.TrimSpace(string(raw))
	}

	// The protocol carries 0-5; cameras ignore values they do not know, so a
	// wider range costs nothing and lets newer models be asked at full quality.
	if opts.Quality < 0 || opts.Quality > 5 {
		return nil, fmt.Errorf("quality must be 0..5, got %d", opts.Quality)
	}
	if opts.PTSLeadMS < 0 || opts.PTSLeadMS > 2000 {
		return nil, fmt.Errorf("pts-lead must be 0..2000 ms, got %d", opts.PTSLeadMS)
	}
	if opts.DumpSeconds < 0 {
		return nil, fmt.Errorf("dump-seconds must be >= 0, got %d", opts.DumpSeconds)
	}
	if opts.Port < 1 || opts.Port > 65535 {
		return nil, fmt.Errorf("port must be 1..65535, got %d", opts.Port)
	}
	if opts.Reconnect < 0 {
		return nil, fmt.Errorf("reconnect must be >= 0, got %d", opts.Reconnect)
	}

	opts.LAN = strings.ToLower(strings.TrimSpace(opts.LAN))
	switch opts.LAN {
	case LANPrefer, LANOnly, LANOff:
	default:
		return nil, fmt.Errorf("lan must be prefer, only or off, got %q", opts.LAN)
	}

	return opts, nil
}

// Apply folds the flags into the persisted credentials.
func (o *Options) Apply(c *creds.Credentials) {
	if o.User != "" {
		c.User = o.User
	}
	if o.UserID != "" {
		c.UserID = o.UserID
	}
	if o.Region != "" {
		c.Region = o.Region
	}
	if o.PassToken != "" {
		c.PassToken = strings.TrimSpace(o.PassToken)
	}
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func envIntOr(key string, fallback int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return fallback
	}
	return n
}

func envBoolOr(key string, fallback bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return parsed
}
