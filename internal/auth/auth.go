// Package auth decides who may watch the stream.
//
// It is deliberately in-process instead of a reverse proxy: the HLS playlist is
// generated here, and the only way to authenticate an Apple player is to carry
// the credential into the segment URIs. No proxy can rewrite that body.
package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Methods are how a request proved it may watch. They are what the management UI
// shows next to a viewer.
const (
	// MethodAnonymous means no token is configured, so nothing is checked.
	MethodAnonymous = "anonymous"
	// MethodPublic is a path that is exempt from authentication.
	MethodPublic   = "public"
	MethodLoopback = "loopback"
	MethodIface    = "iface"
	MethodCIDR     = "cidr"
	MethodBasic    = "basic"
	MethodBearer   = "bearer"
	MethodToken    = "token"
	MethodCookie   = "cookie"
	MethodSigned   = "signed"
)

// Denials. They are separate values because the HTTP response and the metric
// label differ: a throttled client gets 429 and a retry hint, everything else
// gets 401.
var (
	ErrNoCredentials = errors.New("auth: no credentials")
	ErrInvalid       = errors.New("auth: invalid credentials")
	ErrExpired       = errors.New("auth: expired")
	ErrRateLimited   = errors.New("auth: too many failures")
)

// CookieName is the session cookie handed to HLS players.
//
// It exists for players whose segment requests carry neither the query string nor
// the Authorization header: Safari resolves the relative segment URIs itself, and
// a cookie is the one credential it reliably sends along.
const CookieName = "micam_auth"

// Token is one accepted credential. Label is only for humans ("客厅电视") and
// shows up in the viewer list so a credential can be revoked per device.
type Token struct {
	Label  string `json:"label"`
	Secret string `json:"secret"`
}

// Identity is the verdict for one request.
type Identity struct {
	Method string
	Label  string
	Remote string

	// query is what has to be appended to subresource URIs of this request, and
	// cookie is what a player should present next time. Both are empty when the
	// request was let in by where it came from rather than by a credential.
	query  string
	cookie string
}

// String is the one-line form stored in the status file.
func (i Identity) String() string {
	switch {
	case i.Method == MethodCIDR && i.Label != "":
		return "cidr:" + i.Label
	case i.Method == MethodIface && i.Label != "":
		return "iface:" + i.Label
	case i.Label != "" && i.Method != MethodAnonymous && i.Method != MethodLoopback &&
		i.Method != MethodPublic:
		return i.Method + ":" + i.Label
	}
	return i.Method
}

// Query is the query string, without the leading "?", that carries this
// request's credential. Empty when there is nothing to carry.
func (i Identity) Query() string { return i.query }

// CookieValue is what a player should send as the session cookie.
func (i Identity) CookieValue() string { return i.cookie }

// Options configures a Policy. The zero value means "no access control".
type Options struct {
	// Realm is the Basic auth realm shown by players.
	Realm string
	// Tokens are the accepted credentials. No tokens means auth is off.
	Tokens []Token
	// CIDRs are networks that need no credential, e.g. the LAN.
	CIDRs []string
	// Ifaces are interfaces whose accepted connections need no credential. A
	// connection is matched on its local address, which is spoof-proof: it is
	// the interface the kernel handed the socket to, not anything a client can
	// claim.
	Ifaces []string
	// TrustedProxies are peers whose X-Forwarded-For is believed. Empty means
	// the header is never used to decide who is calling, which is the safe
	// default.
	TrustedProxies []string
	// NoLoopback turns off the free pass for requests that arrive from this
	// machine.
	NoLoopback bool
	// FailLimit is how many bad credentials one client may present within
	// FailWindow before it is throttled. Zero means the default (20).
	FailLimit int
	// FailWindow is the throttle window. Zero means one minute.
	FailWindow time.Duration
	// ShareKey signs expiring links. Empty disables signed links.
	ShareKey []byte
	// ShareTTL is how long a signed link lives.
	ShareTTL time.Duration
	// Now is injectable for tests.
	Now func() time.Time
}

// Policy answers whether a request may watch, and how it proved it.
type Policy struct {
	realm          string
	tokens         []Token
	hashes         [][sha256.Size]byte
	cidrs          []netip.Prefix
	ifaces         []string
	trustedProxies []netip.Prefix
	noLoopback     bool
	limiter        *limiter
	shareKey       []byte
	shareTTL       time.Duration
	now            func() time.Time

	ifaceCache ifaceCache

	failMissing atomic.Uint64
	failInvalid atomic.Uint64
	failExpired atomic.Uint64
	failLimited atomic.Uint64
}

// New builds a policy. It validates the CIDR lists so a typo is a startup error
// rather than a silently unprotected stream.
func New(o Options) (*Policy, error) {
	p := &Policy{
		realm:      o.Realm,
		noLoopback: o.NoLoopback,
		shareKey:   o.ShareKey,
		shareTTL:   o.ShareTTL,
		now:        o.Now,
	}
	if p.realm == "" {
		p.realm = "micam"
	}
	if p.now == nil {
		p.now = time.Now
	}
	if p.shareTTL <= 0 {
		p.shareTTL = 12 * time.Hour
	}

	for _, t := range o.Tokens {
		secret := strings.TrimSpace(t.Secret)
		if secret == "" {
			return nil, errors.New("auth: a token has an empty secret")
		}
		// base64url only. The alphabet is what lets one string work everywhere:
		// query string, URL userinfo, shell, config file, none of which need
		// escaping. Anything else would break silently in the HLS playlist.
		if !ValidToken(secret) {
			return nil, errors.New("auth: a token must be base64url (A-Z a-z 0-9 - _); " +
				"generate one instead of typing it")
		}
		p.tokens = append(p.tokens, Token{Label: t.Label, Secret: secret})
		p.hashes = append(p.hashes, sha256.Sum256([]byte(secret)))
	}

	for _, c := range o.CIDRs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(c)
		if err != nil {
			return nil, errors.New("auth: bad CIDR " + strconv.Quote(c) + ": " + err.Error())
		}
		p.cidrs = append(p.cidrs, prefix.Masked())
	}

	for _, i := range o.Ifaces {
		if i = strings.TrimSpace(i); i != "" {
			p.ifaces = append(p.ifaces, i)
		}
	}

	for _, c := range o.TrustedProxies {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(c)
		if err != nil {
			return nil, errors.New("auth: bad trusted proxy " + strconv.Quote(c) + ": " + err.Error())
		}
		p.trustedProxies = append(p.trustedProxies, prefix.Masked())
	}

	limit, window := o.FailLimit, o.FailWindow
	if limit <= 0 {
		limit = 20
	}
	if window <= 0 {
		window = time.Minute
	}
	p.limiter = newLimiter(limit, window, p.now)

	return p, nil
}

// Disabled is a policy that lets everything through.
func Disabled() *Policy {
	p, _ := New(Options{})
	return p
}

// Enabled reports whether anything is being checked at all.
func (p *Policy) Enabled() bool { return len(p.tokens) > 0 }

// Tokens returns the accepted credentials, for the management UI.
func (p *Policy) Tokens() []Token { return p.tokens }

// Realm is the Basic auth realm.
func (p *Policy) Realm() string { return p.realm }

// ShareTTL is how long a generated link lives.
func (p *Policy) ShareTTL() time.Duration { return p.shareTTL }

// FailureCounts is what /metrics exposes. Every denial is counted once.
func (p *Policy) FailureCounts() map[string]uint64 {
	return map[string]uint64{
		"missing":      p.failMissing.Load(),
		"invalid":      p.failInvalid.Load(),
		"expired":      p.failExpired.Load(),
		"rate_limited": p.failLimited.Load(),
	}
}

// Authorize decides one request. The Identity is returned even on failure so the
// caller can log who asked.
func (p *Policy) Authorize(r *http.Request) (Identity, error) {
	peer, _ := splitAddr(r.RemoteAddr)
	client := peer
	proxied := false

	// Any forwarding header means something in front relayed this request. That
	// alone is enough to withhold the loopback free pass: a reverse proxy on this
	// machine would otherwise look exactly like a local player.
	if r.Header.Get("X-Forwarded-For") != "" {
		proxied = true
		if ip, ok := forwardedClient(r.Header); ok && p.trustedPeer(peer) {
			client = ip
		}
	}

	id := Identity{Remote: client.String()}
	if !p.Enabled() {
		id.Method = MethodAnonymous
		return id, nil
	}

	// Where the request came from can be enough on its own. It is checked before
	// any credential because the clients that need it most - televisions, NVRs,
	// DLNA players - cannot send one.
	if !proxied {
		if peer.IsLoopback() && !p.noLoopback {
			id.Method = MethodLoopback
			return id, nil
		}
		if name := p.interfaceOf(localAddr(r)); name != "" {
			id.Method = MethodIface
			id.Label = name
			return id, nil
		}
	}
	if prefix, ok := p.matchCIDR(client); ok {
		id.Method = MethodCIDR
		id.Label = prefix.String()
		return id, nil
	}

	if p.limiter.blocked(client.String()) {
		p.failLimited.Add(1)
		return id, ErrRateLimited
	}

	// Explicit credentials beat ambient ones: a player with a stale cookie and a
	// fresh URL token must not be turned away because of the cookie.
	if token, ok := bearerToken(r); ok {
		if secret, label, ok := p.match(sha256.Sum256([]byte(token))); ok {
			id.Method = MethodBearer
			id.Label = label
			id.query = "token=" + url.QueryEscape(secret)
			id.cookie = secret
			return id, nil
		}
		return p.deny(client, id, ErrInvalid)
	}

	if _, password, ok := basicCredentials(r); ok {
		// The user name is ignored on purpose: players disagree about what it
		// should be, and the secret is what identifies the caller.
		if secret, label, ok := p.match(sha256.Sum256([]byte(password))); ok {
			id.Method = MethodBasic
			id.Label = label
			id.query = "token=" + url.QueryEscape(secret)
			id.cookie = secret
			return id, nil
		}
		return p.deny(client, id, ErrInvalid)
	}

	q := r.URL.Query()
	if v := q.Get("token"); v != "" {
		if secret, label, ok := p.match(sha256.Sum256([]byte(v))); ok {
			id.Method = MethodToken
			id.Label = label
			id.query = "token=" + url.QueryEscape(secret)
			id.cookie = secret
			return id, nil
		}
		return p.deny(client, id, ErrInvalid)
	}

	if sig := q.Get("sig"); sig != "" {
		err := p.verifyShare(q.Get("exp"), sig)
		switch {
		case err == nil:
			id.Method = MethodSigned
			id.query = signedQuery(q.Get("exp"), sig)
			id.cookie = q.Get("exp") + "." + sig
			return id, nil
		case errors.Is(err, ErrExpired):
			return p.deny(client, id, ErrExpired)
		}
		return p.deny(client, id, ErrInvalid)
	}

	if c, err := r.Cookie(CookieName); err == nil && c.Value != "" {
		if secret, label, ok := p.match(sha256.Sum256([]byte(c.Value))); ok {
			id.Method = MethodCookie
			id.Label = label
			id.query = "token=" + url.QueryEscape(secret)
			id.cookie = secret
			return id, nil
		}
		if exp, sig, ok := strings.Cut(c.Value, "."); ok && exp != "" && sig != "" {
			err := p.verifyShare(exp, sig)
			switch {
			case err == nil:
				id.Method = MethodSigned
				id.query = signedQuery(exp, sig)
				id.cookie = c.Value
				return id, nil
			case errors.Is(err, ErrExpired):
				return p.deny(client, id, ErrExpired)
			}
		}
		return p.deny(client, id, ErrInvalid)
	}

	p.failMissing.Add(1)
	return id, ErrNoCredentials
}

// deny records a failure and applies the throttle.
func (p *Policy) deny(client netip.Addr, id Identity, err error) (Identity, error) {
	p.limiter.fail(client.String())
	if errors.Is(err, ErrExpired) {
		p.failExpired.Add(1)
	} else {
		p.failInvalid.Add(1)
	}
	return id, err
}

// match reports the secret and label behind a digest, or nothing.
//
// Every token is compared and the comparison is over digests, so neither the
// position of the match nor the length of the input leaks through timing.
func (p *Policy) match(sum [sha256.Size]byte) (secret, label string, ok bool) {
	found := -1
	for i := range p.hashes {
		if subtle.ConstantTimeCompare(sum[:], p.hashes[i][:]) == 1 && found < 0 {
			found = i
		}
	}
	if found < 0 {
		return "", "", false
	}
	t := p.tokens[found]
	return t.Secret, t.Label, true
}

func (p *Policy) trustedPeer(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	for _, prefix := range p.trustedProxies {
		if prefix.Contains(addr.Unmap()) {
			return true
		}
	}
	return false
}

func (p *Policy) matchCIDR(addr netip.Addr) (netip.Prefix, bool) {
	if !addr.IsValid() {
		return netip.Prefix{}, false
	}
	addr = addr.Unmap()
	for _, prefix := range p.cidrs {
		if prefix.Contains(addr) {
			return prefix, true
		}
	}
	return netip.Prefix{}, false
}

// Handler wraps a handler with the check.
//
// skip is consulted first and exists for /healthz (and /metrics when the operator
// asks for it): those carry no picture, and a health check that needs a password
// is a health check that is always down.
func (p *Policy) Handler(next http.Handler, skip func(*http.Request) bool) http.Handler {
	if skip == nil {
		skip = func(*http.Request) bool { return false }
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if skip(r) {
			next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(),
				Identity{Method: MethodPublic, Remote: ClientIP(r)})))
			return
		}
		id, err := p.Authorize(r)
		if err != nil {
			p.reject(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), id)))
	})
}

func (p *Policy) reject(w http.ResponseWriter, err error) {
	h := w.Header()
	h.Set("WWW-Authenticate", `Basic realm="`+p.realm+`", charset="UTF-8"`)
	h.Set("Cache-Control", "no-store")

	if errors.Is(err, ErrRateLimited) {
		h.Set("Retry-After", strconv.Itoa(int(p.limiter.retryAfter().Seconds())))
		http.Error(w, "too many failed authentication attempts", http.StatusTooManyRequests)
		return
	}
	http.Error(w, "authentication required", http.StatusUnauthorized)
}

// --- request plumbing ---------------------------------------------------

type identityKey struct{}

// WithIdentity attaches a verdict to a request context.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// FromContext reads the verdict the middleware stored. A handler that calls it
// without the middleware gets the zero Identity, which is harmless: there is
// nothing to propagate.
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}

type connKey struct{}

// ConnContext stores the socket addresses on the request context.
//
// The local address is what makes "trust the tailscale interface" sound: only the
// kernel knows which interface accepted the connection, so a client cannot claim
// to have arrived over the VPN.
func ConnContext(ctx context.Context, c net.Conn) context.Context {
	return WithConn(ctx, c.LocalAddr(), c.RemoteAddr())
}

// WithConn is ConnContext with the addresses given explicitly, for tests.
func WithConn(ctx context.Context, local, remote net.Addr) context.Context {
	return context.WithValue(ctx, connKey{}, connInfo{local: local, remote: remote})
}

type connInfo struct {
	local  net.Addr
	remote net.Addr
}

func localAddr(r *http.Request) netip.Addr {
	info, ok := r.Context().Value(connKey{}).(connInfo)
	if !ok || info.local == nil {
		return netip.Addr{}
	}
	addr, _ := splitAddr(info.local.String())
	return addr
}

// ClientIP is who is asking, after any trusted proxy has been resolved.
func ClientIP(r *http.Request) string {
	peer, _ := splitAddr(r.RemoteAddr)
	return peer.String()
}

func splitAddr(hostport string) (netip.Addr, bool) {
	if hostport == "" {
		return netip.Addr{}, false
	}
	if ap, err := netip.ParseAddrPort(hostport); err == nil {
		return ap.Addr().Unmap(), true
	}
	if addr, err := netip.ParseAddr(strings.Trim(hostport, "[]")); err == nil {
		return addr.Unmap(), true
	}
	return netip.Addr{}, false
}

// forwardedClient picks the client out of X-Forwarded-For.
//
// Rightmost-untrusted: entries are appended by proxies, so the last one is what
// the nearest proxy saw. The leftmost is whatever the caller chose to write.
func forwardedClient(h http.Header) (netip.Addr, bool) {
	value := h.Get("X-Forwarded-For")
	if value == "" {
		return netip.Addr{}, false
	}
	parts := strings.Split(value, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		if addr, ok := splitAddr(strings.TrimSpace(parts[i])); ok {
			return addr, true
		}
	}
	return netip.Addr{}, false
}

func bearerToken(r *http.Request) (string, bool) {
	value := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(value) <= len(prefix) || !strings.EqualFold(value[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(value[len(prefix):])
	return token, token != ""
}

func basicCredentials(r *http.Request) (user, password string, ok bool) {
	value := r.Header.Get("Authorization")
	const prefix = "Basic "
	if len(value) <= len(prefix) || !strings.EqualFold(value[:len(prefix)], prefix) {
		return "", "", false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value[len(prefix):]))
	if err != nil {
		return "", "", false
	}
	user, password, found := strings.Cut(string(raw), ":")
	if !found {
		return "", "", false
	}
	return user, password, true
}
