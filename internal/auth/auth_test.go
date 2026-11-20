package auth

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testSecret = "IXk3Qm9vV2hhdEV2ZXJZb25lQ2FuR3Vlc3M"

func newPolicy(t *testing.T, o Options) *Policy {
	t.Helper()
	if o.Tokens == nil {
		o.Tokens = []Token{{Label: "test", Secret: testSecret}}
	}
	p, err := New(o)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func request(t *testing.T, remote string, mutate func(*http.Request)) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/cam.ts", nil)
	r.RemoteAddr = remote
	if mutate != nil {
		mutate(r)
	}
	return r
}

func TestDisabledPolicyLetsEverythingThrough(t *testing.T) {
	p := Disabled()
	id, err := p.Authorize(request(t, "203.0.113.9:5000", nil))
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if id.Method != MethodAnonymous {
		t.Fatalf("method = %q, want %q", id.Method, MethodAnonymous)
	}
	if p.Enabled() {
		t.Fatal("Enabled() = true with no tokens")
	}
}

func TestCredentialMethods(t *testing.T) {
	p := newPolicy(t, Options{})

	cases := []struct {
		name   string
		mutate func(*http.Request)
		want   string
	}{
		{"bearer", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+testSecret)
		}, MethodBearer},
		{"bearer lowercase", func(r *http.Request) {
			r.Header.Set("Authorization", "bearer "+testSecret)
		}, MethodBearer},
		{"basic", func(r *http.Request) {
			r.SetBasicAuth("micam", testSecret)
		}, MethodBasic},
		{"basic with any user", func(r *http.Request) {
			r.SetBasicAuth("whatever", testSecret)
		}, MethodBasic},
		{"query token", func(r *http.Request) {
			q := r.URL.Query()
			q.Set("token", testSecret)
			r.URL.RawQuery = q.Encode()
		}, MethodToken},
		{"cookie", func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: CookieName, Value: testSecret})
		}, MethodCookie},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id, err := p.Authorize(request(t, "203.0.113.9:5000", c.mutate))
			if err != nil {
				t.Fatalf("Authorize: %v", err)
			}
			if id.Method != c.want {
				t.Fatalf("method = %q, want %q", id.Method, c.want)
			}
			// Every credential has to survive into the playlist, or HLS players
			// fetch segments with nothing and get 401 mid-stream.
			if !strings.Contains(id.Query(), "token=") {
				t.Fatalf("Query() = %q, want a token to carry", id.Query())
			}
			if id.CookieValue() == "" {
				t.Fatal("CookieValue() is empty")
			}
		})
	}
}

func TestBadCredentialsAreRejected(t *testing.T) {
	p := newPolicy(t, Options{FailLimit: 100})

	cases := map[string]func(*http.Request){
		"wrong bearer": func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer nope")
		},
		"wrong basic": func(r *http.Request) {
			r.SetBasicAuth("micam", "nope")
		},
		"wrong query": func(r *http.Request) {
			q := r.URL.Query()
			q.Set("token", "nope")
			r.URL.RawQuery = q.Encode()
		},
		"wrong cookie": func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: CookieName, Value: "nope"})
		},
		"no credential": func(*http.Request) {},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := p.Authorize(request(t, "203.0.113.9:5000", mutate)); err == nil {
				t.Fatal("Authorize accepted a bad request")
			}
		})
	}
}

func TestLoopbackAndProxyHeaders(t *testing.T) {
	p := newPolicy(t, Options{})

	id, err := p.Authorize(request(t, "127.0.0.1:4000", nil))
	if err != nil {
		t.Fatalf("loopback: %v", err)
	}
	if id.Method != MethodLoopback {
		t.Fatalf("method = %q, want loopback", id.Method)
	}

	// A forwarding header means something relayed this. A reverse proxy on this
	// machine would otherwise be indistinguishable from a local player, and a
	// disclosed proxy would silently disable authentication.
	_, err = p.Authorize(request(t, "127.0.0.1:4000", func(r *http.Request) {
		r.Header.Set("X-Forwarded-For", "203.0.113.9")
	}))
	if !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("err = %v, want ErrNoCredentials", err)
	}

	// And when the proxy is declared, the real client is what is judged.
	proxied := newPolicy(t, Options{
		TrustedProxies: []string{"127.0.0.1/32"},
		CIDRs:          []string{"10.0.0.0/8"},
	})
	id, err = proxied.Authorize(request(t, "127.0.0.1:4000", func(r *http.Request) {
		r.Header.Set("X-Forwarded-For", "203.0.113.9, 10.1.2.3")
	}))
	if err != nil {
		t.Fatalf("proxied: %v", err)
	}
	if id.Method != MethodCIDR || id.Label != "10.0.0.0/8" {
		t.Fatalf("identity = %+v, want the rightmost CIDR match", id)
	}

	// A stale entry further left must not be the one that is believed.
	_, err = proxied.Authorize(request(t, "127.0.0.1:4000", func(r *http.Request) {
		r.Header.Set("X-Forwarded-For", "10.1.2.3, 203.0.113.9")
	}))
	if !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("err = %v, want ErrNoCredentials for the leftmost entry", err)
	}
}

func TestNoLoopback(t *testing.T) {
	p := newPolicy(t, Options{NoLoopback: true})
	if _, err := p.Authorize(request(t, "127.0.0.1:4000", nil)); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("err = %v, want ErrNoCredentials", err)
	}
}

// loopbackIface is "lo" on Linux and "lo0" on macOS; the test should not assume
// either, and neither should the code.
func loopbackIface(t *testing.T) string {
	t.Helper()
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Fatalf("Interfaces: %v", err)
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			return iface.Name
		}
	}
	t.Skip("no loopback interface")
	return ""
}

func TestInterfaceTrust(t *testing.T) {
	name := loopbackIface(t)
	p := newPolicy(t, Options{NoLoopback: true, Ifaces: []string{name}})

	r := request(t, "127.0.0.1:4000", nil)
	r = r.WithContext(WithConn(r.Context(),
		&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8554},
		&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 4000}))

	id, err := p.Authorize(r)
	if err != nil {
		t.Fatalf("iface: %v", err)
	}
	if id.Method != MethodIface || id.Label != name {
		t.Fatalf("identity = %+v, want iface:%s", id, name)
	}

	// The local address of a LAN connection is not on the VPN interface, so the
	// same request is not trusted.
	r = request(t, "192.168.0.50:4000", nil)
	r = r.WithContext(WithConn(r.Context(),
		&net.TCPAddr{IP: net.ParseIP("192.168.0.111"), Port: 8554},
		&net.TCPAddr{IP: net.ParseIP("192.168.0.50"), Port: 4000}))
	if _, err := p.Authorize(r); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("err = %v, want ErrNoCredentials", err)
	}
}

func TestThrottle(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	p := newPolicy(t, Options{
		FailLimit:  2,
		FailWindow: time.Minute,
		Now:        func() time.Time { return now },
	})

	bad := func(r *http.Request) { r.SetBasicAuth("micam", "wrong") }
	for i := 0; i < 2; i++ {
		if _, err := p.Authorize(request(t, "203.0.113.9:5000", bad)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("attempt %d: err = %v", i, err)
		}
	}
	if _, err := p.Authorize(request(t, "203.0.113.9:5000", bad)); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}

	// The correct credential is refused too while the window is open.
	if _, err := p.Authorize(request(t, "203.0.113.9:5000", func(r *http.Request) {
		r.SetBasicAuth("micam", testSecret)
	})); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited even for the good token", err)
	}

	// Another client is unaffected.
	if _, err := p.Authorize(request(t, "203.0.113.10:5000", nil)); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("err = %v, want ErrNoCredentials", err)
	}

	// And the window does expire.
	now = now.Add(2 * time.Minute)
	if _, err := p.Authorize(request(t, "203.0.113.9:5000", nil)); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("err after the window = %v", err)
	}
}

func TestMiddleware(t *testing.T) {
	p := newPolicy(t, Options{Tokens: []Token{{Label: "客厅", Secret: testSecret}}})

	var seen Identity
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	h := p.Handler(next, func(r *http.Request) bool { return r.URL.Path == "/healthz" })

	t.Run("unauthorized", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodHead, "/cam.ts", nil))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("code = %d, want 401", w.Code)
		}
		if got := w.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "Basic realm=") {
			t.Fatalf("WWW-Authenticate = %q", got)
		}
	})

	t.Run("authorized", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/cam.ts", nil)
		r.RemoteAddr = "203.0.113.9:5000"
		r.SetBasicAuth("micam", testSecret)
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200", w.Code)
		}
		if seen.Method != MethodBasic || seen.Label != "客厅" {
			t.Fatalf("identity = %+v", seen)
		}
	})

	t.Run("healthz is exempt", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200", w.Code)
		}
	})
}

func TestShareLinks(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	now := time.Unix(1_700_000_000, 0)
	p := newPolicy(t, Options{
		ShareKey: key,
		ShareTTL: time.Hour,
		Now:      func() time.Time { return now },
	})

	link := ShareLink(key, "http://nas:8554/cam.m3u8", now.Add(time.Hour).Unix())
	q := strings.TrimPrefix(link, "http://nas:8554/cam.m3u8?")
	if !strings.Contains(q, "exp=") || !strings.Contains(q, "sig=") {
		t.Fatalf("link = %q, want exp and sig", link)
	}

	// The playlist and its segments share one signature: the scope is constant so
	// a link minted for the playlist also opens seg7.m4s.
	params := link[len("http://nas:8554/cam.m3u8?"):]
	for _, path := range []string{"/cam.m3u8", "/cam/seg7.m4s"} {
		r := request(t, "203.0.113.9:5000", func(r *http.Request) {
			r.URL.Path = path
			r.URL.RawQuery = params
		})
		id, err := p.Authorize(r)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if id.Method != MethodSigned {
			t.Fatalf("%s: method = %q", path, id.Method)
		}
		if got := id.Query(); !strings.Contains(got, "sig=") {
			t.Fatalf("%s: Query() = %q", path, got)
		}
	}

	// Expired.
	now = now.Add(2 * time.Hour)
	r := request(t, "203.0.113.9:5000", func(r *http.Request) { r.URL.RawQuery = params })
	if _, err := p.Authorize(r); !errors.Is(err, ErrExpired) {
		t.Fatalf("err = %v, want ErrExpired", err)
	}

	// Tampered expiry.
	now = time.Unix(1_700_000_000, 0)
	forged := ShareLink(key, "http://nas:8554/cam.ts", now.Add(100*time.Hour).Unix())
	forgedParams := forged[len("http://nas:8554/cam.ts?"):]
	if _, err := p.Authorize(request(t, "203.0.113.9:5000", func(r *http.Request) {
		r.URL.RawQuery = forgedParams
	})); err != nil {
		t.Fatalf("a freshly signed link should be valid: %v", err)
	}
	// Keep the signature but move the expiry forward.
	moved := strings.Replace(forgedParams, "exp="+strconv.FormatInt(now.Add(100*time.Hour).Unix(), 10),
		"exp="+strconv.FormatInt(now.Add(1000*time.Hour).Unix(), 10), 1)
	if _, err := p.Authorize(request(t, "203.0.113.9:5000", func(r *http.Request) {
		r.URL.RawQuery = moved
	})); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid for a moved expiry", err)
	}
}

func TestGenerateTokenIsURLSafe(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		tok, err := GenerateToken()
		if err != nil {
			t.Fatalf("GenerateToken: %v", err)
		}
		if strings.ContainsAny(tok, "+/=%&?#@: ") {
			t.Fatalf("token %q has a character that needs escaping", tok)
		}
		if len(tok) < 30 {
			t.Fatalf("token %q is short", tok)
		}
		if seen[tok] {
			t.Fatalf("token %q repeated", tok)
		}
		seen[tok] = true
	}
}

func TestParseErrorsAreStartupErrors(t *testing.T) {
	if _, err := New(Options{CIDRs: []string{"10.0.0.0/33"}}); err == nil {
		t.Fatal("a bad CIDR must be rejected")
	}
	if _, err := New(Options{Tokens: []Token{{Secret: "has space"}}}); err == nil {
		t.Fatal("a token with a space must be rejected")
	}
	if _, err := New(Options{Tokens: []Token{{Secret: "  "}}}); err == nil {
		t.Fatal("an empty token must be rejected")
	}
}
