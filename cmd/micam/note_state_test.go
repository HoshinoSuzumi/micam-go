package main

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"micam/internal/status"
	"micam/internal/stream"
)

// noteState feeds both the status file and the metrics gauge. It used to call
// itself instead of the reporter, which overflowed the stack on the first state
// transition: the process died a few seconds after every start, and with it the
// whole point of the state gauges.
func TestNoteStateReachesTheGauge(t *testing.T) {
	srv := stream.New(stream.Config{Addr: "127.0.0.1:0", Path: "/cam.ts"}, discard())
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	// A nil reporter is the documented no-op case; if this returned by recursing,
	// the test binary would die of a stack overflow before the assertion.
	noteState(nil, srv, status.StateStreaming, nil)

	resp, err := http.Get("http://" + srv.Addr() + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if !strings.Contains(string(body), `micam_stream_state{state="streaming"} 1`) {
		t.Fatalf("the state never reached the gauge:\n%s", body)
	}
}
