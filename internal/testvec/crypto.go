package testvec

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

// CryptoCase mirrors the "crypto" group: RFC 7748 and NaCl core1 vectors.
type CryptoCase struct {
	X25519     X25519Case     `json:"x25519"`
	HSalsa20   HSalsa20Case   `json:"hsalsa20"`
	SessionKey SessionKeyCase `json:"session_key"`
}

type X25519Case struct {
	AlicePriv string `json:"alice_priv"`
	AlicePub  string `json:"alice_pub"`
	BobPriv   string `json:"bob_priv"`
	BobPub    string `json:"bob_pub"`
	Shared    string `json:"shared"`
	Source    string `json:"source"`
}

type HSalsa20Case struct {
	Input   string `json:"input"`
	Nonce16 string `json:"nonce16"`
	Core    string `json:"core"`
	Source  string `json:"source"`
}

type SessionKeyCase struct {
	Priv     string `json:"priv"`
	Pub      string `json:"pub"`
	Expected string `json:"expected"`
}

// MustCrypto loads the crypto vector group.
func MustCrypto(tb testing.TB) *CryptoCase {
	tb.Helper()
	c, err := Crypto()
	if err != nil {
		tb.Fatalf("testvec: %v", err)
	}
	return c
}

// Crypto reads and decodes the crypto group.
func Crypto() (*CryptoCase, error) {
	path, err := find("testdata/vectors.json")
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var doc struct {
		Crypto CryptoCase `json:"crypto"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if doc.Crypto.SessionKey.Expected == "" {
		return nil, fmt.Errorf("parse %s: crypto group is empty", path)
	}
	return &doc.Crypto, nil
}
