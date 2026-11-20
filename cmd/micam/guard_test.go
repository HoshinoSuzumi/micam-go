package main

import (
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"micam/internal/auth"
	"micam/internal/cli"
)

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestBuildAuthWithoutTokensIsOff(t *testing.T) {
	policy, err := buildAuth(&cli.Options{AuthRealm: "micam"}, discard())
	if err != nil {
		t.Fatalf("buildAuth: %v", err)
	}
	if policy.Enabled() {
		t.Fatal("a policy with no tokens must not require anything")
	}
	if len(policy.Tokens()) != 0 {
		t.Fatalf("tokens = %+v", policy.Tokens())
	}
}

// label=secret is the documented form; a bare value stays usable so a
// hand-written MICAM_TOKEN still works.
func TestBuildAuthReadsLabels(t *testing.T) {
	policy, err := buildAuth(&cli.Options{
		AuthRealm: "micam",
		Tokens:    []string{"客厅=TV-token-aaaaaaaaaaaa", "phone-token-bbbbbbbbbbbb"},
		ShareKey:  strings.Repeat("ab", 32),
	}, discard())
	if err != nil {
		t.Fatalf("buildAuth: %v", err)
	}

	tokens := policy.Tokens()
	if len(tokens) != 2 {
		t.Fatalf("tokens = %d, want 2", len(tokens))
	}
	if tokens[0].Label != "客厅" {
		t.Fatalf("first label = %q", tokens[0].Label)
	}
	// The second one had no label, so it must still be named after its position:
	// an unnamed credential in the viewer list explains nothing.
	if !strings.HasPrefix(tokens[1].Label, "token-") {
		t.Fatalf("second label = %q", tokens[1].Label)
	}
}

func TestBuildAuthRejectsBadShareKey(t *testing.T) {
	if _, err := buildAuth(&cli.Options{
		AuthRealm: "micam",
		Tokens:    []string{"laptop-token-cccccccccccc"},
		ShareKey:  "not-hex",
	}, discard()); err == nil {
		t.Fatal("a non-hex share key must be refused")
	}
}

func TestWithCredential(t *testing.T) {
	raw := "http://192.168.0.111:8554/cam.ts"

	if got := withCredential(raw, ""); got != raw {
		t.Fatalf("with no token: %q", got)
	}
	got := withCredential(raw, "abc-DEF")
	want := "http://micam:abc-DEF@192.168.0.111:8554/cam.ts"
	if got != want {
		t.Fatalf("withCredential = %q, want %q", got, want)
	}
}

// The switch that matters when the stream is published through a TCP tunnel on
// this host: the tunnel's requests look exactly like a local player's.
func TestBuildAuthHonoursNoLocalBypass(t *testing.T) {
	policy, err := buildAuth(&cli.Options{
		AuthRealm:     "micam",
		Tokens:        []string{"tunnel-token-aaaaaaa"},
		NoLocalBypass: true,
	}, discard())
	if err != nil {
		t.Fatalf("buildAuth: %v", err)
	}

	request := httptest.NewRequest("GET", "/cam.ts", nil)
	request.RemoteAddr = "127.0.0.1:4000"
	if _, err := policy.Authorize(request); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("err = %v, want a credential to be required from loopback", err)
	}
}
