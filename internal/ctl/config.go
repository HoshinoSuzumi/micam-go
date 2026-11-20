package ctl

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"strconv"
	"strings"

	"micam/internal/auth"
)

// Config is what a user configures. It is the same set of values the wizard
// collects, and it is stored in the same file, so the two entry points cannot
// drift.
type Config struct {
	User      string `json:"user"`
	UserID    string `json:"user_id"`
	Region    string `json:"region"`
	DID       string `json:"did"`
	Quality   int    `json:"quality"`
	PTSLead   int    `json:"pts_lead"`
	LAN       string `json:"lan"`
	Reconnect int    `json:"reconnect"`
	// IdleStop releases the camera after this many minutes with no viewers.
	// Zero keeps pulling around the clock, which is the historical behaviour.
	IdleStop int `json:"idle_stop"`

	// HLS turns on the playlist Safari and iOS need. HLSWindow is how many
	// segments it keeps: more is steadier, fewer is lower latency.
	HLS       bool `json:"hls"`
	HLSWindow int  `json:"hls_window"`

	// Audio asks the camera for sound and serves it as a WAV stream on its own
	// endpoint. AudioRate fills in the WAV header.
	Audio     bool `json:"audio"`
	AudioRate int  `json:"audio_rate"`

	// Access control for the stream itself. The tokens are stream credentials,
	// not the Mi account: they are revocable per device and worth far less than a
	// passToken, which is why the UI may show them.
	Tokens      []AuthToken `json:"tokens"`
	AllowCIDR   []string    `json:"allow_cidr"`
	TrustIfaces []string    `json:"trust_ifaces"`
	// NoLocalBypass requires a credential even from 127.0.0.1.
	//
	// It exists for one deployment: a TCP tunnel (frp type=tcp) on the same host.
	// The tunnel's connections arrive from loopback and carry no
	// X-Forwarded-For, so they are indistinguishable from a local player, and the
	// free pass would silently turn access control off for the whole internet.
	NoLocalBypass bool `json:"no_local_bypass"`

	// HasToken reports whether a passToken is stored. The token itself is never
	// sent to a browser.
	HasToken bool `json:"has_token"`

	// PassToken is input only: set it to replace the stored token, leave it empty
	// to keep the current one.
	PassToken string `json:"pass_token,omitempty"`
}

// AuthToken is one stream credential. Label names the device so it can be
// deleted without rotating everything else.
type AuthToken struct {
	Label  string `json:"label"`
	Secret string `json:"secret"`
}

// Defaults returns the values used when nothing is configured yet.
func Defaults() Config {
	return Config{
		Region:    "cn",
		Quality:   3,
		PTSLead:   100,
		LAN:       "prefer",
		Reconnect: 5,
		HLS:       true,
		HLSWindow: 6,
		Audio:     false,
		AudioRate: 8000,

		// The tailnet is already authenticated by WireGuard and its ACLs, so a
		// second password there is noise. This is matched against the local
		// interface, so a carrier NAT address in the same range gains nothing.
		TrustIfaces: []string{"tailscale0"},
	}
}

var regions = map[string]bool{"cn": true, "de": true, "sg": true, "us": true, "ru": true, "i2": true}

// LoadConfig reads the env file. A missing file is not an error: it just means
// nothing has been configured, which is the state right after installation.
func LoadConfig(path string) (Config, error) {
	cfg := Defaults()

	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		cfg.set(strings.TrimSpace(key), unquote(strings.TrimSpace(value)))
	}
	if err := scanner.Err(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c *Config) set(key, value string) {
	switch key {
	case "MICAM_USER":
		c.User = value
	case "MICAM_USER_ID":
		c.UserID = value
	case "MICAM_REGION":
		c.Region = value
	case "MICAM_DID":
		c.DID = value
	case "MICAM_QUALITY":
		c.Quality = atoiOr(value, c.Quality)
	case "MICAM_PTS_LEAD":
		c.PTSLead = atoiOr(value, c.PTSLead)
	case "MICAM_LAN":
		c.LAN = value
	case "MICAM_RECONNECT":
		c.Reconnect = atoiOr(value, c.Reconnect)
	case "MICAM_IDLE_STOP":
		c.IdleStop = atoiOr(value, c.IdleStop)
	case "MICAM_HLS":
		if parsed, err := strconv.ParseBool(value); err == nil {
			c.HLS = parsed
		}
	case "MICAM_HLS_WINDOW":
		c.HLSWindow = atoiOr(value, c.HLSWindow)
	case "MICAM_AUDIO":
		if parsed, err := strconv.ParseBool(value); err == nil {
			c.Audio = parsed
		}
	case "MICAM_AUDIO_RATE":
		c.AudioRate = atoiOr(value, c.AudioRate)
	case "MICAM_TOKEN":
		c.Tokens = parseTokens(value)
	case "MICAM_ALLOW_CIDR":
		c.AllowCIDR = splitCSV(value)
	case "MICAM_TRUST_IFACE":
		c.TrustIfaces = splitCSV(value)
	case "MICAM_NO_LOCAL_BYPASS":
		if parsed, err := strconv.ParseBool(value); err == nil {
			c.NoLocalBypass = parsed
		}
	}
}

// parseTokens reads "客厅=secret,手机=other" back into entries.
//
// The label is optional: a bare value is still a usable credential, which keeps a
// hand-written MICAM_TOKEN working.
func parseTokens(value string) []AuthToken {
	var out []AuthToken
	for _, item := range splitCSV(value) {
		label, secret, hasLabel := strings.Cut(item, "=")
		if !hasLabel {
			label, secret = "", item
		}
		label, secret = strings.TrimSpace(label), strings.TrimSpace(secret)
		if secret == "" {
			continue
		}
		if label == "" {
			label = fmt.Sprintf("token-%d", len(out)+1)
		}
		out = append(out, AuthToken{Label: label, Secret: secret})
	}
	return out
}

// encodeTokens is the inverse of parseTokens.
func encodeTokens(tokens []AuthToken) string {
	parts := make([]string, 0, len(tokens))
	for _, t := range tokens {
		parts = append(parts, t.Label+"="+t.Secret)
	}
	return strings.Join(parts, ",")
}

// splitCSV splits a comma-separated list and drops empty items.
func splitCSV(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func atoiOr(value string, fallback int) int {
	n, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return n
}

// unquote accepts the three shapes that can appear in the file: bare, 'single' and
// "double" quoted. The wizard used to write bare values, so both must parse.
func unquote(value string) string {
	if len(value) >= 2 {
		switch {
		case value[0] == '\'' && value[len(value)-1] == '\'':
			return strings.ReplaceAll(value[1:len(value)-1], `'\''`, `'`)
		case value[0] == '"' && value[len(value)-1] == '"':
			if s, err := strconv.Unquote(value); err == nil {
				return s
			}
		}
	}
	return value
}

// quote renders a value so cmd/main can source the file with `set -a; .`.
func quote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// Encode renders the file. Values are quoted because a passToken and a user name
// are arbitrary text as far as this program is concerned.
func (c Config) Encode() []byte {
	var b strings.Builder
	b.WriteString("# 由 micam 的应用界面写入；手改也可以，改完重启服务生效。\n")
	fmt.Fprintf(&b, "MICAM_USER=%s\n", quote(c.User))
	fmt.Fprintf(&b, "MICAM_USER_ID=%s\n", quote(c.UserID))
	fmt.Fprintf(&b, "MICAM_REGION=%s\n", quote(c.Region))
	fmt.Fprintf(&b, "MICAM_DID=%s\n", quote(c.DID))
	fmt.Fprintf(&b, "MICAM_QUALITY=%s\n", quote(strconv.Itoa(c.Quality)))
	fmt.Fprintf(&b, "MICAM_PTS_LEAD=%s\n", quote(strconv.Itoa(c.PTSLead)))
	fmt.Fprintf(&b, "MICAM_LAN=%s\n", quote(c.LAN))
	fmt.Fprintf(&b, "MICAM_RECONNECT=%s\n", quote(strconv.Itoa(c.Reconnect)))
	fmt.Fprintf(&b, "MICAM_IDLE_STOP=%s\n", quote(strconv.Itoa(c.IdleStop)))
	fmt.Fprintf(&b, "MICAM_HLS=%s\n", quote(strconv.FormatBool(c.HLS)))
	fmt.Fprintf(&b, "MICAM_HLS_WINDOW=%s\n", quote(strconv.Itoa(c.HLSWindow)))
	fmt.Fprintf(&b, "MICAM_AUDIO=%s\n", quote(strconv.FormatBool(c.Audio)))
	fmt.Fprintf(&b, "MICAM_AUDIO_RATE=%s\n", quote(strconv.Itoa(c.AudioRate)))
	// 访问控制。token 用 label=secret 的逗号分隔形式，二者都不允许包含逗号或等号。
	fmt.Fprintf(&b, "MICAM_TOKEN=%s\n", quote(encodeTokens(c.Tokens)))
	fmt.Fprintf(&b, "MICAM_ALLOW_CIDR=%s\n", quote(strings.Join(c.AllowCIDR, ",")))
	fmt.Fprintf(&b, "MICAM_TRUST_IFACE=%s\n", quote(strings.Join(c.TrustIfaces, ",")))
	fmt.Fprintf(&b, "MICAM_NO_LOCAL_BYPASS=%s\n", quote(strconv.FormatBool(c.NoLocalBypass)))
	return []byte(b.String())
}

// Validate rejects what cmd/main could not use, before anything is written.
func (c *Config) Validate() error {
	c.Region = strings.ToLower(strings.TrimSpace(c.Region))
	c.LAN = strings.ToLower(strings.TrimSpace(c.LAN))
	c.User = strings.TrimSpace(c.User)
	c.UserID = strings.TrimSpace(c.UserID)
	c.DID = strings.TrimSpace(c.DID)

	if c.Region == "" {
		c.Region = "cn"
	}
	if !regions[c.Region] {
		return fmt.Errorf("unknown region %q (cn, de, sg, us, ru, i2)", c.Region)
	}
	if c.LAN == "" {
		c.LAN = "prefer"
	}
	switch c.LAN {
	case "prefer", "only", "off":
	default:
		return fmt.Errorf("lan must be prefer, only or off, got %q", c.LAN)
	}
	if c.Quality < 0 || c.Quality > 5 {
		return fmt.Errorf("quality must be 0..5, got %d", c.Quality)
	}
	if c.PTSLead < 0 || c.PTSLead > 2000 {
		return fmt.Errorf("pts_lead must be 0..2000, got %d", c.PTSLead)
	}
	if c.Reconnect < 0 || c.Reconnect > 1000 {
		return fmt.Errorf("reconnect must be 0..1000, got %d", c.Reconnect)
	}
	if c.IdleStop < 0 || c.IdleStop > 1440 {
		return fmt.Errorf("idle_stop must be 0..1440 minutes, got %d", c.IdleStop)
	}
	if c.HLSWindow < 0 || c.HLSWindow > 60 {
		return fmt.Errorf("hls_window must be 0..60, got %d", c.HLSWindow)
	}
	if c.AudioRate != 0 && (c.AudioRate < 1000 || c.AudioRate > 48000) {
		return fmt.Errorf("audio_rate must be 1000..48000, got %d", c.AudioRate)
	}
	for i := range c.Tokens {
		c.Tokens[i].Label = strings.TrimSpace(c.Tokens[i].Label)
		c.Tokens[i].Secret = strings.TrimSpace(c.Tokens[i].Secret)
		t := c.Tokens[i]
		if t.Label == "" {
			c.Tokens[i].Label = fmt.Sprintf("token-%d", i+1)
		}
		if len(t.Secret) < 16 {
			return fmt.Errorf("token %q is shorter than 16 characters; generate one instead", t.Label)
		}
		if !auth.ValidToken(t.Secret) {
			return fmt.Errorf("token %q 只能包含 A-Z a-z 0-9 - _（base64url），否则放进 URL 时会被转义坏掉；用「生成新 token」", t.Label)
		}
		if strings.ContainsAny(c.Tokens[i].Label, ",") || hasControl(c.Tokens[i].Label) {
			return fmt.Errorf("token label %q contains a comma or a control character", c.Tokens[i].Label)
		}
	}
	for _, cidr := range c.AllowCIDR {
		if _, err := netip.ParsePrefix(strings.TrimSpace(cidr)); err != nil {
			return fmt.Errorf("allow_cidr %q is not a network in CIDR form", cidr)
		}
	}

	if c.DID != "" && !digits(c.DID) {
		return errors.New("did must be digits only")
	}
	if c.UserID != "" && !digits(c.UserID) {
		return errors.New("user_id must be the numeric Mi account id")
	}
	if c.User == "" && c.UserID == "" {
		return errors.New("a Mi account (user or user_id) is required")
	}
	if hasControl(c.User) {
		return errors.New("user contains a control character")
	}
	if c.PassToken != "" {
		if strings.TrimSpace(c.PassToken) != c.PassToken {
			return errors.New("passToken has leading or trailing whitespace")
		}
		if hasControl(c.PassToken) || strings.ContainsAny(c.PassToken, " \t") {
			return errors.New("passToken must be a single token without spaces")
		}
	}
	return nil
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// Configured reports whether the service has enough to start: a camera and some
// way to authenticate.
func (p Paths) Configured() bool {
	cfg, err := LoadConfig(p.EnvFile())
	if err != nil {
		return false
	}
	if cfg.DID == "" {
		return false
	}
	if p.TokenStored() {
		return true
	}
	_, err = os.Stat(p.CredentialsFile())
	return err == nil
}

// TokenStored reports whether a non-empty passToken is on disk.
func (p Paths) TokenStored() bool {
	info, err := os.Stat(p.TokenFile())
	return err == nil && info.Size() > 0
}

// WriteConfig stores cfg and, when cfg.PassToken is set, replaces the token.
func (p Paths) WriteConfig(cfg Config) error {
	if err := os.MkdirAll(p.ETC, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p.EnvFile(), cfg.Encode(), 0o600); err != nil {
		return err
	}
	if cfg.PassToken != "" {
		if err := os.WriteFile(p.TokenFile(), []byte(cfg.PassToken), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// ReadToken returns the stored passToken, or "" when there is none.
func (p Paths) ReadToken() string {
	body, err := os.ReadFile(p.TokenFile())
	if err != nil {
		return ""
	}
	return string(body)
}
