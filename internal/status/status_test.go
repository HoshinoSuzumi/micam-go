package status

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o644)
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// waitFor reads the status file until cond is true, or the test times out.
func waitFor(t *testing.T, path string, cond func(Snapshot) bool) Snapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last Snapshot
	for time.Now().Before(deadline) {
		snap, err := Read(path)
		if err == nil {
			last = snap
			if cond(snap) {
				return snap
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("status file never matched, last = %+v", last)
	return last
}

func TestReporterPublishesWhatItCounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	rep := openEvery(path, quietLogger(), 5*time.Millisecond)
	defer rep.Close()

	rep.BeginSession(2)
	rep.SetState(StateConnecting, nil)
	rep.Connected("小鸡摄像机", "tcp 192.168.0.109:17637")
	rep.SetState(StateStreaming, nil)
	for i := 0; i < 25; i++ {
		rep.Frame("H265")
	}
	rep.Sample(3, 1)

	snap := waitFor(t, path, func(s Snapshot) bool { return s.Frames >= 25 })
	if snap.State != StateStreaming {
		t.Errorf("state = %q, want streaming", snap.State)
	}
	if snap.Session != 2 || snap.Camera != "小鸡摄像机" || snap.Transport == "" {
		t.Errorf("session/camera/transport not published: %+v", snap)
	}
	if snap.Codec != "H265" {
		t.Errorf("codec = %q", snap.Codec)
	}
	if snap.Viewers != 3 || snap.Resyncs != 1 {
		t.Errorf("viewers/resyncs = %d/%d", snap.Viewers, snap.Resyncs)
	}
	if snap.PID <= 0 {
		t.Errorf("pid = %d", snap.PID)
	}
}

func TestReporterRecordsFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	rep := openEvery(path, quietLogger(), 5*time.Millisecond)
	defer rep.Close()

	rep.SetState(StateRetrying, errTest("the camera stopped sending"))

	snap := waitFor(t, path, func(s Snapshot) bool { return s.State == StateRetrying })
	if snap.Error != "the camera stopped sending" {
		t.Errorf("error = %q", snap.Error)
	}
}

func TestReporterCloseWritesAFinalSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	rep := openEvery(path, quietLogger(), time.Hour) // the ticker never fires
	rep.SetState(StateStopped, nil)
	rep.Close()

	snap, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if snap.State != StateStopped {
		t.Errorf("state = %q, want stopped", snap.State)
	}
}

// A nil reporter is what every code path gets when --status-file is unset.
func TestNilReporterIsUsable(t *testing.T) {
	var rep *Reporter
	rep.SetState(StateStreaming, nil)
	rep.BeginSession(1)
	rep.Connected("", "")
	rep.Frame("H265")
	rep.Sample(0, 0)
	rep.Close()
}

func TestReadRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	if err := writeFile(path, "not json"); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil {
		t.Fatal("expected an error for a corrupt status file")
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }
