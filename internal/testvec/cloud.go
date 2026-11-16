package testvec

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

// CloudCase mirrors the "cloud" group of testdata/vectors.json: vectors for the
// signed Mi cloud API, produced by an independent reference implementation and
// validated against the live service.
type CloudCase struct {
	Ssecurity   string        `json:"ssecurity"`
	Nonce       string        `json:"nonce"`
	SignedNonce string        `json:"signed_nonce"`
	URL         string        `json:"url"`
	APIPath     string        `json:"api_path"`
	APIPaths    []APIPathCase `json:"api_paths"`
	Data        string        `json:"data"`
	RC4         RC4Case       `json:"rc4"`
	Sig         SigCase       `json:"signature_plaintext"`
	Body        BodyCase      `json:"encrypted_body"`
	NonceLen    int           `json:"nonce_len"`
}

type APIPathCase struct {
	URL  string `json:"url"`
	Path string `json:"path"`
}

type RC4Case struct {
	Key       string `json:"key"`
	Plaintext string `json:"plaintext"`
	CipherB64 string `json:"cipher_b64"`
}

type SigCase struct {
	Method      string   `json:"method"`
	ParamsOrder []string `json:"params_order"`
	Expected    string   `json:"expected"`
}

type BodyCase struct {
	Order     []string `json:"order"`
	Data      string   `json:"data"`
	RC4Hash   string   `json:"rc4_hash__"`
	Signature string   `json:"signature"`
	Ssecurity string   `json:"ssecurity"`
	Nonce     string   `json:"_nonce"`
}

// MustCloud loads the cloud vector group.
func MustCloud(tb testing.TB) *CloudCase {
	tb.Helper()
	c, err := Cloud()
	if err != nil {
		tb.Fatalf("testvec: %v", err)
	}
	return c
}

// Cloud reads and decodes the cloud group on every call; it is small and only
// used by tests that need it.
func Cloud() (*CloudCase, error) {
	path, err := find("testdata/vectors.json")
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var doc struct {
		Cloud CloudCase `json:"cloud"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if doc.Cloud.SignedNonce == "" {
		return nil, fmt.Errorf("parse %s: cloud group is empty", path)
	}
	return &doc.Cloud, nil
}
