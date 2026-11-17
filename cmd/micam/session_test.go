package main

import (
	"testing"
	"time"
)

// healthy is a session that lived long enough to prove the camera works.
func healthy() outcome {
	return outcome{retryable: true, frames: 600, duration: time.Minute}
}

// brief is a session that connected but died almost immediately.
func brief() outcome {
	return outcome{retryable: true, frames: 40, duration: 2 * time.Second}
}

// The bug that ended a whole night of relaying: hours of success were paid for by
// the next five failures, and the process gave up instead of reconnecting.
func TestRetryPacerResetsTheBudgetAfterAHealthySession(t *testing.T) {
	p := &retryPacer{max: 5}

	for i := 0; i < 10; i++ {
		if stop, _ := p.after(healthy()); stop {
			t.Fatalf("gave up after %d drops, all of them after a healthy session", i+1)
		}
	}
	if p.attempt != 1 {
		t.Fatalf("attempt = %d, want the counter reset to a fresh incident", p.attempt)
	}
}

func TestRetryPacerGivesUpAfterTheBudget(t *testing.T) {
	p := &retryPacer{max: 2}

	for i := 0; i < 2; i++ {
		if stop, _ := p.after(brief()); stop {
			t.Fatalf("gave up after %d failures, budget was 2 retries", i+1)
		}
	}

	stop, _ := p.after(brief())
	if !stop {
		t.Fatal("the third consecutive failure must exhaust a budget of 2")
	}
}

// An unreachable Mi API says nothing about the camera, and restarting the process
// cannot fix it: it must never consume the session budget.
func TestRetryPacerNeverSpendsTheBudgetOnTheCloud(t *testing.T) {
	p := &retryPacer{max: 1}

	for i := 0; i < 50; i++ {
		if stop, _ := p.after(outcome{retryable: true, cloud: true}); stop {
			t.Fatalf("gave up on the cloud after %d attempts", i+1)
		}
	}
	if p.attempt != 0 {
		t.Fatalf("attempt = %d, want cloud failures to leave it at 0", p.attempt)
	}
}

// A healthy session in between clears the cloud ladder too, so a brief outage
// does not leave the next one waiting 30 seconds.
func TestRetryPacerResetsTheCloudLadder(t *testing.T) {
	p := &retryPacer{max: 5}

	for i := 0; i < 8; i++ {
		p.after(outcome{retryable: true, cloud: true})
	}
	if p.cloud != 8 {
		t.Fatalf("cloud = %d, want 8", p.cloud)
	}

	p.after(healthy())
	if p.cloud != 0 {
		t.Fatalf("cloud = %d, want the ladder reset by a healthy session", p.cloud)
	}
}

func TestRetryPacerStopsImmediatelyWhenReconnectIsOff(t *testing.T) {
	p := &retryPacer{max: 0}

	for _, o := range []outcome{brief(), healthy(), {cloud: true}} {
		if stop, _ := p.after(o); !stop {
			t.Fatalf("--reconnect 0 must not retry %+v", o)
		}
	}
}

func TestReconnectDelayPlateaus(t *testing.T) {
	cases := map[int]time.Duration{
		1:  5 * time.Second,
		3:  15 * time.Second,
		6:  30 * time.Second,
		20: maxReconnectDelay,
	}
	for attempt, want := range cases {
		if got := reconnectDelay(attempt); got != want {
			t.Errorf("reconnectDelay(%d) = %s, want %s", attempt, got, want)
		}
	}
}

// A session is healthy only if it actually relayed video: a long stall before the
// first frame must not be mistaken for a working camera.
func TestOutcomeHealthyRequiresFrames(t *testing.T) {
	if (outcome{frames: 0, duration: time.Hour}).healthy() {
		t.Fatal("a session without frames cannot be healthy")
	}
	if (outcome{frames: 600, duration: healthySession - time.Second}).healthy() {
		t.Fatal("a session shorter than healthySession cannot be healthy")
	}
	if !healthy().healthy() {
		t.Fatal("a session with frames past healthySession is healthy")
	}
}
