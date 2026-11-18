package stream

import (
	"net/http"
	"testing"
	"time"
)

// TestViewersInfoListsTransportStream checks the view a user gets: the address
// they are watching from, and how much has been sent.
func TestViewersInfoListsTransportStream(t *testing.T) {
	srv := startServer(t)

	go func() {
		resp, err := http.Get("http://" + srv.Addr() + "/cam.ts")
		if err != nil {
			return
		}
		defer resp.Body.Close()
		// Keep the connection alive long enough for the assertion.
		time.Sleep(300 * time.Millisecond)
	}()

	// Wait for the handler to register the viewer before sending anything: a frame
	// published earlier has nowhere to go.
	deadline := time.Now().Add(2 * time.Second)
	for len(srv.ViewersInfo()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the viewer never registered")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// A keyframe starts the viewer, so the next frame has a sink to go into.
	srv.Publish(packet(0, true, 0))
	srv.Publish(packet(1, false, 50))

	conns := srv.ViewersInfo()
	if len(conns) != 1 {
		t.Fatalf("%d connections listed, want 1", len(conns))
	}
	if conns[0].Kind != "ts" {
		t.Fatalf("kind = %q, want ts", conns[0].Kind)
	}
	if conns[0].Remote != "127.0.0.1" {
		t.Fatalf("remote = %q, want 127.0.0.1", conns[0].Remote)
	}
	if conns[0].Since.IsZero() {
		t.Fatal("Since was not recorded")
	}

	// The bytes arrive asynchronously; give the writer a moment, then check that
	// something has been counted.
	time.Sleep(100 * time.Millisecond)
	if bytes := srv.ViewersInfo()[0].Bytes; bytes == 0 {
		t.Fatal("no bytes were counted for the viewer")
	}
}

// TestHLSViewerIsCounted covers the gap this change closed: a player reading the
// playlist had never shown up in the viewer count.
func TestHLSViewerIsCounted(t *testing.T) {
	srv := startHLS(t)

	srv.Publish(hlsFrame(t, 19, 0, true))
	srv.Publish(hlsFrame(t, 1, 50, false))
	srv.Publish(hlsFrame(t, 19, 100, true))

	_, playlist := get(t, srv, "/cam.m3u8")
	uris := playlistURIs(string(playlist))
	if len(uris) == 0 {
		t.Fatal("no segments in the playlist")
	}
	get(t, srv, "/"+uris[0])

	conns := srv.ViewersInfo()
	if len(conns) != 1 {
		t.Fatalf("%d connections listed, want 1", len(conns))
	}
	if conns[0].Kind != "hls" {
		t.Fatalf("kind = %q, want hls", conns[0].Kind)
	}
	if conns[0].Requests < 2 {
		t.Fatalf("requests = %d, want at least 2 (playlist and segment)", conns[0].Requests)
	}
	if conns[0].Bytes == 0 {
		t.Fatal("no bytes were counted for the HLS player")
	}
	if srv.Viewers() != 1 {
		t.Fatalf("Viewers() = %d, want 1", srv.Viewers())
	}
}

// TestHLSViewerExpiresWhenItStopsAsking is why the session has a deadline: there
// is no socket to close when a player goes away.
func TestHLSViewerExpiresWhenItStopsAsking(t *testing.T) {
	srv := startHLS(t)

	get(t, srv, "/cam.m3u8")
	if len(srv.ViewersInfo()) != 1 {
		t.Fatal("the player did not register")
	}

	// Backdate the last request past the deadline, as if the tab were closed.
	srv.mu.Lock()
	for _, sess := range srv.hls {
		sess.last = time.Now().Add(-2 * hlsSessionTimeout)
	}
	srv.mu.Unlock()

	if got := len(srv.ViewersInfo()); got != 0 {
		t.Fatalf("%d connections still listed after the deadline", got)
	}
	if srv.Viewers() != 0 {
		t.Fatalf("Viewers() = %d after the deadline, want 0", srv.Viewers())
	}
}
