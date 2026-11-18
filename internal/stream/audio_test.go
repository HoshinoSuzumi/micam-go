package stream

import (
	"bytes"
	"io"
	"net/http"
	"testing"
	"time"

	"micam/internal/audio"
	"micam/internal/miss"
)

func startAudio(t *testing.T) *Server {
	t.Helper()

	srv := New(Config{Addr: "127.0.0.1:0", Path: "/cam.ts", Audio: true, AudioRate: 8000}, testLogger())
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

func audioPacket(payload []byte) miss.Packet {
	return miss.Packet{Codec: miss.CodecPCMA, TimestampMS: 0, Payload: payload}
}

// waitForAudioListener gives the handler a moment to register: the response
// headers are written before the listener exists, so a test that publishes
// immediately can race it.
func waitForAudioListener(t *testing.T, srv *Server) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if srv.AudioListeners() > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the audio listener never registered")
}

func TestAudioEndpointServesWAV(t *testing.T) {
	srv := startAudio(t)

	resp, err := http.Get("http://" + srv.Addr() + "/cam.wav")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "audio/wav" {
		t.Fatalf("content type = %q", ct)
	}

	header := make([]byte, 44)
	if _, err := io.ReadFull(resp.Body, header); err != nil {
		t.Fatalf("read header: %v", err)
	}
	if string(header[0:4]) != "RIFF" || string(header[8:12]) != "WAVE" {
		t.Fatalf("not a WAV header: % x", header[:12])
	}

	waitForAudioListener(t, srv)

	// Two A-law octets are known values: 0x00 and 0xFF, from the ffmpeg fixture.
	payload := []byte{0x00, 0xFF}
	srv.Publish(audioPacket(payload))

	pcm := make([]byte, 4)
	if _, err := io.ReadFull(resp.Body, pcm); err != nil {
		t.Fatalf("read pcm: %v", err)
	}

	want := audio.DecodeALaw(nil, payload)
	for i := range want {
		if pcm[i] != want[i] {
			t.Fatalf("pcm byte %d = 0x%02x, want 0x%02x", i, pcm[i], want[i])
		}
	}
}

func TestAudioStaysOutOfTheVideoPath(t *testing.T) {
	srv := startAudio(t)

	// A viewer attached directly, without an HTTP connection: this tests the
	// routing, not the transport.
	v := &viewer{frames: make(chan miss.Packet, queueDepth), newSink: nil, log: testLogger()}
	srv.add(v)
	t.Cleanup(func() { srv.remove(v) })

	srv.Publish(audioPacket([]byte{0x00, 0x11, 0x22}))
	if got := len(v.frames); got != 0 {
		t.Fatalf("a video viewer received %d audio frames", got)
	}

	// And the reverse: video still reaches it.
	srv.Publish(packet(0, true, 0))
	if got := len(v.frames); got != 1 {
		t.Fatalf("video viewer has %d frames after a video frame, want 1", got)
	}
}

func TestAudioEndpointIsOffByDefault(t *testing.T) {
	srv := New(Config{Addr: "127.0.0.1:0", Path: "/cam.ts"}, testLogger())
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	resp, err := http.Get("http://" + srv.Addr() + "/cam.wav")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("audio answered %d with audio disabled, want 404", resp.StatusCode)
	}
}

func TestAudioHeadHasNoBody(t *testing.T) {
	srv := startAudio(t)

	resp, err := http.Head("http://" + srv.Addr() + "/cam.wav")
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD returned %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if len(body) != 0 {
		t.Fatalf("HEAD returned %d bytes", len(body))
	}
}

// TestAudioReachesTheHLSTrack covers the wiring between Publish and the
// segmenter.
//
// It was missing once: audio went to the WAV listeners and nowhere else, so the
// init segment declared an audio track that no fragment ever carried. Players
// waited for it and showed black - on every browser, while the transport stream
// stayed perfectly healthy.
func TestAudioReachesTheHLSTrack(t *testing.T) {
	srv := New(Config{Addr: "127.0.0.1:0", Path: "/cam.ts", HLS: true, Audio: true}, testLogger())
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	// One segment: a keyframe, some frames, then the next keyframe closes it.
	srv.Publish(hlsFrame(t, 19, 0, true))
	for i := 1; i <= 10; i++ {
		srv.Publish(hlsFrame(t, 1, int64(i)*50, false))
		// Two audio packets per video frame, as the camera sends them.
		srv.Publish(miss.Packet{Codec: miss.CodecOpus, TimestampMS: int64(i) * 50, Payload: []byte{0xF8, 1, 2, 3}})
		srv.Publish(miss.Packet{Codec: miss.CodecOpus, TimestampMS: int64(i)*50 + 25, Payload: []byte{0xF8, 4, 5, 6}})
	}
	srv.Publish(hlsFrame(t, 19, 550, true))

	_, playlist := get(t, srv, "/cam.m3u8")
	uris := playlistURIs(string(playlist))
	if len(uris) < 2 {
		t.Fatalf("playlist names %d URIs:\n%s", len(uris), playlist)
	}

	_, segment := get(t, srv, "/"+uris[1])
	if got := bytes.Count(segment[:min(len(segment), 4096)], []byte("traf")); got != 2 {
		t.Fatalf("segment has %d trafs, want 2 (video and audio)", got)
	}
}

func TestAudioIgnoresOpus(t *testing.T) {
	srv := startAudio(t)

	resp, err := http.Get("http://" + srv.Addr() + "/cam.wav")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	header := make([]byte, 44)
	if _, err := io.ReadFull(resp.Body, header); err != nil {
		t.Fatalf("read header: %v", err)
	}
	waitForAudioListener(t, srv)

	// Opus cannot be expanded to PCM here; writing it as if it were would put
	// noise on the wire, so it must be skipped instead.
	srv.Publish(miss.Packet{Codec: miss.CodecOpus, Payload: []byte{1, 2, 3, 4}})
	srv.Publish(audioPacket([]byte{0x00}))

	pcm := make([]byte, 2)
	if _, err := io.ReadFull(resp.Body, pcm); err != nil {
		t.Fatalf("read pcm: %v", err)
	}
	want := audio.DecodeALaw(nil, []byte{0x00})
	if pcm[0] != want[0] || pcm[1] != want[1] {
		t.Fatalf("pcm = % x, want % x", pcm, want)
	}
}
