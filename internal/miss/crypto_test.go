package miss

import (
	"bytes"
	"encoding/hex"
	"testing"

	"micam/internal/testvec"
)

func mustKey(t *testing.T, h string) [KeyLen]byte {
	t.Helper()
	raw, err := hex.DecodeString(h)
	if err != nil {
		t.Fatalf("bad key fixture %q: %v", h, err)
	}
	if len(raw) != KeyLen {
		t.Fatalf("key fixture is %d bytes, want %d", len(raw), KeyLen)
	}
	var out [KeyLen]byte
	copy(out[:], raw)
	return out
}

func TestX25519Agreement(t *testing.T) {
	c := testvec.MustCrypto(t)
	x := c.X25519

	alice := mustKey(t, x.AlicePriv)
	bob := mustKey(t, x.BobPriv)

	fromAlice, err := SharedSecret(alice, x.BobPub)
	if err != nil {
		t.Fatalf("SharedSecret(alice, bob): %v", err)
	}
	if got := hex.EncodeToString(fromAlice[:]); got != x.Shared {
		t.Fatalf("alice->bob = %s, want %s (%s)", got, x.Shared, x.Source)
	}

	fromBob, err := SharedSecret(bob, x.AlicePub)
	if err != nil {
		t.Fatalf("SharedSecret(bob, alice): %v", err)
	}
	if fromAlice != fromBob {
		t.Fatal("both directions must agree")
	}
}

func TestHSalsa20CoreMatchesNaCl(t *testing.T) {
	c := testvec.MustCrypto(t)
	h := c.HSalsa20

	shared := mustKey(t, h.Input)
	rawNonce, err := hex.DecodeString(h.Nonce16)
	if err != nil {
		t.Fatalf("bad nonce fixture: %v", err)
	}
	var nonce [16]byte
	copy(nonce[:], rawNonce)

	got := HSalsa20Core(shared, nonce)
	if want := hex.EncodeToString(got[:]); want != h.Core {
		t.Fatalf("HSalsa20 core = %s, want %s (%s)", want, h.Core, h.Source)
	}
}

func TestSessionKeyIsCryptoBoxBeforenm(t *testing.T) {
	c := testvec.MustCrypto(t)
	sk := c.SessionKey

	priv := mustKey(t, sk.Priv)
	key, err := SessionKey(priv, sk.Pub)
	if err != nil {
		t.Fatalf("SessionKey: %v", err)
	}
	if got := hex.EncodeToString(key[:]); got != sk.Expected {
		t.Fatalf("session key = %s, want %s", got, sk.Expected)
	}

	// The trap this test exists to catch: a bare X25519 output is not the key,
	// and using it produces noise without any error anywhere.
	shared, err := SharedSecret(priv, sk.Pub)
	if err != nil {
		t.Fatalf("SharedSecret: %v", err)
	}
	if bytes.Equal(shared[:], key[:]) {
		t.Fatal("session key must differ from the raw agreement output")
	}
}

func TestGenerateKeysAgree(t *testing.T) {
	a, err := GenerateKeys()
	if err != nil {
		t.Fatalf("GenerateKeys: %v", err)
	}
	b, err := GenerateKeys()
	if err != nil {
		t.Fatalf("GenerateKeys: %v", err)
	}

	if a.Public == b.Public {
		t.Fatal("two key pairs must differ")
	}
	if len(a.PublicHex()) != 64 {
		t.Fatalf("public hex is %d chars, want 64", len(a.PublicHex()))
	}

	fromA, err := SharedSecret(a.Private, b.PublicHex())
	if err != nil {
		t.Fatalf("SharedSecret: %v", err)
	}
	fromB, err := SharedSecret(b.Private, a.PublicHex())
	if err != nil {
		t.Fatalf("SharedSecret: %v", err)
	}
	if fromA != fromB {
		t.Fatal("both sides must arrive at the same secret")
	}
}

func TestChaChaRoundTrip(t *testing.T) {
	var key [KeyLen]byte
	for i := range key {
		key[i] = byte(i)
	}
	plaintext := []byte(`{"videoquality":2,"enableaudio":1}`)

	sealed, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if len(sealed) != len(plaintext)+8 {
		t.Fatalf("sealed length = %d, want %d (the nonce is prefixed)", len(sealed), len(plaintext)+8)
	}

	opened, err := Decrypt(key, sealed)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(opened, plaintext) {
		t.Fatalf("round trip changed the payload: %q", opened)
	}

	again, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if bytes.Equal(sealed, again) {
		t.Fatal("each encryption must use a fresh nonce")
	}
}

func TestDecryptRejectsShortPayload(t *testing.T) {
	var key [KeyLen]byte
	if _, err := Decrypt(key, []byte{1, 2, 3}); err == nil {
		t.Fatal("a payload shorter than the nonce must be rejected")
	}
}
