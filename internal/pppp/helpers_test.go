package pppp

import (
	"encoding/hex"
	"testing"
)

// mustHex decodes a hex fixture, failing the test rather than returning an
// error the caller would have to handle.
func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex fixture %q: %v", s, err)
	}
	return b
}
