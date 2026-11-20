package ctl

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"micam/internal/auth"
	"micam/internal/status"
)

// shareRequest is the body of POST /api/share.
type shareRequest struct {
	TTLHours int `json:"ttl_hours"`
}

// ShareReply is what the UI renders as a link or a QR code.
type ShareReply struct {
	OK      bool               `json:"ok"`
	Expires time.Time          `json:"expires"`
	URLs    []status.StreamURL `json:"urls"`
}

// share mints links that expire.
//
// A guest should not be handed a credential that works forever, and a link that
// expires is also the only kind that is safe to paste into a chat: once it stops
// working, the thing that was pasted is worthless.
func (s *Server) share() reply {
	body, err := io.ReadAll(io.LimitReader(s.Stdin, 1<<16))
	if err != nil {
		return s.fail(http.StatusBadRequest, fmt.Errorf("cannot read the request body: %w", err))
	}
	var request shareRequest
	if err := json.Unmarshal(body, &request); err != nil {
		return s.fail(http.StatusBadRequest, fmt.Errorf("cannot parse the request body: %w", err))
	}

	cfg, err := LoadConfig(s.Paths.EnvFile())
	if err != nil {
		return s.fail(http.StatusInternalServerError, fmt.Errorf("cannot read the configuration: %w", err))
	}
	if len(cfg.Tokens) == 0 {
		return s.fail(http.StatusConflict, fmt.Errorf("access control is off, so a link would protect nothing"))
	}

	key, err := auth.LoadOrCreateShareKey(auth.ShareKeyPath(s.Paths.DataDir()))
	if err != nil {
		return s.fail(http.StatusInternalServerError, fmt.Errorf("cannot read the signing key: %w", err))
	}

	snap, err := status.Read(s.Paths.StatusFile())
	if err != nil {
		return s.fail(http.StatusServiceUnavailable, fmt.Errorf("the stream is not running: %w", err))
	}

	ttl := time.Duration(request.TTLHours) * time.Hour
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	if ttl > 30*24*time.Hour {
		ttl = 30 * 24 * time.Hour
	}
	exp := time.Now().Add(ttl)

	// One signature covers every endpoint because the scope is constant: the same
	// link works for the playlist, its segments and the WAV stream.
	all := make([]status.StreamURL, 0, len(snap.URLs)+len(snap.HLS)+len(snap.Audio))
	all = append(all, snap.URLs...)
	all = append(all, snap.HLS...)
	all = append(all, snap.Audio...)

	urls := make([]status.StreamURL, 0, len(all))
	for _, u := range all {
		if u.URL == "" {
			continue
		}
		urls = append(urls, status.StreamURL{
			URL:   auth.ShareLink(key, u.URL, exp.Unix()),
			Iface: u.Iface,
		})
	}
	if len(urls) == 0 {
		return s.fail(http.StatusServiceUnavailable,
			fmt.Errorf("no reachable address yet; start the stream and try again"))
	}

	return s.json(http.StatusOK, ShareReply{OK: true, Expires: exp, URLs: urls})
}

// newToken mints a credential.
//
// Done here rather than in the browser so the entropy comes from crypto/rand and
// the alphabet stays base64url, which is what makes the token safe to put in a
// URL, a shell command and a config file without escaping.
func (s *Server) newToken() reply {
	token, err := auth.GenerateToken()
	if err != nil {
		return s.fail(http.StatusInternalServerError, fmt.Errorf("cannot generate a token: %w", err))
	}
	return s.json(http.StatusOK, map[string]any{"ok": true, "token": token})
}
