package ctl

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"micam/internal/status"
)

// requirePOSIX skips the tests that need a shell and POSIX process semantics.
//
// ctl only ever runs on fnOS, which is Linux, so this is not a portability
// promise: it keeps `go test ./...` usable on a Windows development machine
// instead of leaving four failures that mean nothing there. CI still runs them.
func requirePOSIX(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell and POSIX process existence; runs in CI on Linux")
	}
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newTestServer lays out an installation in a temp dir and stubs the lifecycle
// script, so the API can be exercised without fnOS or a camera.
func newTestServer(t *testing.T) (*Server, *[]string) {
	t.Helper()
	root := t.TempDir()
	paths := Paths{
		App:  filepath.Join(root, "micam"),
		Dest: filepath.Join(root, "micam", "target"),
		ETC:  filepath.Join(root, "micam", "etc"),
		VAR:  filepath.Join(root, "micam", "var"),
	}
	for _, dir := range []string{paths.ETC, paths.VAR} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	var calls []string
	server := &Server{
		Paths:   paths,
		Version: "test",
		Log:     quiet(),
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls = append(calls, filepath.Base(name)+" "+strings.Join(args, " "))
			return nil, nil
		},
	}
	return server, &calls
}

// request runs one CGI call and returns the status code and body.
func request(t *testing.T, server *Server, method, route, body string) (int, string) {
	t.Helper()
	// The gateway splits the query off into QUERY_STRING and leaves the path in
	// PATH_INFO, so the test does the same.
	path, query, _ := strings.Cut(route, "?")

	t.Setenv("REQUEST_METHOD", method)
	t.Setenv("PATH_INFO", path)
	t.Setenv("QUERY_STRING", query)

	var out strings.Builder
	server.Stdout = &out
	server.Stdin = strings.NewReader(body)
	server.Serve()

	head, payload, ok := strings.Cut(out.String(), "\r\n\r\n")
	if !ok {
		t.Fatalf("malformed CGI response: %q", out.String())
	}
	var code int
	for _, line := range strings.Split(head, "\r\n") {
		if rest, found := strings.CutPrefix(line, "Status: "); found {
			if _, err := fmtSscanf(rest, &code); err != nil {
				t.Fatalf("cannot parse %q: %v", line, err)
			}
		}
	}
	return code, payload
}

func fmtSscanf(line string, code *int) (int, error) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return 0, io.ErrUnexpectedEOF
	}
	n, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, err
	}
	*code = n
	return 1, nil
}

func decodeStatus(t *testing.T, body string) StatusResponse {
	t.Helper()
	var got StatusResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("cannot parse %q: %v", body, err)
	}
	return got
}

func TestStatusReportsStoppedWhenThereIsNoProcess(t *testing.T) {
	server, _ := newTestServer(t)

	code, body := request(t, server, "GET", "/api/status", "")
	if code != 200 {
		t.Fatalf("code = %d, body = %s", code, body)
	}
	got := decodeStatus(t, body)
	if got.App.Running || got.App.State != status.StateStopped {
		t.Fatalf("expected a stopped service, got %+v", got.App)
	}
	if got.Config.Region != "cn" || got.Config.Quality != 3 || got.Config.LAN != "prefer" {
		t.Fatalf("defaults not applied: %+v", got.Config)
	}
	if got.Configured {
		t.Fatal("nothing is configured right after installation")
	}
}

func TestStatusReadsThePublishedSnapshot(t *testing.T) {
	requirePOSIX(t)
	server, _ := newTestServer(t)

	// A live pidfile (this test process) and a snapshot from the daemon.
	if err := os.WriteFile(server.Paths.PIDFile(), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	snap := status.Snapshot{
		State: status.StateStreaming, Camera: "小鸡摄像机", Transport: "tcp 192.168.0.109:17637",
		Codec: "H265", Frames: 271331, FPS: 19.8, Viewers: 2, Resyncs: 0,
	}
	encoded, _ := json.Marshal(snap)
	if err := os.WriteFile(server.Paths.StatusFile(), encoded, 0o644); err != nil {
		t.Fatal(err)
	}

	code, body := request(t, server, "GET", "/api/status", "")
	if code != 200 {
		t.Fatalf("code = %d", code)
	}
	got := decodeStatus(t, body)
	if !got.App.Running || got.App.State != status.StateStreaming {
		t.Fatalf("app = %+v", got.App)
	}
	if got.App.Frames != 271331 || got.App.Camera != "小鸡摄像机" || got.App.Viewers != 2 {
		t.Fatalf("snapshot not surfaced: %+v", got.App)
	}
}

func TestStatusFallsBackToStoppedWithoutAPid(t *testing.T) {
	server, _ := newTestServer(t)

	// A snapshot left behind by a service that has since exited: the file still
	// says "streaming", but nothing is running.
	snap := status.Snapshot{State: status.StateStreaming, Frames: 10, Error: "boom"}
	encoded, _ := json.Marshal(snap)
	if err := os.WriteFile(server.Paths.StatusFile(), encoded, 0o644); err != nil {
		t.Fatal(err)
	}

	_, body := request(t, server, "GET", "/api/status", "")
	got := decodeStatus(t, body)
	if got.App.Running || got.App.State != status.StateStopped {
		t.Fatalf("a stale snapshot must not look alive: %+v", got.App)
	}
	if got.App.Error != "boom" {
		t.Fatalf("the reason it stopped should survive: %q", got.App.Error)
	}
}

func TestControlCallsTheLifecycleScript(t *testing.T) {
	server, calls := newTestServer(t)

	for _, action := range []string{"start", "stop", "restart"} {
		code, body := request(t, server, "POST", "/api/control", `{"action":"`+action+`"}`)
		if code != 200 {
			t.Fatalf("%s: code = %d, body = %s", action, code, body)
		}
	}

	want := []string{"main start", "main stop", "main stop", "main start"}
	if strings.Join(*calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %v, want %v", *calls, want)
	}
}

func TestControlRejectsAnUnknownAction(t *testing.T) {
	server, _ := newTestServer(t)
	code, _ := request(t, server, "POST", "/api/control", `{"action":"explode"}`)
	if code != 400 {
		t.Fatalf("code = %d, want 400", code)
	}
}

func TestConfigIsSavedAndApplied(t *testing.T) {
	requirePOSIX(t)
	server, calls := newTestServer(t)

	// Running before the save: the new values must be picked up by a restart.
	if err := os.WriteFile(server.Paths.PIDFile(), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}

	body := `{"user":"","user_id":"2255231260","region":"cn","did":"1221672311",` +
		`"quality":3,"pts_lead":100,"lan":"prefer","reconnect":5,"pass_token":"SECRET-TOKEN"}`
	code, out := request(t, server, "POST", "/api/config", body)
	if code != 200 {
		t.Fatalf("code = %d, body = %s", code, out)
	}
	if strings.Contains(out, "SECRET-TOKEN") {
		t.Fatal("the passToken must never come back in a response")
	}

	cfg, err := LoadConfig(server.Paths.EnvFile())
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.DID != "1221672311" || cfg.UserID != "2255231260" || cfg.Quality != 3 {
		t.Fatalf("saved config = %+v", cfg)
	}
	if token := server.Paths.ReadToken(); token != "SECRET-TOKEN" {
		t.Fatalf("token = %q", token)
	}
	if !server.Paths.Configured() {
		t.Fatal("a did plus a token should count as configured")
	}

	want := "main stop|main start"
	if strings.Join(*calls, "|") != want {
		t.Fatalf("calls = %v, want %s", *calls, want)
	}

	// A second save without a token must keep the stored one.
	body = `{"user_id":"2255231260","region":"cn","did":"1221672311","quality":2,"pts_lead":0,"lan":"only"}`
	if code, out := request(t, server, "POST", "/api/config", body); code != 200 {
		t.Fatalf("code = %d, body = %s", code, out)
	}
	if token := server.Paths.ReadToken(); token != "SECRET-TOKEN" {
		t.Fatalf("the stored token was lost: %q", token)
	}
	if cfg, _ := LoadConfig(server.Paths.EnvFile()); cfg.Quality != 2 || cfg.LAN != "only" {
		t.Fatalf("second save not applied: %+v", cfg)
	}
}

func TestConfigRejectsBadValues(t *testing.T) {
	server, calls := newTestServer(t)

	cases := []string{
		`{"user_id":"2255231260","region":"mars","did":"1"}`,
		`{"user_id":"2255231260","region":"cn","did":"not-a-did"}`,
		`{"user_id":"2255231260","region":"cn","did":"1","quality":9}`,
		`{"user_id":"","user":"","region":"cn","did":"1"}`,
	}
	for _, body := range cases {
		if code, _ := request(t, server, "POST", "/api/config", body); code != 400 {
			t.Errorf("body %s: code = %d, want 400", body, code)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("a rejected config must not touch the service: %v", *calls)
	}
}

func TestLogReturnsTheTail(t *testing.T) {
	server, _ := newTestServer(t)

	var lines []string
	for i := 0; i < 500; i++ {
		lines = append(lines, "line "+strconv.Itoa(i))
	}
	if err := os.WriteFile(server.Paths.LogFile(), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, body := request(t, server, "GET", "/api/log?lines=10", "")
	if code != 200 {
		t.Fatalf("code = %d", code)
	}
	got := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if len(got) != 10 {
		t.Fatalf("got %d lines: %v", len(got), got)
	}
	if got[len(got)-1] != "line 499" {
		t.Fatalf("last line = %q", got[len(got)-1])
	}
}

func TestUnknownRouteIs404(t *testing.T) {
	server, _ := newTestServer(t)
	if code, _ := request(t, server, "GET", "/api/nope", ""); code != 404 {
		t.Fatalf("code = %d, want 404", code)
	}
}

func TestWrongMethodIsRejected(t *testing.T) {
	server, _ := newTestServer(t)
	if code, _ := request(t, server, "GET", "/api/control", ""); code != 405 {
		t.Fatalf("code = %d, want 405", code)
	}
}

// A running process with no status file used to look exactly like a session that
// never manages to connect. It is a different problem - usually the package not
// passing --status-file - so it gets its own stage.
func TestStatusSaysWhenNothingPublishesState(t *testing.T) {
	requirePOSIX(t)
	server, _ := newTestServer(t)

	if err := os.WriteFile(server.Paths.PIDFile(), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}

	_, body := request(t, server, "GET", "/api/status", "")
	got := decodeStatus(t, body)
	if !got.App.Running {
		t.Fatal("expected a running service")
	}
	if got.App.State != status.StateConnecting || got.App.Stage != status.Stage("no-status-file") {
		t.Fatalf("state/stage = %q/%q, want connecting/no-status-file", got.App.State, got.App.Stage)
	}
}
