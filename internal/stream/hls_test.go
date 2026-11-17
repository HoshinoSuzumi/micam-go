package stream

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"micam/internal/miss"
)

// The parameter sets of a real camera, so the init segment built here is the one
// a player would receive.
const (
	testVPS = "40010c01ffff016000000300900000030000030096aa0240"
	testSPS = "420101016000000300900000030000030096a00168200659f796aa493088040000030004000003005020"
	testPPS = "4401c072f02240"
)

func hlsFrame(t *testing.T, vclType int, tsMS int64, key bool) miss.Packet {
	t.Helper()

	var payload []byte
	for _, h := range []string{testVPS, testSPS, testPPS} {
		raw := mustDecodeHex(t, h)
		payload = append(payload, 0x00, 0x00, 0x00, 0x01)
		payload = append(payload, raw...)
	}
	payload = append(payload, 0x00, 0x00, 0x00, 0x01, byte(vclType<<1), 0x01, 0xAA, 0x02)

	p := miss.Packet{Codec: miss.CodecH265, TimestampMS: tsMS, Payload: payload}
	if key {
		p.Flags = 1
	}
	return p
}

func mustDecodeHex(t *testing.T, s string) []byte {
	t.Helper()
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		hi := hexVal(s[i*2])
		lo := hexVal(s[i*2+1])
		if hi < 0 || lo < 0 {
			t.Fatalf("bad hex fixture at %d", i*2)
		}
		out[i] = byte(hi<<4 | lo)
	}
	return out
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

func startHLS(t *testing.T) *Server {
	t.Helper()

	srv := New(Config{Addr: "127.0.0.1:0", Path: "/cam.ts", HLS: true, HLSWindow: 3}, testLogger())
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

// get fetches one of the server's endpoints and returns the body.
func get(t *testing.T, srv *Server, path string) (*http.Response, []byte) {
	t.Helper()

	resp, err := http.Get("http://" + srv.Addr() + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return resp, body
}

func TestHLSPlaylistAndSegments(t *testing.T) {
	srv := startHLS(t)

	// Two keyframes so one segment closes and a second opens.
	srv.Publish(hlsFrame(t, 19, 0, true))
	for i := 1; i <= 10; i++ {
		srv.Publish(hlsFrame(t, 1, int64(i)*50, false))
	}
	srv.Publish(hlsFrame(t, 19, 550, true))

	resp, body := get(t, srv, "/cam.m3u8")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("playlist returned %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/vnd.apple.mpegurl" {
		t.Fatalf("playlist content type is %q", ct)
	}
	if !strings.HasPrefix(string(body), "#EXTM3U") {
		t.Fatalf("playlist body:\n%s", body)
	}

	// Media names are content addresses, so they are read from the playlist
	// rather than assumed.
	uris := playlistURIs(string(body))
	if len(uris) < 2 {
		t.Fatalf("playlist names %d media URIs, want at least init and one segment:\n%s", len(uris), body)
	}

	resp, init := get(t, srv, "/"+uris[0])
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("init returned %d", resp.StatusCode)
	}
	if !strings.Contains(string(init), "hvcC") {
		t.Fatal("init segment has no hvcC record")
	}

	resp, seg := get(t, srv, "/"+uris[1])
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("segment returned %d", resp.StatusCode)
	}
	// CMAF segments start with styp and an index, then the fragment.
	if len(seg) < 80 || string(seg[4:8]) != "styp" || !bytes.Contains(seg[:200], []byte("moof")) {
		t.Fatalf("segment is not CMAF-shaped: %x", seg[:min(80, len(seg))])
	}
}

func TestHLSPlaylistURIsResolve(t *testing.T) {
	srv := startHLS(t)

	srv.Publish(hlsFrame(t, 19, 0, true))
	for i := 1; i <= 10; i++ {
		srv.Publish(hlsFrame(t, 1, int64(i)*50, false))
	}
	srv.Publish(hlsFrame(t, 19, 550, true))

	_, body := get(t, srv, "/cam.m3u8")

	base, err := url.Parse("http://" + srv.Addr() + "/cam.m3u8")
	if err != nil {
		t.Fatalf("parse playlist URL: %v", err)
	}

	uris := playlistURIs(string(body))
	if len(uris) < 2 {
		t.Fatalf("playlist names %d media URIs, want at least init and one segment:\n%s", len(uris), body)
	}

	// This is what a player does: resolve each URI against the playlist's own URL
	// and fetch it. A URI that only works when the playlist sits at the root would
	// pass a string comparison but fail here - which is exactly the bug that made
	// a real player ask for /init.mp4 instead of /cam/init.mp4.
	for _, raw := range uris {
		ref, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse URI %q: %v", raw, err)
		}
		resolved := base.ResolveReference(ref)

		resp, err := http.Get(resolved.String())
		if err != nil {
			t.Fatalf("GET %s: %v", resolved, err)
		}
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("playlist points at %s, which answers %d", resolved, resp.StatusCode)
		}
	}
}

// playlistURIs lists the media URIs a player would fetch: the EXT-X-MAP target
// and every segment line.
func playlistURIs(playlist string) []string {
	var uris []string
	for _, line := range strings.Split(playlist, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-MAP:") {
			if _, value, ok := strings.Cut(line, `URI="`); ok {
				if uri, _, ok := strings.Cut(value, `"`); ok {
					uris = append(uris, uri)
				}
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		uris = append(uris, line)
	}
	return uris
}

func TestHLSUnknownNameIs404(t *testing.T) {
	srv := startHLS(t)
	srv.Publish(hlsFrame(t, 19, 0, true))
	srv.Publish(hlsFrame(t, 1, 50, false))
	srv.Publish(hlsFrame(t, 19, 100, true))

	// A name that has slid out of the window, a malformed one, and one from an
	// earlier session all mean the same thing now that names are content
	// addresses: we do not have it.
	for _, name := range []string{"/cam/seg99.m4s", "/cam/seg-abc.m4s", "/cam/init.mp4", "/cam/nonsense"} {
		resp, _ := get(t, srv, name)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s returned %d, want 404", name, resp.StatusCode)
		}
	}
}

func TestHLSSegmentNameIsAContentAddress(t *testing.T) {
	srv := startHLS(t)

	srv.Publish(hlsFrame(t, 19, 0, true))
	srv.Publish(hlsFrame(t, 1, 50, false))
	srv.Publish(hlsFrame(t, 19, 100, true))

	_, body := get(t, srv, "/cam.m3u8")
	uris := playlistURIs(string(body))
	if len(uris) < 2 {
		t.Fatalf("playlist names %d URIs:\n%s", len(uris), body)
	}

	// Nothing may be named after a position: numbers restart at zero on every
	// reconnect, and a cached "seg3.m4s" would then be a different video.
	for _, uri := range uris {
		base := uri[strings.LastIndex(uri, "/")+1:]
		if !strings.HasPrefix(base, "seg") && !strings.HasPrefix(base, "init-") {
			t.Fatalf("URI %q is not content addressed", uri)
		}
		if regexp.MustCompile(`^seg\d+\.m4s$`).MatchString(base) {
			t.Fatalf("URI %q still looks positional", uri)
		}
	}
}

func TestHLSIsOffByDefault(t *testing.T) {
	srv := New(Config{Addr: "127.0.0.1:0", Path: "/cam.ts"}, testLogger())
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	resp, err := http.Get("http://" + srv.Addr() + "/cam.m3u8")
	if err != nil {
		t.Fatalf("GET playlist: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("playlist answered %d with HLS disabled, want 404", resp.StatusCode)
	}
}
