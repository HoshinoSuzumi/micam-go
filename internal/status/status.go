// Package status publishes what the stream is doing, for the management UI.
//
// The relay runs as a daemon with no IPC surface: no socket, no port. The cheapest
// way to let a separate process (the CGI behind the fnOS gateway) see what is
// happening is a small JSON file the daemon rewrites while it works.
package status

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// State is what the service is doing right now.
type State string

const (
	// StateConnecting covers everything before the first frame: provisioning,
	// the handshake, authentication, starting video.
	StateConnecting State = "connecting"
	// StateStreaming means frames are arriving.
	StateStreaming State = "streaming"
	// StateRetrying is a session that ended and is waiting for the next attempt.
	StateRetrying State = "retrying"
	// StateStopped is a clean shutdown (SIGTERM), or no session yet.
	StateStopped State = "stopped"
	// StateError is a failure that will not retry by itself.
	StateError State = "error"
)

// StreamURL is one address the stream can be reached at.
type StreamURL struct {
	URL   string `json:"url"`
	Iface string `json:"iface,omitempty"`
}

// Snapshot is the JSON document written to the status file.
type Snapshot struct {
	State State `json:"state"`
	// Session counts attempts within this process, so a restarting stream is
	// visible even when the state is briefly "connecting" again.
	Session   int    `json:"session"`
	Camera    string `json:"camera,omitempty"`
	Transport string `json:"transport,omitempty"`
	Codec     string `json:"codec,omitempty"`

	// Bind is the address the HTTP server actually bound, so the UI can tell a
	// wildcard listener from a loopback-only one.
	Bind string `json:"bind,omitempty"`
	// URLs are the addresses another machine can use to play the stream, one per
	// interface. The UI lets the user pick one instead of guessing.
	URLs []StreamURL `json:"urls,omitempty"`

	Frames  int64   `json:"frames"`
	FPS     float64 `json:"fps"`
	Viewers int     `json:"viewers"`
	Resyncs int     `json:"resyncs"`

	// Since is when the current session started, Updated when it was written.
	Since   time.Time `json:"since"`
	Updated time.Time `json:"updated"`

	Error string `json:"error,omitempty"`
	PID   int    `json:"pid"`
}

// Reporter keeps the status file current. A nil Reporter is usable: every method
// is a no-op, so callers never need to branch on whether reporting is enabled.
type Reporter struct {
	path string
	log  *slog.Logger
	now  func() time.Time
	// interval is how often the file is rewritten; a variable for tests.
	interval time.Duration

	mu     sync.Mutex
	snap   Snapshot
	frames int64
	at     time.Time

	stop   chan struct{}
	done   chan struct{}
	once   sync.Once
	shield sync.Mutex // serialises writes so an old one cannot land after Close
}

// Open starts reporting to path. An empty path returns nil, which is safe to call.
func Open(path string, log *slog.Logger) *Reporter {
	return openEvery(path, log, 2*time.Second)
}

// openEvery is Open with a configurable period, for tests.
func openEvery(path string, log *slog.Logger, every time.Duration) *Reporter {
	if path == "" {
		return nil
	}
	if log == nil {
		log = slog.Default()
	}
	r := &Reporter{
		path:     path,
		log:      log,
		now:      time.Now,
		interval: every,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	r.snap = Snapshot{State: StateStopped, PID: os.Getpid()}
	r.at = r.now()
	go r.loop()
	return r
}

func (r *Reporter) loop() {
	defer close(r.done)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
			r.publish()
		}
	}
}

// publish writes the current snapshot, refreshing the frame rate first.
func (r *Reporter) publish() {
	r.mu.Lock()
	now := r.now()
	if r.snap.State == StateStreaming {
		if dt := now.Sub(r.at).Seconds(); dt > 0 {
			r.snap.FPS = float64(r.snap.Frames-r.frames) / dt
		}
	}
	r.frames = r.snap.Frames
	r.at = now
	r.snap.Updated = now
	snap := r.snap
	r.mu.Unlock()

	if err := r.write(snap); err != nil {
		// The UI losing its status file is not worth stopping the relay for; log
		// it once per write and carry on.
		r.log.Warn("cannot write the status file", "path", r.path, "err", err)
	}
}

func (r *Reporter) write(snap Snapshot) error {
	body, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	body = append(body, '\n')

	r.shield.Lock()
	defer r.shield.Unlock()

	dir := filepath.Dir(r.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return err
	}
	// Rename is atomic: a reader either sees the previous file or this one, never
	// a half-written document.
	return os.Rename(tmp, r.path)
}

// SetState records the current state and, when it is an error, why.
func (r *Reporter) SetState(state State, err error) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snap.State = state
	r.snap.Error = ""
	if err != nil {
		r.snap.Error = err.Error()
	}
	if state != StateStreaming {
		r.snap.FPS = 0
	}
}

// BeginSession starts a new attempt: counters reset, the camera is not known yet.
func (r *Reporter) BeginSession(n int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snap.Session = n
	r.snap.Camera = ""
	r.snap.Transport = ""
	r.snap.Codec = ""
	r.snap.Frames = 0
	r.snap.FPS = 0
	r.snap.Viewers = 0
	r.snap.Resyncs = 0
	r.snap.Since = time.Time{}
	r.snap.Bind = ""
	r.snap.URLs = nil
	r.frames = 0
	r.at = r.now()
}

// Bind records where the stream is being served from, and where else it can be
// reached.
func (r *Reporter) Bind(addr string, urls []StreamURL) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snap.Bind = addr
	r.snap.URLs = urls
}

// Connected records how the camera was reached, and when the session started.
func (r *Reporter) Connected(camera, transport string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snap.Camera = camera
	r.snap.Transport = transport
	r.snap.Since = r.now()
}

// Frame counts one relayed video frame.
func (r *Reporter) Frame(codec string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snap.Frames++
	if codec != "" {
		r.snap.Codec = codec
	}
}

// Sample records what the fan-out is doing. It is cheap enough to call per frame.
func (r *Reporter) Sample(viewers, resyncs int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snap.Viewers = viewers
	r.snap.Resyncs = resyncs
}

// Close writes a final snapshot and stops the writer.
func (r *Reporter) Close() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		close(r.stop)
		<-r.done
		r.publish()
	})
}

// Read loads a snapshot written by Open. A missing or unreadable file is reported
// as an error, and the caller decides how to present it.
func Read(path string) (Snapshot, error) {
	var snap Snapshot
	body, err := os.ReadFile(path)
	if err != nil {
		return snap, err
	}
	if err := json.Unmarshal(body, &snap); err != nil {
		return snap, fmt.Errorf("status: cannot parse %s: %w", path, err)
	}
	return snap, nil
}
