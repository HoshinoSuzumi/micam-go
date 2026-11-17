package stream

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"micam/internal/miss"
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
