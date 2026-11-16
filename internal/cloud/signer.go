package cloud

import (
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

// KV is one ordered form parameter. Order is part of the signature contract, so
// nothing here may be backed by a map.
type KV struct {
	Key   string
	Value string
}

// NewNonce returns 8 random bytes followed by the current minute as a
// big-endian uint32, base64-encoded: 12 bytes in total.
func NewNonce() (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:8]); err != nil {
		return "", fmt.Errorf("cloud: cannot read entropy: %w", err)
	}
	binary.BigEndian.PutUint32(raw[8:12], uint32(time.Now().Unix()/60))
	return base64.StdEncoding.EncodeToString(raw[:]), nil
}

// SignedNonce derives the per-request key: SHA-256 over the raw ssecurity and
// the decoded nonce, base64-encoded. It is both the RC4 key material and the
// signature salt.
func SignedNonce(ssecurity, nonce string) (string, error) {
	rawSsecurity, err := base64.StdEncoding.DecodeString(ssecurity)
	if err != nil {
		return "", fmt.Errorf("cloud: ssecurity is not base64: %w", err)
	}
	rawNonce, err := base64.StdEncoding.DecodeString(nonce)
	if err != nil {
		return "", fmt.Errorf("cloud: nonce is not base64: %w", err)
	}
	h := sha256.New()
	h.Write(rawSsecurity)
	h.Write(rawNonce)
	return base64.StdEncoding.EncodeToString(h.Sum(nil)), nil
}

// APIPath strips the scheme, host and the /app prefix from an API URL, so that
// https://ru.api.io.mi.com/app/miotspec/action becomes /miotspec/action.
func APIPath(rawURL string) string {
	path := rawURL
	// The reference implementation locates the end of the host by searching for
	// "com", which is fine for *.mi.com and deliberate: keep it, so a port or a
	// trailing slash behaves identically.
	if host := strings.Index(rawURL, "com"); host >= 0 {
		path = rawURL[host+3:]
	}
	return strings.ReplaceAll(path, "/app/", "/")
}

// Signature is SHA-1 over the method, the API path, every k=v pair in order,
// and the signed nonce:
//
//	POST&/miotspec/action&data=<value>&<signedNonce>
//
// Values are NOT URL-encoded here. They are base64 by the time they are
// signed, and encoding them early breaks every call.
func Signature(rawURL, method, signedNonce string, params []KV) string {
	var b strings.Builder
	b.WriteString(strings.ToUpper(method))
	b.WriteByte('&')
	b.WriteString(APIPath(rawURL))
	for _, kv := range params {
		b.WriteByte('&')
		b.WriteString(kv.Key)
		b.WriteByte('=')
		b.WriteString(kv.Value)
	}
	b.WriteByte('&')
	b.WriteString(signedNonce)

	sum := sha1.Sum([]byte(b.String()))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// EncryptParams builds the final form body: sign the plaintext parameters into
// rc4_hash__, RC4 the values, sign the encrypted set, then append the fields
// that travel in the clear.
func EncryptParams(rawURL, method, signedNonce, nonce, ssecurity string, params []KV) ([]KV, error) {
	key, err := base64.StdEncoding.DecodeString(signedNonce)
	if err != nil {
		return nil, fmt.Errorf("cloud: signed nonce is not base64: %w", err)
	}

	out := make([]KV, 0, len(params)+4)
	out = append(out, params...)
	out = append(out, KV{Key: "rc4_hash__", Value: Signature(rawURL, method, signedNonce, out)})

	for i := range out {
		out[i].Value = base64.StdEncoding.EncodeToString(RC4(key, []byte(out[i].Value)))
	}
	out = append(out, KV{Key: "signature", Value: Signature(rawURL, method, signedNonce, out)})
	out = append(out, KV{Key: "ssecurity", Value: ssecurity})
	out = append(out, KV{Key: "_nonce", Value: nonce})
	return out, nil
}
