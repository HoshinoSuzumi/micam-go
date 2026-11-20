package ctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"micam/internal/status"
)

// AppStatus is what the UI polls for.
type AppStatus struct {
	Name    string `json:"name"`
	Version string `json:"version"`

	Running bool         `json:"running"`
	PID     int          `json:"pid"`
	State   status.State `json:"state"`
	// Stage refines a "connecting" state.
	Stage status.Stage `json:"stage,omitempty"`

	Session   int    `json:"session"`
	Camera    string `json:"camera,omitempty"`
	Transport string `json:"transport,omitempty"`
	Codec     string `json:"codec,omitempty"`

	// Bind is where the HTTP server listens; "127.0.0.1:8554" means only this
	// machine can play the stream. URLs are the addresses others can use.
	Bind string             `json:"bind,omitempty"`
	URLs []status.StreamURL `json:"urls,omitempty"`
	// HLS are the same addresses pointing at the playlist, which is the only
	// thing Safari and iOS can play.
	HLS []status.StreamURL `json:"hls,omitempty"`
	// Audio is the WAV endpoint, when the camera's sound is enabled.
	Audio []status.StreamURL `json:"audio,omitempty"`
	// Connections lists who is watching, through which output.
	Connections []status.Connection `json:"connections,omitempty"`

	Frames  int64   `json:"frames"`
	FPS     float64 `json:"fps"`
	Viewers int     `json:"viewers"`
	Resyncs int     `json:"resyncs"`

	Since   *time.Time `json:"since,omitempty"`
	Updated *time.Time `json:"updated,omitempty"`

	Error string `json:"error,omitempty"`
}

// StatusResponse is the body of /api/status.
type StatusResponse struct {
	OK bool `json:"ok"`
	// Configured reports whether a start could succeed: a camera plus a way to
	// authenticate. Without it the UI explains what is missing instead of showing
	// a service that is "stopped" for no visible reason.
	Configured bool      `json:"configured"`
	App        AppStatus `json:"app"`
	Config     Config    `json:"config"`
}

// Server serves one CGI request.
type Server struct {
	Paths   Paths
	Version string
	Log     *slog.Logger

	// Run executes the lifecycle script. A field so tests can replace it.
	Run   func(ctx context.Context, name string, args ...string) ([]byte, error)
	Stdin io.Reader
	// Stdout receives the CGI response.
	Stdout io.Writer

	// controlTimeout bounds start/stop, which wait for the daemon to come up.
	controlTimeout time.Duration
}

// Serve handles one request and returns the process exit code.
func (s *Server) Serve() int {
	if s.Stdout == nil {
		s.Stdout = os.Stdout
	}
	if s.Stdin == nil {
		s.Stdin = os.Stdin
	}
	if s.Run == nil {
		s.Run = runCommand
	}
	if s.controlTimeout == 0 {
		s.controlTimeout = 40 * time.Second
	}

	reply := s.handle()
	s.write(reply)
	if reply.code >= 400 {
		return 1
	}
	return 0
}

type reply struct {
	code        int
	contentType string
	body        []byte
}

func (s *Server) write(r reply) {
	w := s.Stdout
	fmt.Fprintf(w, "Status: %d %s\r\n", r.code, http.StatusText(r.code))
	fmt.Fprintf(w, "Content-Type: %s\r\n", r.contentType)
	fmt.Fprint(w, "Cache-Control: no-store\r\n")
	fmt.Fprintf(w, "Content-Length: %d\r\n", len(r.body))
	fmt.Fprint(w, "\r\n")
	_, _ = w.Write(r.body)
}

func (s *Server) handle() reply {
	method := strings.ToUpper(os.Getenv("REQUEST_METHOD"))
	if method == "" {
		method = http.MethodGet
	}
	route := routeFromEnv()

	if s.Log != nil {
		s.Log.Debug("ctl request", "method", method, "route", route)
	}

	switch route {
	case "/api/status":
		if method != http.MethodGet {
			return s.methodNotAllowed(method, route)
		}
		return s.json(http.StatusOK, s.snapshot(nil))

	case "/api/cameras":
		if method != http.MethodGet {
			return s.methodNotAllowed(method, route)
		}
		return s.cameras()

	case "/api/log":
		if method != http.MethodGet {
			return s.methodNotAllowed(method, route)
		}
		return s.logTail()

	case "/api/config":
		if method != http.MethodPost {
			return s.methodNotAllowed(method, route)
		}
		return s.updateConfig()

	case "/api/control":
		if method != http.MethodPost {
			return s.methodNotAllowed(method, route)
		}
		return s.control()
	}

	return s.fail(http.StatusNotFound, fmt.Errorf("unknown route %q", route))
}

func (s *Server) methodNotAllowed(method, route string) reply {
	return s.fail(http.StatusMethodNotAllowed, fmt.Errorf("%s is not allowed on %s", method, route))
}

// routeFromEnv finds /api/... wherever the gateway put it. fnOS normally sets
// PATH_INFO; REQUEST_URI is the fallback for the case where it does not.
func routeFromEnv() string {
	for _, key := range []string{"PATH_INFO", "REQUEST_URI"} {
		value := os.Getenv(key)
		if !strings.Contains(value, "/api/") {
			continue
		}
		if i := strings.Index(value, "/api/"); i > 0 {
			value = value[i:]
		}
		if i := strings.IndexAny(value, "?#"); i >= 0 {
			value = value[:i]
		}
		return strings.TrimRight(value, "/")
	}
	return ""
}

func (s *Server) json(code int, body any) reply {
	encoded, err := json.Marshal(body)
	if err != nil {
		return s.fail(http.StatusInternalServerError, err)
	}
	return reply{code: code, contentType: "application/json; charset=utf-8", body: append(encoded, '\n')}
}

func (s *Server) fail(code int, err error) reply {
	if s.Log != nil {
		s.Log.Warn("ctl request failed", "code", code, "err", err)
	}
	encoded, _ := json.Marshal(map[string]any{"ok": false, "error": err.Error()})
	return reply{code: code, contentType: "application/json; charset=utf-8", body: append(encoded, '\n')}
}

// snapshot assembles what the UI shows. err, if given, is surfaced alongside so a
// failed action explains itself in the same response.
func (s *Server) snapshot(actionErr error) StatusResponse {
	cfg, err := LoadConfig(s.Paths.EnvFile())
	if err != nil && s.Log != nil {
		s.Log.Warn("cannot read the configuration", "err", err)
	}
	cfg.HasToken = s.Paths.TokenStored()
	cfg.PassToken = ""

	app := AppStatus{Name: "micam", Version: s.Version, State: status.StateStopped}

	pid := readPID(s.Paths.PIDFile())
	app.PID = pid
	app.Running = pid > 0 && alive(pid)

	// Read the file even when the service is stopped: its last error is what
	// explains why it is not running.
	snap, readErr := status.Read(s.Paths.StatusFile())
	if readErr == nil {
		app.State = snap.State
		app.Stage = snap.Stage
		app.Session = snap.Session
		app.Camera = snap.Camera
		app.Transport = snap.Transport
		app.Codec = snap.Codec
		app.Bind = snap.Bind
		app.URLs = snap.URLs
		app.HLS = snap.HLS
		app.Audio = snap.Audio
		app.Connections = snap.Connections
		app.Frames = snap.Frames
		app.FPS = snap.FPS
		app.Viewers = snap.Viewers
		app.Resyncs = snap.Resyncs
		app.Error = snap.Error
		if !snap.Since.IsZero() {
			since := snap.Since
			app.Since = &since
		}
		if !snap.Updated.IsZero() {
			updated := snap.Updated
			app.Updated = &updated
		}
	}

	if !app.Running {
		app.State = status.StateStopped
		app.FPS = 0
	} else if readErr != nil {
		// Running but no status file: either it is between start and the first
		// write, or nothing is publishing one at all. Say which, because the second
		// case looks identical to a session that never connects.
		app.State = status.StateConnecting
		app.Stage = status.Stage("no-status-file")
	}

	if actionErr != nil {
		app.Error = actionErr.Error()
	}

	return StatusResponse{
		OK:         actionErr == nil,
		Configured: s.Paths.Configured(),
		App:        app,
		Config:     cfg,
	}
}

// updateConfig writes the configuration and makes it take effect.
func (s *Server) updateConfig() reply {
	body, err := io.ReadAll(io.LimitReader(s.Stdin, 1<<20))
	if err != nil {
		return s.fail(http.StatusBadRequest, fmt.Errorf("cannot read the request body: %w", err))
	}

	// Start from what is stored, then overlay the request: a field the caller did
	// not send must keep its current value. Decoding into a zero Config would turn
	// `hls` off for anyone posting a partial update.
	incoming, err := LoadConfig(s.Paths.EnvFile())
	if err != nil {
		return s.fail(http.StatusInternalServerError, fmt.Errorf("cannot read the current configuration: %w", err))
	}
	if err := json.Unmarshal(body, &incoming); err != nil {
		return s.fail(http.StatusBadRequest, fmt.Errorf("cannot parse the request body: %w", err))
	}
	if err := incoming.Validate(); err != nil {
		return s.fail(http.StatusBadRequest, err)
	}

	if err := s.Paths.WriteConfig(incoming); err != nil {
		return s.fail(http.StatusInternalServerError, fmt.Errorf("cannot save the configuration: %w", err))
	}

	// Apply it: stop first so the new values are read on a fresh start, then start
	// if there is enough to start with.
	if alive(readPID(s.Paths.PIDFile())) {
		if _, err := s.run("stop"); err != nil {
			return s.json(http.StatusOK, s.snapshot(fmt.Errorf("saved, but stopping the service failed: %w", err)))
		}
	}
	if s.Paths.Configured() {
		if _, err := s.run("start"); err != nil {
			return s.json(http.StatusOK, s.snapshot(fmt.Errorf("saved, but starting the service failed: %w", err)))
		}
	}
	return s.json(http.StatusOK, s.snapshot(nil))
}

// control handles start, stop and restart.
func (s *Server) control() reply {
	body, err := io.ReadAll(io.LimitReader(s.Stdin, 1<<16))
	if err != nil {
		return s.fail(http.StatusBadRequest, fmt.Errorf("cannot read the request body: %w", err))
	}
	var request struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return s.fail(http.StatusBadRequest, fmt.Errorf("cannot parse the request body: %w", err))
	}

	switch request.Action {
	case "start", "stop":
		out, err := s.run(request.Action)
		if err != nil {
			return s.json(http.StatusInternalServerError, s.snapshot(withOutput(err, out)))
		}
		return s.json(http.StatusOK, s.snapshot(nil))
	case "restart":
		if _, err := s.run("stop"); err != nil {
			return s.json(http.StatusInternalServerError, s.snapshot(err))
		}
		out, err := s.run("start")
		if err != nil {
			return s.json(http.StatusInternalServerError, s.snapshot(withOutput(err, out)))
		}
		return s.json(http.StatusOK, s.snapshot(nil))
	}
	return s.fail(http.StatusBadRequest, fmt.Errorf("unknown action %q", request.Action))
}

func (s *Server) run(action string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.controlTimeout)
	defer cancel()
	return s.Run(ctx, s.Paths.Main(), action)
}

// logTail returns the end of the service log as plain text: no JSON escaping, and
// a browser can show it as-is.
func (s *Server) logTail() reply {
	lines := 200
	if values, err := url.ParseQuery(os.Getenv("QUERY_STRING")); err == nil {
		if n, err := strconv.Atoi(values.Get("lines")); err == nil && n > 0 {
			lines = n
		}
	}
	if lines > 2000 {
		lines = 2000
	}

	body, err := tailFile(s.Paths.LogFile(), lines, 256<<10)
	if err != nil {
		return reply{
			code:        http.StatusOK,
			contentType: "text/plain; charset=utf-8",
			body:        []byte("（还没有日志）\n"),
		}
	}
	return reply{code: http.StatusOK, contentType: "text/plain; charset=utf-8", body: body}
}

func withOutput(err error, out []byte) error {
	text := strings.TrimSpace(string(out))
	if text == "" {
		return err
	}
	// The lifecycle script logs why it refused; the last line is the actionable one.
	lines := strings.Split(text, "\n")
	if len(lines) > 6 {
		lines = lines[len(lines)-6:]
	}
	return fmt.Errorf("%w: %s", err, strings.Join(lines, " / "))
}

func readPID(path string) int {
	body, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(body)))
	if err != nil {
		return 0
	}
	return pid
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return out, fmt.Errorf("%s %s timed out", name, strings.Join(args, " "))
		}
		return out, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return out, nil
}

// tailFile returns the last lines of a file without reading all of it.
func tailFile(path string, lines int, maxBytes int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	offset := int64(0)
	if size > maxBytes {
		offset = size - maxBytes
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	body, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		// Drop the partial first line.
		if i := strings.IndexByte(string(body), '\n'); i >= 0 {
			body = body[i+1:]
		}
	}

	all := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return []byte(strings.Join(all, "\n") + "\n"), nil
}
