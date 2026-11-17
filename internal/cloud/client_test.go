package cloud

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"micam/internal/creds"
)

// fakeLogin stands in for account.xiaomi.com. passTokenAccepted selects the two
// response shapes the real service produces.
func fakeLogin(t *testing.T, passTokenAccepted bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if passTokenAccepted {
			io.WriteString(w, `&&&START&&&{"ssecurity":"c2VjcmV0","userId":1234567890,`+
				`"passToken":"token","location":"https://account.xiaomi.com/pass/x"}`)
			return
		}
		// What the service returns when the passToken is not accepted: the form
		// parameters for a fresh login, and no ssecurity. Measured against the real
		// endpoint: an invalid token answers 70016 "登录验证失败".
		io.WriteString(w, `&&&START&&&{"code":70016,"description":"登录验证失败",`+
			`"qs":"%3Fsid%3Dxiaomiio","callback":"cb","_sign":"sig"}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLoginRejectsStalePassToken(t *testing.T) {
	srv := fakeLogin(t, false)

	c := &creds.Credentials{User: "15023365994", PassToken: "stale", Region: "cn"}
	client, err := New(c)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	client.loginBase = srv.URL

	err = client.Login(context.Background(), "")
	if !errors.Is(err, ErrPassTokenRejected) {
		t.Fatalf("err = %v, want ErrPassTokenRejected", err)
	}
	if !strings.Contains(err.Error(), "15023365994") {
		t.Fatalf("the error must name the userId that was sent: %v", err)
	}
	// The service's own code and description must survive to the log: "rejected"
	// alone sent the last debugging session in the wrong direction.
	if !strings.Contains(err.Error(), "70016") || !strings.Contains(err.Error(), "登录验证失败") {
		t.Fatalf("the error must carry the service's explanation: %v", err)
	}
	// The token never appears, but its fingerprint does: it is what lets two
	// machines compare copies without pasting the secret around.
	if !strings.Contains(err.Error(), "5 bytes") {
		t.Fatalf("the error must fingerprint the token that was sent: %v", err)
	}
}

func TestLoginWithoutCredentials(t *testing.T) {
	srv := fakeLogin(t, false)

	c := &creds.Credentials{User: "15023365994", Region: "cn"}
	client, err := New(c)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	client.loginBase = srv.URL

	if err := client.Login(context.Background(), ""); !errors.Is(err, ErrNoPassword) {
		t.Fatalf("err = %v, want ErrNoPassword", err)
	}
}

func TestCookieUserIDPrefersTheNumericID(t *testing.T) {
	c := &creds.Credentials{User: "15023365994", UserID: "1234567890", PassToken: "t"}
	client, err := New(c)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := client.cookieUserID(); got != "1234567890" {
		t.Fatalf("userId cookie = %q, want the numeric id", got)
	}
}

func TestNewGeneratesAStableDeviceID(t *testing.T) {
	c := &creds.Credentials{User: "u", Region: "cn"}
	first, err := New(c)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	id := first.Credentials().DeviceID
	if len(id) != 16 {
		t.Fatalf("device id = %q, want 16 characters", id)
	}

	second, err := New(c)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if second.Credentials().DeviceID != id {
		t.Fatal("a second client must reuse the generated device id, not replace it")
	}
}
