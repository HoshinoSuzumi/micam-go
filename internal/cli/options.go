// Package cli parses flags and runs the interactive prompts.
package cli

import (
	"flag"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"micam/internal/auth"
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
	// HLS serves a playlist next to the MPEG-TS stream: Safari and iOS cannot
	// play MPEG-TS at all.
	HLS bool
	// HLSWindow is how many segments the playlist keeps.
	HLSWindow int
	// Audio asks the camera for sound and serves it as a WAV stream.
	Audio bool
	// AudioRate is the sample rate of that sound.
	AudioRate int

	ConnectTimeout time.Duration
	Relogin        bool
	Verbose        bool
	ShowVersion    bool
	ListCameras    bool
	Reconnect      int
	IdleStop       int
	LAN            string
	Dump           string
	DumpSeconds    int
	StatusFile     string

	// Access control. Tokens are repeatable so one device can be revoked without
	// rotating the credential every device shares.
	Tokens      []string
	TokenFile   string
	AllowCIDRs  []string
	TrustIfaces []string
	// TrustedProxies are the peers whose X-Forwarded-For is believed when
	// resolving the client address.
	TrustedProxies []string
	AuthRealm      string
	AuthFailLimit  int
	// NoLocalBypass turns off the free pass for requests from this machine.
	NoLocalBypass bool
	PublicMetrics bool
	// ShareKey is a hex-encoded signing key; empty uses the one in the data
	// directory, which is created on first use.
	ShareKey string
	ShareTTL time.Duration
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
	fs.BoolVar(&opts.HLS, "hls", envBoolOr("MICAM_HLS", true),
		"also serve /cam.m3u8 for Safari and iOS")
	fs.IntVar(&opts.HLSWindow, "hls-window", envIntOr("MICAM_HLS_WINDOW", 6),
		"segments kept in the playlist: more is steadier, fewer is lower latency")
	fs.BoolVar(&opts.Audio, "audio", envBoolOr("MICAM_AUDIO", false),
		"ask the camera for sound and serve it as /cam.wav")
	fs.IntVar(&opts.AudioRate, "audio-rate", envIntOr("MICAM_AUDIO_RATE", 8000),
		"sample rate of the camera's audio, for the WAV header")
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
	fs.IntVar(&opts.IdleStop, "idle-stop", envIntOr("MICAM_IDLE_STOP", 0),
		"release the camera after this many minutes with no viewers (0 disables)")
	fs.StringVar(&opts.LAN, "lan", envOr("MICAM_LAN", LANPrefer),
		"prefer|only|off: try a direct local-network session before the relay")
	fs.StringVar(&opts.StatusFile, "status-file", envOr("MICAM_STATUS_FILE", ""),
		"publish the stream state to this JSON file, for the management UI")
	fs.StringVar(&opts.Dump, "dump", "",
		"write the raw elementary stream to this file instead of serving it")
	fs.IntVar(&opts.DumpSeconds, "dump-seconds", 5,
		"how many seconds to capture when using --dump")

	fs.Var((*stringList)(&opts.Tokens), "token",
		"require this token to watch; repeatable, and label=secret names it in the viewer list")
	fs.StringVar(&opts.TokenFile, "token-file", envOr("MICAM_TOKEN_FILE", ""),
		"file with one token per line, for secrets that should not be in argv")
	fs.Var((*stringList)(&opts.AllowCIDRs), "allow-cidr",
		"network that may watch without a credential, e.g. 192.168.0.0/24; repeatable")
	fs.Var((*stringList)(&opts.TrustIfaces), "trust-iface",
		"interface whose clients may watch without a credential; empty disables")
	fs.Var((*stringList)(&opts.TrustedProxies), "trusted-proxy",
		"peer allowed to declare the client in X-Forwarded-For; repeatable")
	fs.StringVar(&opts.AuthRealm, "auth-realm", envOr("MICAM_AUTH_REALM", "micam"),
		"realm offered to players in the Basic auth challenge")
	fs.IntVar(&opts.AuthFailLimit, "auth-fail-limit", envIntOr("MICAM_AUTH_FAIL_LIMIT", 20),
		"bad credentials one client may present per minute before it is throttled")
	fs.BoolVar(&opts.NoLocalBypass, "no-local-bypass", envBoolOr("MICAM_NO_LOCAL_BYPASS", false),
		"require a credential even from this machine (use when a proxy on this host terminates auth)")
	fs.BoolVar(&opts.PublicMetrics, "public-metrics", envBoolOr("MICAM_PUBLIC_METRICS", false),
		"leave /metrics and the index readable without a credential")
	fs.StringVar(&opts.ShareKey, "share-key", envOr("MICAM_SHARE_KEY", ""),
		"hex signing key for expiring links (default: share.key in the data directory)")
	fs.DurationVar(&opts.ShareTTL, "share-ttl", envDurationOr("MICAM_SHARE_TTL", 12*time.Hour),
		"how long a generated link stays valid")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	opts.ConnectTimeout = time.Duration(timeoutSeconds) * time.Second

	// Repeatable flags also accept a comma-separated value from the environment,
	// which is how the container and the fnOS package pass their settings. The
	// distinction between "unset" and "set to empty" matters: an empty
	// MICAM_TRUST_IFACE means "trust no interface".
	seen := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { seen[f.Name] = true })

	if !seen["token"] {
		if v, ok := os.LookupEnv("MICAM_TOKEN"); ok {
			opts.Tokens = splitList(v)
		}
	}
	if !seen["allow-cidr"] {
		if v, ok := os.LookupEnv("MICAM_ALLOW_CIDR"); ok {
			opts.AllowCIDRs = splitList(v)
		}
	}
	if !seen["trusted-proxy"] {
		if v, ok := os.LookupEnv("MICAM_TRUSTED_PROXY"); ok {
			opts.TrustedProxies = splitList(v)
		}
	}
	if !seen["trust-iface"] {
		if v, ok := os.LookupEnv("MICAM_TRUST_IFACE"); ok {
			opts.TrustIfaces = splitList(v)
		} else {
			// A tailnet is already authenticated by WireGuard and its ACLs, so
			// asking for a second password there is noise. The address is matched
			// against the local interface, not a CIDR, so a carrier NAT address
			// that happens to be in 100.64/10 gains nothing.
			opts.TrustIfaces = []string{"tailscale0"}
		}
	}

	if opts.TokenFile != "" {
		raw, err := os.ReadFile(opts.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("cannot read token file: %w", err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			opts.Tokens = append(opts.Tokens, line)
		}
	}

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
	if opts.AudioRate < 1000 || opts.AudioRate > 48000 {
		return nil, fmt.Errorf("audio-rate must be 1000..48000, got %d", opts.AudioRate)
	}
	if opts.Port < 1 || opts.Port > 65535 {
		return nil, fmt.Errorf("port must be 1..65535, got %d", opts.Port)
	}
	if opts.Reconnect < 0 {
		return nil, fmt.Errorf("reconnect must be >= 0, got %d", opts.Reconnect)
	}
	for _, token := range opts.Tokens {
		_, secret, hasLabel := strings.Cut(token, "=")
		if !hasLabel {
			secret = token
		}
		if secret = strings.TrimSpace(secret); secret == "" {
			return nil, fmt.Errorf("token %q has an empty secret", token)
		}
		if !auth.ValidToken(secret) {
			return nil, fmt.Errorf("token %q must be base64url (A-Z a-z 0-9 - _): "+
				"it has to travel through a URL unescaped", token)
		}
	}
	for _, cidr := range append(append([]string{}, opts.AllowCIDRs...), opts.TrustedProxies...) {
		if _, err := netip.ParsePrefix(strings.TrimSpace(cidr)); err != nil {
			return nil, fmt.Errorf("%q is not a network in CIDR form", cidr)
		}
	}
	if opts.AuthFailLimit < 0 || opts.AuthFailLimit > 10000 {
		return nil, fmt.Errorf("auth-fail-limit must be 0..10000, got %d", opts.AuthFailLimit)
	}
	if opts.ShareTTL < 5*time.Minute || opts.ShareTTL > 30*24*time.Hour {
		return nil, fmt.Errorf("share-ttl must be 5m..720h, got %s", opts.ShareTTL)
	}
	if strings.ContainsAny(opts.AuthRealm, "\"\r\n") {
		return nil, fmt.Errorf("auth-realm must not contain quotes or newlines")
	}
	if opts.IdleStop < 0 || opts.IdleStop > 1440 {
		return nil, fmt.Errorf("idle-stop must be 0..1440 minutes, got %d", opts.IdleStop)
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

// stringList is a flag that may be repeated, and that also accepts one
// comma-separated value from the environment.
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }

func (l *stringList) Set(value string) error {
	*l = append(*l, splitList(value)...)
	return nil
}

// splitList splits on commas and drops empty items.
func splitList(value string) []string {
	out := make([]string, 0, 2)
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func envDurationOr(key string, fallback time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return parsed
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
