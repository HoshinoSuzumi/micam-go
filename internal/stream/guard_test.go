package stream

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"micam/internal/auth"
)

// A realistic credential: base64url, exactly what the UI generates. The
// generator's alphabet is what keeps it usable in a URL without escaping.
const guardSecret = "IXk3uP9qWz1rT8sN0bV6cY2dE4fG5hJ7"

// startGuarded leaves the loopback free pass off, so the test client - which
// always connects from 127.0.0.1 - has to present something.
func startGuarded(t *testing.T, o auth.Options, publicMetrics bool) *Server {
	t.Helper()

	if o.Tokens == nil {
		o.Tokens = []auth.Token{{Label: "客厅", Secret: guardSecret}}
	}
	o.NoLoopback = true

	policy, err := auth.New(o)
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}

	srv := New(Config{
		Addr:          "127.0.0.1:0",
		Path:          "/cam.ts",
		HLS:           true,
		HLSWindow:     3,
		Audio:         true,
		Auth:          policy,
		PublicMetrics: publicMetrics,
	}, testLogger())
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

func TestEveryEndpointNeedsACredential(t *testing.T) {
	srv := startGuarded(t, auth.Options{}, false)

	client := &http.Client{Timeout: 5 * time.Second}

	// Every output, plus the two monitoring endpoints that are not /healthz: one
	// middleware covers them, and this is what proves it.
	for _, path := range []string{"/cam.ts", "/cam.m3u8", "/cam.wav", "/", "/metrics"} {
		resp, err := client.Get("http://" + srv.Addr() + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("GET %s = %d, want 401", path, resp.StatusCode)
		}
		if !strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Basic realm=") {
			t.Fatalf("GET %s has WWW-Authenticate %q", path, resp.Header.Get("WWW-Authenticate"))
		}
	}

	// A player probing with HEAD must not learn more than a GET would tell it.
	req, _ := http.NewRequest(http.MethodHead, "http://"+srv.Addr()+"/cam.ts", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("HEAD /cam.ts = %d, want 401", resp.StatusCode)
	}

	// A health check that needs a password is a health check that is always down.
	resp, err = client.Get("http://" + srv.Addr() + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200", resp.StatusCode)
	}
}

func TestPublicMetricsOptIn(t *testing.T) {
	srv := startGuarded(t, auth.Options{}, true)

	for _, path := range []string{"/metrics", "/"} {
		resp, err := http.Get("http://" + srv.Addr() + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, resp.StatusCode)
		}
	}

	// The picture is still protected: monitoring is not watching.
	resp, err := http.Get("http://" + srv.Addr() + "/cam.m3u8")
	if err != nil {
		t.Fatalf("GET playlist: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("playlist = %d, want 401", resp.StatusCode)
	}
}

// publishHLSWindow makes the segmenter emit an init segment and at least one
// media segment, so the playlist names real URIs.
func publishHLSWindow(t *testing.T, srv *Server) {
	t.Helper()
	srv.Publish(hlsFrame(t, 19, 0, true))
	for i := 1; i <= 10; i++ {
		srv.Publish(hlsFrame(t, 1, int64(i)*50, false))
	}
	srv.Publish(hlsFrame(t, 19, 550, true))
}

// TestPlaylistHandsTheCredentialToSegments is the compatibility contract that
// matters most: an Apple player cannot set a header, so the credential has to
// arrive with the playlist and travel to every URI it names.
func TestPlaylistHandsTheCredentialToSegments(t *testing.T) {
	srv := startGuarded(t, auth.Options{}, false)
	publishHLSWindow(t, srv)

	playlistURL := "http://" + srv.Addr() + "/cam.m3u8?token=" + guardSecret
	resp, err := http.Get(playlistURL)
	if err != nil {
		t.Fatalf("GET playlist: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("playlist = %d", resp.StatusCode)
	}

	uris := playlistURIs(string(body))
	if len(uris) < 2 {
		t.Fatalf("playlist names %d URIs:\n%s", len(uris), body)
	}
	for _, uri := range uris {
		if !strings.Contains(uri, "token="+guardSecret) {
			t.Fatalf("URI %q does not carry the credential:\n%s", uri, body)
		}
	}

	// The cookie carries the same credential, for players that drop the query.
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == auth.CookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("the playlist set no session cookie")
	}
	if cookie.Value != guardSecret {
		t.Fatalf("cookie value = %q", cookie.Value)
	}
	if !cookie.HttpOnly {
		t.Fatal("the cookie is not HttpOnly")
	}

	// Fetching a segment with only the cookie has to work: that is the path a
	// Safari player takes.
	segment := "/" + stripQuery(uris[1])
	req, _ := http.NewRequest(http.MethodGet, "http://"+srv.Addr()+segment, nil)
	req.AddCookie(cookie)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET segment with cookie: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("segment with cookie = %d, want 200", resp.StatusCode)
	}

	// And without any credential it is closed.
	resp, err = http.Get("http://" + srv.Addr() + segment)
	if err != nil {
		t.Fatalf("GET segment bare: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("segment without credential = %d, want 401", resp.StatusCode)
	}
}

// TestPlaylistCarriesTheCredentialForHeaderClients: ffmpeg and VLC can send Basic
// on the playlist but are not reliable about repeating it for every segment, and
// a player that gets 401 mid-stream stalls with no error worth reading.
func TestPlaylistCarriesTheCredentialForHeaderClients(t *testing.T) {
	srv := startGuarded(t, auth.Options{}, false)
	publishHLSWindow(t, srv)

	req, _ := http.NewRequest(http.MethodGet, "http://"+srv.Addr()+"/cam.m3u8", nil)
	req.SetBasicAuth("micam", guardSecret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET playlist: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("playlist = %d", resp.StatusCode)
	}
	for _, uri := range playlistURIs(string(body)) {
		if !strings.Contains(uri, "token="+guardSecret) {
			t.Fatalf("URI %q has no credential:\n%s", uri, body)
		}
	}
}

func TestViewerListRecordsHowTheyGotIn(t *testing.T) {
	srv := startGuarded(t, auth.Options{}, false)
	publishHLSWindow(t, srv)

	resp, err := http.Get("http://" + srv.Addr() + "/cam.m3u8?token=" + guardSecret)
	if err != nil {
		t.Fatalf("GET playlist: %v", err)
	}
	resp.Body.Close()

	viewers := srv.ViewersInfo()
	if len(viewers) != 1 {
		t.Fatalf("viewers = %d, want 1", len(viewers))
	}
	if viewers[0].Auth != "token:客厅" {
		t.Fatalf("auth = %q, want token:客厅", viewers[0].Auth)
	}
}

func TestMeasuresBadCredentials(t *testing.T) {
	srv := startGuarded(t, auth.Options{}, true)

	resp, err := http.Get("http://" + srv.Addr() + "/cam.ts?token=wrong")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()

	_, body := get(t, srv, "/metrics")
	if !strings.Contains(string(body), `micam_auth_failures_total{reason="invalid"} 1`) {
		t.Fatalf("metrics do not count the failure:\n%s", body)
	}
	if !strings.Contains(string(body), "micam_auth_enabled 1") {
		t.Fatalf("metrics do not report that auth is on:\n%s", body)
	}
}

func stripQuery(uri string) string {
	if i := strings.IndexByte(uri, '?'); i >= 0 {
		return uri[:i]
	}
	return uri
}
