package cloud

import (
	"encoding/base64"
	"encoding/hex"
	"reflect"
	"testing"

	"micam/internal/testvec"
)

func TestSignedNonce(t *testing.T) {
	c := testvec.MustCloud(t)
	got, err := SignedNonce(c.Ssecurity, c.Nonce)
	if err != nil {
		t.Fatalf("SignedNonce: %v", err)
	}
	if got != c.SignedNonce {
		t.Fatalf("SignedNonce = %q, want %q", got, c.SignedNonce)
	}
}

func TestAPIPath(t *testing.T) {
	c := testvec.MustCloud(t)
	if got := APIPath(c.URL); got != c.APIPath {
		t.Fatalf("APIPath(%q) = %q, want %q", c.URL, got, c.APIPath)
	}
	for _, tc := range c.APIPaths {
		if got := APIPath(tc.URL); got != tc.Path {
			t.Fatalf("APIPath(%q) = %q, want %q", tc.URL, got, tc.Path)
		}
	}
}

func TestRC4(t *testing.T) {
	c := testvec.MustCloud(t)

	key, err := base64.StdEncoding.DecodeString(c.RC4.Key)
	if err != nil {
		t.Fatalf("vector key: %v", err)
	}
	if len(key) != 32 {
		t.Fatalf("rc4 key is %d bytes, want 32 (decode the base64, do not hash the text)", len(key))
	}

	cipher := base64.StdEncoding.EncodeToString(RC4(key, []byte(c.RC4.Plaintext)))
	if cipher != c.RC4.CipherB64 {
		t.Fatalf("RC4(hello) = %q, want %q", cipher, c.RC4.CipherB64)
	}

	raw, err := base64.StdEncoding.DecodeString(c.RC4.CipherB64)
	if err != nil {
		t.Fatalf("vector ciphertext: %v", err)
	}
	if got := string(RC4(key, raw)); got != c.RC4.Plaintext {
		t.Fatalf("RC4 decrypted to %q, want %q", got, c.RC4.Plaintext)
	}
}

func TestSignatureOverPlaintext(t *testing.T) {
	c := testvec.MustCloud(t)
	got := Signature(c.URL, c.Sig.Method, c.SignedNonce, []KV{{Key: "data", Value: c.Data}})
	if got != c.Sig.Expected {
		t.Fatalf("signature = %q, want %q", got, c.Sig.Expected)
	}
}

func TestEncryptParams(t *testing.T) {
	c := testvec.MustCloud(t)

	body, err := EncryptParams(c.URL, "POST", c.SignedNonce, c.Nonce, c.Ssecurity,
		[]KV{{Key: "data", Value: c.Data}})
	if err != nil {
		t.Fatalf("EncryptParams: %v", err)
	}

	keys := make([]string, 0, len(body))
	values := make(map[string]string, len(body))
	for _, kv := range body {
		keys = append(keys, kv.Key)
		values[kv.Key] = kv.Value
	}

	// Order is part of the contract: the signature is computed over the
	// parameters in sequence, so a reordered body does not verify.
	if !reflect.DeepEqual(keys, c.Body.Order) {
		t.Fatalf("key order = %v, want %v", keys, c.Body.Order)
	}

	if values["data"] != c.Body.Data {
		t.Fatalf("data = %q\nwant %q", values["data"], c.Body.Data)
	}
	if values["rc4_hash__"] != c.Body.RC4Hash {
		t.Fatalf("rc4_hash__ = %q, want %q", values["rc4_hash__"], c.Body.RC4Hash)
	}
	if values["signature"] != c.Body.Signature {
		t.Fatalf("signature = %q, want %q", values["signature"], c.Body.Signature)
	}
	if values["ssecurity"] != c.Ssecurity {
		t.Fatalf("ssecurity was altered in transit")
	}
	if values["_nonce"] != c.Nonce {
		t.Fatalf("_nonce was altered in transit")
	}
}

func TestNewNonce(t *testing.T) {
	c := testvec.MustCloud(t)

	nonce, err := NewNonce()
	if err != nil {
		t.Fatalf("NewNonce: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(nonce)
	if err != nil {
		t.Fatalf("nonce is not base64: %v", err)
	}
	if len(raw) != c.NonceLen {
		t.Fatalf("nonce is %d bytes, want %d", len(raw), c.NonceLen)
	}

	second, err := NewNonce()
	if err != nil {
		t.Fatalf("NewNonce: %v", err)
	}
	if nonce == second {
		t.Fatal("two nonces in the same minute must still differ")
	}
}

func TestRC4Deterministic(t *testing.T) {
	key := make([]byte, 32)
	first := hex.EncodeToString(RC4(key, []byte("same input")))
	second := hex.EncodeToString(RC4(key, []byte("same input")))
	if first != second {
		t.Fatal("RC4 must be deterministic; a fresh keystream would corrupt the stream")
	}
}
