package stream

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"micam/internal/miss"
	"micam/internal/status"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// packet builds one HEVC access unit.
func packet(seq uint32, keyframe bool, timestampMS int64) miss.Packet {
	payload := make([]byte, 600)
	copy(payload, []byte{0x00, 0x00, 0x00, 0x01})

	flags := uint32(0)
	if keyframe {
		flags = 1
	}

	return miss.Packet{Codec: miss.CodecH265, Seq: seq, Flags: flags, TimestampMS: timestampMS, Payload: payload}
}

func startServer(t *testing.T) *Server {
	t.Helper()
	server := New(Config{
		Addr:      "127.0.0.1:0",
		Path:      "/cam.ts",
		PTSLeadMS: 300, // the production default; the PTS assertions below rely on it
	}, testLogger())
	if err := server.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { server.Close() })
	return server
}

func TestServesTransportStream(t *testing.T) {
	server := startServer(t)

	go func() {
		for i := 0; i < 40; i++ {
			// The leading frames are not keyframes and must be withheld: a viewer
			// that starts mid-GOP sees P-frames referencing parameter sets it never
			// received.
			server.Publish(packet(uint32(i), i >= 3, int64(1000+i*50)))
			time.Sleep(10 * time.Millisecond)
		}
	}()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + server.listener.Addr().String() + "/cam.ts")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "video/mp2t" {
		t.Fatalf("content type = %q, want video/mp2t", got)
	}

	head := make([]byte, 188*4)
	if _, err := io.ReadFull(resp.Body, head); err != nil {
		t.Fatalf("reading four packets: %v", err)
	}
	for off := 0; off < len(head); off += 188 {
		if head[off] != 0x47 {
			t.Fatalf("missing sync byte at offset %d", off)
		}
	}
	if pid := int(head[1]&0x1f)<<8 | int(head[2]); pid != 0x0000 {
		t.Fatalf("first packet PID = 0x%x, want PAT", pid)
	}
	if pid := int(head[188+1]&0x1f)<<8 | int(head[188+2]); pid != 0x1000 {
		t.Fatalf("second packet PID = 0x%x, want PMT", pid)
	}
}

func TestStartsWithAKeyframe(t *testing.T) {
	server := startServer(t)

	go func() {
		for i := 0; i < 30; i++ {
			server.Publish(packet(uint32(i), i >= 5, int64(1000+i*50)))
			time.Sleep(5 * time.Millisecond)
		}
	}()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + server.listener.Addr().String() + "/cam.ts")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	head := make([]byte, 188*3)
	if _, err := io.ReadFull(resp.Body, head); err != nil {
		t.Fatalf("reading three packets: %v", err)
	}

	// The third packet is the first video PES and must carry the offset PTS,
	// which is only true if the stream began at a keyframe.
	off := 188 * 2
	adaptationLength := int(head[off+4])
	pes := off + 5 + adaptationLength
	at := pes + 9
	pts := int64(head[at]&0x0E)<<29 |
		int64(head[at+1])<<22 |
		int64(head[at+2]&0xFE)<<14 |
		int64(head[at+3])<<7 |
		int64(head[at+4]&0xFE>>1)

	if pts != 27000 {
		t.Fatalf("first PTS = %d, want 27000; the viewer did not start at a keyframe", pts)
	}
}

func TestRejectsNonGet(t *testing.T) {
	server := startServer(t)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post("http://"+server.listener.Addr().String()+"/cam.ts", "text/plain", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
}

// TestViewerResyncsAtKeyframe pins the recovery rule: when a viewer falls
// behind, the backlog is discarded and the stream restarts at a keyframe. Sending
// a P-frame into a hole produces "Could not find ref with POC ..." on the
// decoder and corrupts everything until the next I-frame.
func TestViewerResyncsAtKeyframe(t *testing.T) {
	v := &viewer{frames: make(chan miss.Packet, 2), log: testLogger()}

	v.offer(packet(0, true, 0))
	v.offer(packet(1, false, 50))
	if got := len(v.frames); got != 2 {
		t.Fatalf("queue = %d, want 2", got)
	}

	// Overflow on a P-frame: nothing can be shown, so the backlog goes.
	v.offer(packet(2, false, 100))
	if got := len(v.frames); got != 0 {
		t.Fatalf("backlog = %d, want 0 after a resync", got)
	}
	if v.resyncs != 1 {
		t.Fatalf("resyncs = %d, want 1", v.resyncs)
	}

	// P-frames must not restart the stream mid-GOP.
	v.offer(packet(3, false, 150))
	if got := len(v.frames); got != 0 {
		t.Fatalf("a P-frame restarted the stream: queue = %d", got)
	}

	// A keyframe does.
	v.offer(packet(4, true, 200))
	if got := len(v.frames); got != 1 {
		t.Fatalf("keyframe was not accepted: queue = %d", got)
	}
	if !v.started {
		t.Fatal("the viewer should be streaming again")
	}
}

func TestViewerCountReturnsToZero(t *testing.T) {
	server := startServer(t)

	if got := server.Viewers(); got != 0 {
		t.Fatalf("viewers = %d before anyone connected", got)
	}

	go func() {
		for i := 0; i < 20; i++ {
			server.Publish(packet(uint32(i), true, int64(1000+i*50)))
			time.Sleep(5 * time.Millisecond)
		}
	}()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + server.listener.Addr().String() + "/cam.ts")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	buf := make([]byte, 188)
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	resp.Body.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if server.Viewers() == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("viewers = %d after the player disconnected", server.Viewers())
}

// The banner and the UI must report the address the kernel actually bound, not the
// configured one: "0.0.0.0" printed as "127.0.0.1" reads like "local only", and the
// reverse mistake ("127.0.0.1" reported as reachable) hides every remote failure.
func TestServerReportsWhatItBound(t *testing.T) {
	server := New(Config{Addr: "127.0.0.1:0", Path: "/cam.ts"}, testLogger())
	if err := server.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer server.Close()

	if got := server.Addr(); !strings.HasPrefix(got, "127.0.0.1:") {
		t.Fatalf("Addr() = %q, want the bound loopback address", got)
	}
	if !server.Loopback() {
		t.Fatal("a 127.0.0.1 listener must be reported as loopback")
	}
}

func TestServerWildcardIsReachable(t *testing.T) {
	server := New(Config{Addr: "0.0.0.0:0", Path: "/cam.ts"}, testLogger())
	if err := server.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer server.Close()

	if server.Loopback() {
		t.Fatalf("0.0.0.0 must not be reported as loopback (bound %q)", server.Addr())
	}
	if strings.HasPrefix(server.Addr(), "127.") {
		t.Fatalf("Addr() = %q, want the wildcard the kernel bound", server.Addr())
	}
}

// The banner must not advertise loopback, link-local or the unspecified address as
// something another machine can open - and it must prefer IPv4, which is what a
// Tailscale or LAN user will actually type.
func TestReachableURLsSkipUnusableAddresses(t *testing.T) {
	mustAddr := func(s string) *net.IPNet {
		ip, ipnet, err := net.ParseCIDR(s)
		if err != nil {
			t.Fatalf("bad fixture %q: %v", s, err)
		}
		ipnet.IP = ip
		return ipnet
	}

	candidates := []candidateAddr{
		{Iface: "lo", Addr: mustAddr("127.0.0.1/8")},
		{Iface: "lo", Addr: mustAddr("::1/128")},
		{Iface: "enp1s0", Addr: mustAddr("169.254.10.20/16")},
		{Iface: "enp1s0", Addr: mustAddr("fe80::1/64")},
		{Iface: "tailscale0", Addr: mustAddr("100.103.197.34/32")},
		{Iface: "enp1s0", Addr: mustAddr("192.168.0.5/24")},
		{Iface: "enp1s0", Addr: mustAddr("2001:db8::5/64")},
	}

	got := reachableURLs(candidates, "8554", "/cam.ts")
	if len(got) != 3 {
		t.Fatalf("got %d urls, want 3: %+v", len(got), got)
	}

	if got[0].URL != "http://100.103.197.34:8554/cam.ts" || got[0].Iface != "tailscale0" {
		t.Errorf("first = %+v, want the tailscale IPv4", got[0])
	}
	if got[1].URL != "http://192.168.0.5:8554/cam.ts" {
		t.Errorf("second = %+v, want the LAN IPv4", got[1])
	}
	if got[2].URL != "http://[2001:db8::5]:8554/cam.ts" {
		t.Errorf("third = %+v, want the IPv6 address bracketed", got[2])
	}

	for _, u := range got {
		for _, bad := range []string{"127.0.0.1", "::1", "169.254.", "fe80:", "[::]"} {
			if strings.Contains(u.URL, bad) {
				t.Errorf("%q should not be advertised", u.URL)
			}
		}
	}
}

func TestReachableURLsAreEmptyForALoopbackListener(t *testing.T) {
	server := New(Config{Addr: "127.0.0.1:0", Path: "/cam.ts"}, testLogger())
	if err := server.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer server.Close()

	if got := server.ReachableURLs(); len(got) != 0 {
		t.Fatalf("a loopback listener must advertise nothing, got %+v", got)
	}
}

// HEAD is what players and proxies use to probe before opening a stream. Answering
// 405 made a perfectly healthy server look broken during a debugging session.
func TestHeadIsAllowed(t *testing.T) {
	server := startServer(t)

	response, err := http.Head(server.URL())
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	if got := response.Header.Get("Content-Type"); got != "video/mp2t" {
		t.Fatalf("content-type = %q", got)
	}
	if response.ContentLength > 0 {
		t.Fatalf("HEAD must not claim a body, got %d bytes", response.ContentLength)
	}
}

func TestOtherMethodsAreRejected(t *testing.T) {
	server := startServer(t)

	request, err := http.NewRequest(http.MethodPost, server.URL(), nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", response.StatusCode)
	}
	if allow := response.Header.Get("Allow"); !strings.Contains(allow, "HEAD") {
		t.Fatalf("Allow = %q, want it to mention HEAD", allow)
	}
}

// On-demand streaming is built on these two: idle time tells the session when to
// let the camera go, and the signal tells a waiting session that somebody arrived.
func TestIdleTimeFollowsTheAudience(t *testing.T) {
	server := New(Config{Addr: "127.0.0.1:0", Path: "/cam.ts"}, testLogger())

	if server.IdleFor() <= 0 {
		t.Fatal("a server nobody is watching is already idle")
	}

	v := &viewer{frames: make(chan miss.Packet, 1)}
	server.add(v)
	if got := server.IdleFor(); got != 0 {
		t.Fatalf("IdleFor = %v with a viewer attached, want 0", got)
	}

	server.remove(v)
	if server.IdleFor() <= 0 {
		t.Fatal("the idle clock must restart when the last viewer leaves")
	}
}

func TestWaitForViewerWakesOnTheFirstOne(t *testing.T) {
	server := New(Config{Addr: "127.0.0.1:0", Path: "/cam.ts"}, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	woken := make(chan bool, 1)
	go func() { woken <- server.WaitForViewer(ctx) }()

	time.Sleep(20 * time.Millisecond)
	server.add(&viewer{frames: make(chan miss.Packet, 1)})

	if !<-woken {
		t.Fatal("WaitForViewer did not notice the viewer")
	}
}

func TestWaitForViewerReturnsWhenTheContextEnds(t *testing.T) {
	server := New(Config{Addr: "127.0.0.1:0", Path: "/cam.ts"}, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	if server.WaitForViewer(ctx) {
		t.Fatal("nobody ever connected, WaitForViewer must report false")
	}
}

func TestHealthReportsAFrameAge(t *testing.T) {
	server := startServer(t)

	// Nobody is watching: an idle server is healthy, there is nothing to be late
	// for.
	response, err := http.Get(server.URL()[:strings.Index(server.URL(), "/cam.ts")] + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("idle health = %d, want 200", response.StatusCode)
	}

	// A viewer with frames flowing stays healthy.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL(), nil)
	stream, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("open the stream: %v", err)
	}
	defer stream.Body.Close()

	for i := 0; i < 3; i++ {
		server.Publish(packet(uint32(i), i == 0, int64(i)*50))
	}
	time.Sleep(20 * time.Millisecond)

	response, err = http.Get(server.URL()[:strings.Index(server.URL(), "/cam.ts")] + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("streaming health = %d, want 200", response.StatusCode)
	}
}

func TestIndexListsTheOutputs(t *testing.T) {
	server := startServer(t)
	base := server.URL()[:strings.Index(server.URL(), "/cam.ts")]

	response, err := http.Get(base + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer response.Body.Close()

	body, _ := io.ReadAll(response.Body)
	text := string(body)
	for _, want := range []string{"/cam.ts", "viewers:"} {
		if !strings.Contains(text, want) {
			t.Errorf("index missing %q:\n%s", want, text)
		}
	}

	// Anything else is still a 404 rather than the index.
	missing, err := http.Get(base + "/nope")
	if err != nil {
		t.Fatalf("GET /nope: %v", err)
	}
	defer missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown path = %d, want 404", missing.StatusCode)
	}
}

// The scrape is the only thing an external monitor sees, so the names and the
// shape matter more than the numbers.
func TestMetricsExposeWhatAPrometheusWouldWant(t *testing.T) {
	server := startServer(t)
	base := server.URL()[:strings.Index(server.URL(), "/cam.ts")]

	server.NoteState(status.StateStreaming)
	server.NoteSession(3)
	for i := 0; i < 4; i++ {
		server.Publish(packet(uint32(i), i == 0, int64(i)*50))
	}

	response, err := http.Get(base + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer response.Body.Close()

	body, _ := io.ReadAll(response.Body)
	text := string(body)

	if ct := response.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") || !strings.Contains(ct, "version=0.0.4") {
		t.Errorf("content-type = %q", ct)
	}

	for _, want := range []string{
		"# TYPE micam_up gauge",
		"micam_up 1",
		`micam_stream_state{state="streaming"} 1`,
		`micam_stream_state{state="idle"} 0`,
		"micam_session 3",
		`micam_viewers{kind="ts"} 0`,
		`micam_viewers{kind="hls"} 0`,
		"micam_frames_published_total 4",
		"micam_last_frame_age_seconds",
		"# TYPE micam_viewer_bytes_total counter",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("metrics missing %q", want)
		}
	}

	// A TYPE line must name a real metric type: passing a label name there
	// produces a scrape Prometheus rejects outright.
	validTypes := map[string]bool{"counter": true, "gauge": true, "histogram": true, "summary": true, "untyped": true}
	for _, line := range strings.Split(text, "\n") {
		rest, ok := strings.CutPrefix(line, "# TYPE ")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) != 2 || !validTypes[fields[1]] {
			t.Errorf("bad type line %q", line)
		}
	}

	// Every sample line must be "name value" or "name{labels} value".
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Errorf("malformed sample %q", line)
			continue
		}
		if _, err := strconv.ParseFloat(fields[1], 64); err != nil {
			t.Errorf("value of %q is not a number: %v", line, err)
		}
	}
}
