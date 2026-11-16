package cloud

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"micam/internal/creds"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("bad url %q: %v", raw, err)
	}
	return u
}

// The login exchange finishes on a host of the account service's choosing, not
// the one we started from. This test reproduces that: the second hop is reached
// under a different host name (localhost vs 127.0.0.1), so a per-URL cookie
// lookup cannot see the token while a whole-store lookup can.
func TestFinishAuthFindsServiceTokenOnAnotherHost(t *testing.T) {
	sts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "serviceToken", Value: "the-token", Path: "/"})
		io.WriteString(w, "ok")
	}))
	defer sts.Close()

	// Reaching the same listener under another host name makes the cookie jar
	// treat it as a different site.
	otherHost := strings.Replace(sts.URL, "127.0.0.1", "localhost", 1)

	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `&&&START&&&{"ssecurity":"c2VjcmV0","userId":1234567890,`+
			`"nonce":"bm9uY2U=","location":"`+otherHost+`?d=1"}`)
	}))
	defer login.Close()

	c := &creds.Credentials{User: "u", UserID: "1234567890", PassToken: "token", Region: "cn"}
	client, err := New(c)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	client.loginBase = login.URL

	if err := client.Login(context.Background(), ""); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if c.ServiceToken != "the-token" {
		t.Fatalf("serviceToken = %q, want the-token", c.ServiceToken)
	}
}

func TestFinishAuthReportsWhatItSaw(t *testing.T) {
	// No Set-Cookie anywhere: the exchange returns 200 but issues no token.
	sts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	}))
	defer sts.Close()

	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `&&&START&&&{"ssecurity":"c2VjcmV0","userId":1234567890,`+
			`"nonce":"bm9uY2U=","location":"`+sts.URL+`?d=1"}`)
	}))
	defer login.Close()

	c := &creds.Credentials{User: "u", UserID: "1234567890", PassToken: "token", Region: "cn"}
	client, err := New(c)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	client.loginBase = login.URL

	err = client.Login(context.Background(), "")
	if err == nil {
		t.Fatal("expected a failure")
	}
	if !strings.Contains(err.Error(), "hosts visited") {
		t.Fatalf("the error should report the hosts it saw: %v", err)
	}
}

func TestSeededCookiesUseTheXiaomiDomain(t *testing.T) {
	c := &creds.Credentials{User: "u", UserID: "1234567890", PassToken: "token", Region: "cn"}
	client, err := New(c)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.seedCookies(); err != nil {
		t.Fatalf("seedCookies: %v", err)
	}

	got := client.jar.Cookies(mustURL(t, "https://account.xiaomi.com/pass/serviceLogin"))
	names := make(map[string]string, len(got))
	for _, ck := range got {
		names[ck.Name] = ck.Value
	}
	for _, want := range []string{"sdkVersion", "deviceId", "userId", "passToken"} {
		if names[want] == "" {
			t.Fatalf("cookie %q was not seeded (got %v)", want, names)
		}
	}
	if names["userId"] != "1234567890" {
		t.Fatalf("userId cookie = %q, want the numeric id", names["userId"])
	}
}
