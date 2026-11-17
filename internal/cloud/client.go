package cloud

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"micam/internal/creds"
	"micam/internal/pppp"
)

const (
	loginBase   = "https://account.xiaomi.com/pass"
	sid         = "xiaomiio"
	startMarker = "&&&START&&&"
)

// ErrNoPassword is returned when there is no cached passToken and no password
// was supplied.
var ErrNoPassword = errors.New("cloud: no usable passToken and no password supplied")

// ErrPassTokenRejected means a passToken was supplied but the account service
// did not accept it. It is almost always a userId mismatch, or an expired
// token.
var ErrPassTokenRejected = errors.New("cloud: the passToken was rejected")

// TwoFactorError reports that the account wants a second factor. Confirming the
// URL in a browser does not help: the approval binds to that browser's device
// id, not ours. The only way through is to paste a passToken.
type TwoFactorError struct {
	URL string
}

func (e *TwoFactorError) Error() string {
	return "cloud: two-factor verification required: " + e.URL
}

// Client talks to the Mi cloud: login, signed API calls, device list and P2P
// provisioning.
type Client struct {
	creds     *creds.Credentials
	jar       *cookiejar.Jar
	cookies   *cookieRecorder
	login     *http.Client
	api       *http.Client
	loginBase string
}

// New prepares a client. It generates a stable-ish device id when the
// credentials do not carry one yet; the caller must persist it, because Xiaomi
// binds two-factor approval to it.
func New(c *creds.Credentials) (*Client, error) {
	if c == nil {
		return nil, errors.New("cloud: nil credentials")
	}
	if c.DeviceID == "" {
		id, err := randomDeviceID()
		if err != nil {
			return nil, err
		}
		c.DeviceID = id
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("cloud: cannot create a cookie jar: %w", err)
	}

	recorder := newCookieRecorder()

	return &Client{
		creds:   c,
		jar:     jar,
		cookies: recorder,
		login: &http.Client{
			Timeout:   30 * time.Second,
			Jar:       jar,
			Transport: &recordingTransport{base: http.DefaultTransport, recorder: recorder},
		},
		api: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		loginBase: loginBase,
	}, nil
}

// Credentials exposes the session the client is mutating.
func (c *Client) Credentials() *creds.Credentials { return c.creds }

func (c *Client) userAgent() string {
	return "Android-7.1.1-1.0.0-ONEPLUS A3010-136-" + c.creds.DeviceID +
		" APP/xiaomi.smarthome APPV/62830"
}

// Login authenticates. A stored passToken is tried first and never triggers
// two-factor; the password path is only used when there is no token.
func (c *Client) Login(ctx context.Context, password string) error {
	if err := c.seedCookies(); err != nil {
		return err
	}

	step, err := c.getStep(ctx, c.loginBase+"/serviceLogin?sid="+sid+"&_json=true")
	if err != nil {
		return err
	}

	if step.Ssecurity == "" {
		if password == "" {
			// The account service explained itself; report that instead of guessing.
			if step.NotificationURL != "" {
				return &TwoFactorError{URL: step.NotificationURL.String()}
			}
			if c.creds.CanRenew() {
				detail := rejectionDetail(step)
				if detail != "" {
					return fmt.Errorf("%w: %s (sent userId=%q, passToken %s)",
						ErrPassTokenRejected, detail, c.cookieUserID(),
						tokenFingerprint(c.creds.PassToken))
				}
				return fmt.Errorf("%w (sent userId=%q, passToken %s)",
					ErrPassTokenRejected, c.cookieUserID(),
					tokenFingerprint(c.creds.PassToken))
			}
			return ErrNoPassword
		}
		step, err = c.passwordLogin(ctx, step, password)
		if err != nil {
			return err
		}
	}

	if step.NotificationURL != "" {
		return &TwoFactorError{URL: step.NotificationURL.String()}
	}
	if step.Ssecurity == "" {
		return fmt.Errorf("cloud: login failed: no ssecurity in the response")
	}

	c.creds.Ssecurity = step.Ssecurity.String()
	if uid := step.UserID.String(); uid != "" {
		c.creds.UserID = uid
	}
	if step.PassToken != "" {
		c.creds.PassToken = step.PassToken.String()
	}

	return c.finishAuth(ctx, step)
}

func (c *Client) seedCookies() error {
	u, err := url.Parse("https://account.xiaomi.com")
	if err != nil {
		return fmt.Errorf("cloud: %w", err)
	}

	// The domain matters: the login exchange redirects between xiaomi.com hosts,
	// and host-only cookies are dropped on a cross-host hop.
	const domain = ".xiaomi.com"
	cookies := []*http.Cookie{
		{Name: "sdkVersion", Value: "accountsdk-18.8.15", Path: "/", Domain: domain},
		{Name: "deviceId", Value: c.creds.DeviceID, Path: "/", Domain: domain},
	}
	if c.creds.CanRenew() {
		cookies = append(cookies,
			&http.Cookie{Name: "userId", Value: c.cookieUserID(), Path: "/", Domain: domain},
			&http.Cookie{Name: "passToken", Value: c.creds.PassToken, Path: "/", Domain: domain},
		)
	}
	c.jar.SetCookies(u, cookies)
	return nil
}

// cookieUserID is the value Xiaomi expects in the userId cookie: a numeric
// account id. Falling back to whatever the user typed only works when that
// happened to be the numeric id.
func (c *Client) cookieUserID() string {
	if c.creds.UserID != "" {
		return c.creds.UserID
	}
	return c.creds.User
}

func (c *Client) getStep(ctx context.Context, target string) (*loginStep, error) {
	resp, err := c.get(ctx, target)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cloud: cannot read the login response: %w", err)
	}

	var step loginStep
	if err := json.Unmarshal(stripMarker(body), &step); err != nil {
		return nil, fmt.Errorf("cloud: cannot parse the login response: %w", err)
	}
	return &step, nil
}

func (c *Client) passwordLogin(ctx context.Context, step *loginStep, password string) (*loginStep, error) {
	sum := md5.Sum([]byte(password))
	form := url.Values{}
	form.Set("sid", sid)
	form.Set("hash", strings.ToUpper(hex.EncodeToString(sum[:])))
	form.Set("callback", step.Callback.String())
	form.Set("qs", step.Qs.String())
	form.Set("user", c.creds.User)
	form.Set("_sign", step.Sign.String())
	form.Set("_json", "true")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.loginBase+"/serviceLoginAuth2", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("cloud: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent())
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	body, err := c.do(c.login, req)
	if err != nil {
		return nil, err
	}

	var next loginStep
	if err := json.Unmarshal(stripMarker(body), &next); err != nil {
		return nil, fmt.Errorf("cloud: cannot parse the login response: %w", err)
	}
	return &next, nil
}

// finishAuth exchanges the login location for a serviceToken cookie.
func (c *Client) finishAuth(ctx context.Context, step *loginStep) error {
	sum := sha1.Sum([]byte("nonce=" + step.Nonce.String() + "&" + c.creds.Ssecurity))
	clientSign := base64.StdEncoding.EncodeToString(sum[:])

	target := step.Location.String() + "&clientSign=" + url.QueryEscape(clientSign)
	resp, err := c.get(ctx, target)
	if err != nil {
		return err
	}
	resp.Body.Close()

	token := c.cookie("serviceToken")
	if token == "" {
		// The account service sets the token on whichever host handles the
		// exchange, which is not always the one we started from. Report what we
		// actually saw instead of a bare failure.
		return fmt.Errorf(
			"cloud: login succeeded but no serviceToken was issued (hosts visited: %v; cookies set: %v)",
			c.cookies.trail(), c.cookies.names())
	}
	c.creds.ServiceToken = token
	return nil
}

// cookie looks a cookie up by name across the whole login exchange.
//
// The reference implementation reads every cookie in the store rather than
// asking for a specific URL, because the serviceToken is set on whichever host
// completes the exchange. A per-URL lookup here silently misses it.
func (c *Client) cookie(name string) string {
	if v := c.cookies.value(name); v != "" {
		return v
	}
	u, err := url.Parse("https://account.xiaomi.com")
	if err != nil {
		return ""
	}
	for _, ck := range c.jar.Cookies(u) {
		if ck.Name == name && ck.Value != "" {
			return ck.Value
		}
	}
	return ""
}

func (c *Client) get(ctx context.Context, target string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("cloud: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent())
	return c.login.Do(req)
}

func (c *Client) do(client *http.Client, req *http.Request) ([]byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloud: %s: %w", req.URL.Host, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cloud: cannot read the response from %s: %w", req.URL.Host, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cloud: HTTP %d from %s: %s",
			resp.StatusCode, req.URL.Path, truncate(string(body), 200))
	}
	return body, nil
}

// apiHost selects the regional API endpoint.
func (c *Client) apiHost() string {
	region := strings.ToLower(strings.TrimSpace(c.creds.Region))
	if region == "" || region == "cn" {
		return "api.io.mi.com"
	}
	return region + ".api.io.mi.com"
}

func (c *Client) apiCookies() string {
	token := c.creds.ServiceToken
	return "userId=" + c.creds.UserID +
		"; serviceToken=" + token +
		"; yetAnotherServiceToken=" + token +
		"; locale=en_GB; timezone=GMT%2B03%3A00; is_daylight=0; dst_offset=0" +
		"; channel=MI_APP_STORE"
}

// call performs one signed POST and returns the decrypted JSON body.
func (c *Client) call(ctx context.Context, path string, payload any) ([]byte, error) {
	if !c.creds.HasSession() {
		return nil, errors.New("cloud: no session; log in first")
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("cloud: cannot encode the payload: %w", err)
	}

	rawURL := "https://" + c.apiHost() + "/app" + path

	nonce, err := NewNonce()
	if err != nil {
		return nil, err
	}
	signedNonce, err := SignedNonce(c.creds.Ssecurity, nonce)
	if err != nil {
		return nil, err
	}
	params, err := EncryptParams(rawURL, http.MethodPost, signedNonce, nonce, c.creds.Ssecurity,
		[]KV{{Key: "data", Value: string(data)}})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL,
		strings.NewReader(encodeForm(params)))
	if err != nil {
		return nil, fmt.Errorf("cloud: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent())
	req.Header.Set("x-xiaomi-protocal-flag-cli", "PROTOCAL-HTTP2")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("MIOT-ENCRYPT-ALGORITHM", "ENCRYPT-RC4")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Cookie", c.apiCookies())

	body, err := c.do(c.api, req)
	if err != nil {
		return nil, err
	}

	key, err := base64.StdEncoding.DecodeString(signedNonce)
	if err != nil {
		return nil, fmt.Errorf("cloud: %w", err)
	}
	cipher, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(body)))
	if err != nil {
		// An expired serviceToken comes back as plaintext, not RC4.
		return nil, fmt.Errorf("cloud: unreadable response from %s (an expired session answers in plaintext): %s",
			path, truncate(string(body), 200))
	}
	return RC4(key, cipher), nil
}

// DeviceList returns every device bound to the account.
func (c *Client) DeviceList(ctx context.Context) ([]Device, error) {
	payload := deviceListPayload{GetVirtualModel: false, GetHuamiDevices: 0}
	raw, err := c.call(ctx, "/home/device_list", payload)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Result struct {
			List []struct {
				DID     string `json:"did"`
				Name    string `json:"name"`
				Model   string `json:"model"`
				LocalIP string `json:"localip"`
				Online  bool   `json:"isOnline"`
			} `json:"list"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("cloud: cannot parse the device list: %w: %s", err, truncate(string(raw), 200))
	}

	out := make([]Device, 0, len(resp.Result.List))
	for _, d := range resp.Result.List {
		out = append(out, Device{
			DID:     d.DID,
			Name:    d.Name,
			Model:   d.Model,
			LocalIP: d.LocalIP,
			Online:  d.Online,
		})
	}
	return out, nil
}

// Provision negotiates a P2P transport for one camera. appPubKeyHex must be the
// public half of the key pair the MISS session will use: without it the cloud
// answers code -6 "nil request".
func (c *Client) Provision(ctx context.Context, did, appPubKeyHex string) (*Provisioning, error) {
	payload := provisionPayload{
		AppPubKey:      appPubKeyHex,
		DID:            did,
		SupportVendors: "TUTK_CS2_MTP",
	}
	raw, err := c.call(ctx, "/v2/device/miss_get_vendor", payload)
	if err != nil {
		return nil, err
	}

	var resp provisionResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("cloud: cannot parse miss_get_vendor: %w: %s", err, truncate(string(raw), 200))
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("cloud: miss_get_vendor failed: code=%d %s", resp.Code, truncate(string(raw), 200))
	}

	p := &Provisioning{
		VendorID:     resp.Result.Vendor.Vendor,
		P2PID:        resp.Result.Vendor.Params.P2PID,
		License:      resp.Result.Vendor.Params.License,
		CRCKey:       resp.Result.Vendor.Params.CRCKey,
		InitString:   resp.Result.Vendor.Params.InitString,
		DevicePublic: resp.Result.PublicKey,
		Sign:         resp.Result.Sign,
		MissVersion:  resp.Result.MissVersion,
	}
	if p.InitString != "" {
		servers, err := pppp.Servers(p.InitString)
		if err != nil {
			return nil, fmt.Errorf("cloud: cannot decode the rendezvous list: %w", err)
		}
		p.Servers = servers
	}
	return p, nil
}

// ---------------------------------------------------------------- payloads ---

type deviceListPayload struct {
	GetVirtualModel bool `json:"getVirtualModel"`
	GetHuamiDevices int  `json:"getHuamiDevices"`
}

type provisionPayload struct {
	AppPubKey      string `json:"app_pubkey"`
	DID            string `json:"did"`
	SupportVendors string `json:"support_vendors"`
}

type provisionResponse struct {
	Code   int `json:"code"`
	Result struct {
		Vendor struct {
			Vendor int `json:"vendor"`
			Params struct {
				P2PID      string `json:"p2p_id"`
				License    string `json:"license"`
				CRCKey     string `json:"crc_key"`
				InitString string `json:"init_string"`
			} `json:"vendor_params"`
		} `json:"vendor"`
		PublicKey   string `json:"public_key"`
		Sign        string `json:"sign"`
		MissVersion string `json:"miss_version"`
	} `json:"result"`
}

// loginStep is the shape of both serviceLogin and serviceLoginAuth2 responses.
type loginStep struct {
	Ssecurity       flexString `json:"ssecurity"`
	UserID          flexString `json:"userId"`
	PassToken       flexString `json:"passToken"`
	Location        flexString `json:"location"`
	Nonce           flexString `json:"nonce"`
	NotificationURL flexString `json:"notificationUrl"`
	Callback        flexString `json:"callback"`
	Qs              flexString `json:"qs"`
	Sign            flexString `json:"_sign"`
	Code            flexString `json:"code"`
	Desc            flexString `json:"description"`
}

// rejectionDetail renders what the account service said, so the caller can tell an
// expired token (70016 "登录验证失败") from a userId that belongs to another
// account - both arrive as "no ssecurity" and used to look identical.
func rejectionDetail(step *loginStep) string {
	var parts []string
	if code := strings.TrimSpace(step.Code.String()); code != "" && code != "0" {
		parts = append(parts, "code="+code)
	}
	if desc := strings.TrimSpace(step.Desc.String()); desc != "" {
		parts = append(parts, desc)
	}
	return strings.Join(parts, " ")
}

// tokenFingerprint identifies a passToken without printing it: its length and the
// first bytes of its SHA-256. Two copies that look alike can differ by a trailing
// space or a truncated tail, and comparing fingerprints across machines answers
// "is it even the same token?" without putting the secret in a log.
func tokenFingerprint(token string) string {
	if token == "" {
		return "none"
	}
	sum := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%d bytes, sha256 %s", len(token), hex.EncodeToString(sum[:4]))
}

// flexString accepts either a JSON string or a bare number, because userId
// comes back as a number and the rest as strings.
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*f = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexString(s)
		return nil
	}
	*f = flexString(string(b))
	return nil
}

func (f flexString) String() string { return string(f) }

// -------------------------------------------------------------- cookies ---

// cookieRecorder remembers every Set-Cookie seen during the login exchange,
// regardless of which host sent it. That mirrors the reference implementation,
// which reads the whole cookie store rather than querying one URL.
type cookieRecorder struct {
	mu    sync.Mutex
	seen  map[string]string
	hosts []string
}

func newCookieRecorder() *cookieRecorder {
	return &cookieRecorder{seen: make(map[string]string)}
}

func (r *cookieRecorder) record(resp *http.Response) {
	if resp == nil || resp.Request == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.hosts) < 24 {
		r.hosts = append(r.hosts, fmt.Sprintf("%s %d", resp.Request.URL.Host, resp.StatusCode))
	}
	for _, ck := range resp.Cookies() {
		if ck.Value != "" {
			r.seen[ck.Name] = ck.Value
		}
	}
}

func (r *cookieRecorder) value(name string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seen[name]
}

func (r *cookieRecorder) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.seen))
	for name := range r.seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (r *cookieRecorder) trail() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.hosts...)
}

// recordingTransport feeds every response through the recorder.
type recordingTransport struct {
	base     http.RoundTripper
	recorder *cookieRecorder
}

func (t *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err == nil {
		t.recorder.record(resp)
	}
	return resp, err
}

// ----------------------------------------------------------------- plumbing ---

func encodeForm(params []KV) string {
	var b strings.Builder
	for i, kv := range params {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(kv.Key))
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(kv.Value))
	}
	return b.String()
}

func stripMarker(body []byte) []byte {
	if bytes.HasPrefix(body, []byte(startMarker)) {
		return body[len(startMarker):]
	}
	return body
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

const deviceIDAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func randomDeviceID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("cloud: cannot generate a device id: %w", err)
	}
	for i, b := range buf {
		buf[i] = deviceIDAlphabet[int(b)%len(deviceIDAlphabet)]
	}
	return string(buf), nil
}
