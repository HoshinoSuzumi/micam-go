package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// shareScope is mixed into the signature.
//
// It is a constant, not the request path, on purpose: one link has to work for
// the playlist and for every segment under /cam/, and those are different paths.
// A path-bound signature would authenticate the playlist and then reject the
// segments it just told the player to fetch.
const shareScope = "media"

// GenerateToken returns a fresh credential.
//
// base64url on purpose: the alphabet has no '+', '/', '=' or ':', so the same
// string can go into a query string, a URL's userinfo, a shell command and a
// config file without anyone having to escape it.
func GenerateToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// ValidToken reports whether a secret survives every context it has to travel
// through unescaped: query string, URL userinfo, shell, config file.
//
// base64url covers all of them, and nothing else does without quoting rules that
// every player implements differently.
func ValidToken(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return s != ""
}

// Sign returns the signature for an expiry, as raw base64url.
func Sign(key []byte, exp int64) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(shareScope))
	mac.Write([]byte("|"))
	mac.Write([]byte(strconv.FormatInt(exp, 10)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:16])
}

// ShareLink returns the URL with a signature that expires at exp.
func ShareLink(key []byte, rawURL string, exp int64) string {
	sig := Sign(key, exp)
	sep := "?"
	if strings.Contains(rawURL, "?") {
		sep = "&"
	}
	return rawURL + sep + "exp=" + strconv.FormatInt(exp, 10) + "&sig=" + url.QueryEscape(sig)
}

func signedQuery(exp, sig string) string {
	return "exp=" + url.QueryEscape(exp) + "&sig=" + url.QueryEscape(sig)
}

// verifyShare checks a signature, reporting ErrExpired separately so the UI and
// the metric can tell "wrong secret" from "this link timed out".
func (p *Policy) verifyShare(expValue, sig string) error {
	if len(p.shareKey) == 0 {
		return ErrInvalid
	}
	exp, err := strconv.ParseInt(expValue, 10, 64)
	if err != nil {
		return ErrInvalid
	}
	want := Sign(p.shareKey, exp)
	if subtle.ConstantTimeCompare([]byte(want), []byte(sig)) != 1 {
		return ErrInvalid
	}
	if !p.now().Before(time.Unix(exp, 0)) {
		return ErrExpired
	}
	return nil
}

// ShareKeyPath is where the signing key lives.
//
// It sits next to the cached session in the data directory because the management
// API runs as a separate process: it has to be able to read the same key to mint
// a link.
func ShareKeyPath(dataDir string) string {
	return filepath.Join(dataDir, "share.key")
}

// LoadOrCreateShareKey reads the signing key, creating it on first use.
//
// Rotation is the revoke button: a new key invalidates every link that was handed
// out, which is the only revocation a stateless signature can offer.
func LoadOrCreateShareKey(path string) ([]byte, error) {
	key, err := os.ReadFile(path)
	if err == nil {
		decoded, decErr := hex.DecodeString(strings.TrimSpace(string(key)))
		if decErr != nil || len(decoded) != 32 {
			return nil, errors.New("auth: " + path + " is not a 32-byte hex key")
		}
		return decoded, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	encoded := hex.EncodeToString(raw)

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// O_EXCL: if a second process raced us, the loser reads the winner's key
	// instead of overwriting it and invalidating links already in circulation.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return LoadOrCreateShareKey(path)
		}
		return nil, err
	}
	if _, err := f.WriteString(encoded + "\n"); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return raw, nil
}
