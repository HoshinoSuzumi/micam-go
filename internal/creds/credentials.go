// Package creds persists the Mi account session.
//
// The file holds a passToken, which is roughly as powerful as the account
// password, so it is created with owner-only permissions and kept outside the
// repository.
package creds

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultRegion is where most deployments of this fork live. Upstream defaults
// to "ru"; the region only selects which API host to talk to, and a mismatch
// shows up as a login failure rather than anything subtle.
const DefaultRegion = "cn"

const (
	dirMode  = 0o700
	fileMode = 0o600
)

// Credentials is the persisted session. Field names match the upstream JSON so
// an existing file keeps working.
type Credentials struct {
	User         string `json:"user,omitempty"`
	Region       string `json:"region,omitempty"`
	DeviceID     string `json:"deviceId,omitempty"`
	UserID       string `json:"userId,omitempty"`
	Ssecurity    string `json:"ssecurity,omitempty"`
	ServiceToken string `json:"serviceToken,omitempty"`
	PassToken    string `json:"passToken,omitempty"`
}

// HasSession reports whether the cached tokens can sign API calls without
// logging in again.
func (c *Credentials) HasSession() bool {
	return c.Ssecurity != "" && c.ServiceToken != "" && c.UserID != ""
}

// CanRenew reports whether a passToken is available to re-authenticate
// silently: no password, no two-factor.
func (c *Credentials) CanRenew() bool {
	return c.PassToken != ""
}

// Store reads and writes the credentials file.
type Store struct {
	Path string
}

// DefaultStore returns <home>/.micam/credentials.json.
func DefaultStore() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("creds: cannot locate home directory: %w", err)
	}
	return &Store{Path: filepath.Join(home, ".micam", "credentials.json")}, nil
}

// StoreAt keeps credentials.json inside dir. It exists because the home
// directory is not always writable: a locked-down profile, a service account,
// or a container with a read-only root all leave no place for the session to
// live.
func StoreAt(dir string) (*Store, error) {
	if dir == "" {
		return DefaultStore()
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("creds: bad data directory %q: %w", dir, err)
	}
	return &Store{Path: filepath.Join(abs, "credentials.json")}, nil
}

// Load returns the stored credentials, or an empty set when the file does not
// exist yet.
func (s *Store) Load() (*Credentials, error) {
	raw, err := os.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Credentials{Region: DefaultRegion}, nil
		}
		return nil, fmt.Errorf("creds: cannot read %s: %w", s.Path, err)
	}
	var c Credentials
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("creds: cannot parse %s: %w", s.Path, err)
	}
	if c.Region == "" {
		c.Region = DefaultRegion
	}
	return &c, nil
}

// Save writes the credentials atomically with owner-only permissions.
func (s *Store) Save(c *Credentials) error {
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("creds: cannot create %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("creds: cannot encode credentials: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, ".credentials-*.json")
	if err != nil {
		return fmt.Errorf("creds: cannot create a temporary file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("creds: cannot write %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("creds: cannot close %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, fileMode); err != nil {
		return fmt.Errorf("creds: cannot restrict %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, s.Path); err != nil {
		return fmt.Errorf("creds: cannot replace %s: %w", s.Path, err)
	}
	return nil
}

// Delete removes the stored session, if any.
func (s *Store) Delete() error {
	if err := os.Remove(s.Path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("creds: cannot delete %s: %w", s.Path, err)
	}
	return nil
}

// Redacted returns a log-safe description of the credentials.
func (c *Credentials) Redacted() string {
	var b strings.Builder
	b.WriteString("user=")
	b.WriteString(orDash(c.User))
	b.WriteString(" region=")
	b.WriteString(orDash(c.Region))
	b.WriteString(" session=")
	if c.HasSession() {
		b.WriteString("cached")
	} else {
		b.WriteString("none")
	}
	b.WriteString(" renew=")
	if c.CanRenew() {
		b.WriteString("passToken")
	} else {
		b.WriteString("password")
	}
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
