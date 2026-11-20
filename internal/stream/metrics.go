package stream

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"micam/internal/status"
)

// handleMetrics renders the Prometheus text exposition format.
//
// Hand-written on purpose: the format is a HELP line, a TYPE line and samples, and
// a client library would be the only dependency this program has beyond
// golang.org/x/crypto. Everything here is a counter or a gauge read at scrape
// time; nothing accumulates across scrapes.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	viewers := s.ViewersInfo()
	kinds := map[string]int{"ts": 0, "wav": 0, "hls": 0}
	for _, v := range viewers {
		kinds[v.Kind]++
	}

	s.mu.RLock()
	state := s.state
	session := s.session
	s.mu.RUnlock()

	var b line
	b.gauge("micam_up", "Always 1; if this is missing the process is not answering.", 1)
	b.gauge("micam_session", "Attempt number within this process.", float64(session))

	// One sample per state, zero for the ones that are not current: a dashboard
	// can then alert on the value instead of testing for absence.
	b.header("micam_stream_state", "Current session state, 1 for the active one.", "gauge")
	states := []status.State{
		status.StateConnecting, status.StateStreaming, status.StateRetrying,
		status.StateIdle, status.StateStopped, status.StateError,
	}
	names := make([]string, 0, len(states))
	for _, st := range states {
		names = append(names, string(st))
	}
	sort.Strings(names)
	for _, name := range names {
		value := 0
		if string(state) == name {
			value = 1
		}
		fmt.Fprintf(&b, "micam_stream_state{state=%q} %d\n", name, value)
	}

	b.header("micam_viewers", "Players connected, by output.", "gauge")
	for _, kind := range []string{"hls", "ts", "wav"} {
		fmt.Fprintf(&b, "micam_viewers{kind=%q} %d\n", kind, kinds[kind])
	}

	b.counter("micam_frames_published_total", "Video frames fanned out to players and HLS.", s.framesTotal.Load())
	b.counter("micam_audio_frames_published_total", "Audio frames fanned out.", s.audioTotal.Load())
	b.counter("micam_viewer_bytes_total", "Bytes written to players.", s.bytesTotal.Load())
	b.counter("micam_viewer_resyncs_total",
		"Times a player fell behind and was restarted at a keyframe.", s.resyncsTotal.Load())

	age := -1.0
	if last := s.lastFrame.Load(); last > 0 {
		age = time.Since(time.Unix(0, last)).Seconds()
	}
	b.gauge("micam_last_frame_age_seconds",
		"Seconds since the last video frame, -1 before the first one.", age)

	if s.seg != nil {
		b.gauge("micam_hls_segments", "Segments currently in the playlist window.",
			float64(s.seg.Segments()))
		b.counter("micam_hls_frames_dropped_total",
			"Frames the segmenter could not place (no parameter sets yet, or a stalled clock).",
			int64(s.seg.Dropped()))
	}

	_, _ = w.Write([]byte(b.String()))
}

// line builds the exposition text and dedupes the HELP/TYPE headers.
type line struct {
	help map[string]bool
	buf  []byte
}

func (l *line) String() string { return string(l.buf) }

// Write lets fmt.Fprintf append samples with labels directly.
func (l *line) Write(p []byte) (int, error) {
	l.buf = append(l.buf, p...)
	return len(p), nil
}

func (l *line) header(name, help, typ string) {
	if l.help == nil {
		l.help = make(map[string]bool)
	}
	if l.help[name] {
		return
	}
	l.help[name] = true
	l.buf = append(l.buf, "# HELP "...)
	l.buf = append(l.buf, name...)
	l.buf = append(l.buf, ' ')
	l.buf = append(l.buf, help...)
	l.buf = append(l.buf, "\n# TYPE "...)
	l.buf = append(l.buf, name...)
	l.buf = append(l.buf, ' ')
	l.buf = append(l.buf, typ...)
	l.buf = append(l.buf, '\n')
}

func (l *line) counter(name, help string, value int64) {
	l.header(name, help, "counter")
	l.buf = append(l.buf, name...)
	l.buf = append(l.buf, ' ')
	l.buf = strconv.AppendInt(l.buf, value, 10)
	l.buf = append(l.buf, '\n')
}

func (l *line) gauge(name, help string, value float64) {
	l.header(name, help, "gauge")
	l.buf = append(l.buf, name...)
	l.buf = append(l.buf, ' ')
	l.buf = strconv.AppendFloat(l.buf, value, 'f', -1, 64)
	l.buf = append(l.buf, '\n')
}

// NoteState records what the session loop is doing, for the state gauge.
//
// The stream server has no view of the camera side; this is the one line of
// communication from the loop to the scrape, and it is nil-safe because --dump
// runs without a server.
func (s *Server) NoteState(st status.State) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.state = st
	s.mu.Unlock()
}

// NoteSession records which attempt this process is on.
func (s *Server) NoteSession(n int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.session = n
	s.mu.Unlock()
}
