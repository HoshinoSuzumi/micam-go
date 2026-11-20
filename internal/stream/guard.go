package stream

import (
	"net/http"

	"micam/internal/auth"
)

// Auth is the access-control policy in force, for the banner and the status file.
func (s *Server) Auth() *auth.Policy { return s.auth }

// skipAuth is what stays reachable without a credential.
//
// /healthz carries no picture, and a health check that needs a password is a
// health check that is always down. /metrics and the index are open only when the
// operator asks for it: they leak the frame rate, the state and the audience.
func (s *Server) skipAuth(r *http.Request) bool {
	switch r.URL.Path {
	case "/healthz":
		return true
	case "/metrics", "/":
		return s.cfg.PublicMetrics
	}
	return false
}

// setSessionCookie hands a player a cookie carrying its credential.
//
// It is belt and braces next to the query string: some players strip the query
// from subresource URLs, some drop the Authorization header on segment requests,
// and a cookie is what Safari, ExoPlayer and hls.js all send unprompted.
//
// Path is "/" because a cookie scoped to /cam would not be sent for /cam.m3u8:
// cookie paths match on segment boundaries, not on string prefixes.
func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	value := id.CookieValue()
	if value == "" {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   24 * 60 * 60,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		// A Secure cookie is discarded by browsers on plain HTTP, which is how a
		// LAN stream is normally reached, so it is only set where it can work.
		Secure: r.TLS != nil,
	})
}

// identityLabel is the "auth" column of the viewer list.
func identityLabel(r *http.Request) string {
	id, ok := auth.FromContext(r.Context())
	if !ok {
		return ""
	}
	return id.String()
}
