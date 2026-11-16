// Package miss implements the media session that rides on top of a PPPP relay
// connection: key agreement, command framing and media decryption.
package miss

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"golang.org/x/crypto/chacha20"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/salsa20/salsa"
)

const (
	// KeyLen is the size of every X25519 key and of the session key.
	KeyLen = 32
	// nonceTailLen is how many bytes of the media nonce travel in front of the
	// ciphertext; the other four bytes of the 12-byte ChaCha20 nonce are zero.
	nonceTailLen = 8
)

// Keys is one X25519 key pair in the wire format the cloud expects.
type Keys struct {
	Public  [KeyLen]byte
	Private [KeyLen]byte
}

// PublicHex renders the public half as the cloud wants it.
func (k Keys) PublicHex() string { return hex.EncodeToString(k.Public[:]) }

// GenerateKeys creates a fresh key pair. It must be generated before asking the
// cloud for provisioning: the public half is the app_pubkey parameter.
func GenerateKeys() (Keys, error) {
	var k Keys
	if _, err := rand.Read(k.Private[:]); err != nil {
		return Keys{}, fmt.Errorf("miss: cannot generate a key pair: %w", err)
	}
	pub, err := curve25519.X25519(k.Private[:], curve25519.Basepoint)
	if err != nil {
		return Keys{}, fmt.Errorf("miss: cannot derive the public key: %w", err)
	}
	copy(k.Public[:], pub)
	return k, nil
}

// SharedSecret is the raw X25519 output. It is NOT the session key.
func SharedSecret(priv [KeyLen]byte, peerPubHex string) ([KeyLen]byte, error) {
	peer, err := decodeKey(peerPubHex)
	if err != nil {
		return [KeyLen]byte{}, err
	}
	raw, err := curve25519.X25519(priv[:], peer[:])
	if err != nil {
		return [KeyLen]byte{}, fmt.Errorf("miss: X25519 agreement failed: %w", err)
	}
	var out [KeyLen]byte
	copy(out[:], raw)
	return out, nil
}

// HSalsa20Core is NaCl's crypto_core_hsalsa20.
func HSalsa20Core(shared [KeyLen]byte, nonce [16]byte) [KeyLen]byte {
	var out [KeyLen]byte
	var sigma [16]byte
	copy(sigma[:], "expand 32-byte k")
	salsa.HSalsa20(&out, &nonce, &shared, &sigma)
	return out
}

// SessionKey is NaCl's crypto_box_beforenm: X25519 followed by HSalsa20 with a
// zero nonce.
//
// Skipping the HSalsa20 step yields a key that fails silently: frames still
// arrive, lengths look right, and the payload is noise.
func SessionKey(priv [KeyLen]byte, peerPubHex string) ([KeyLen]byte, error) {
	shared, err := SharedSecret(priv, peerPubHex)
	if err != nil {
		return [KeyLen]byte{}, err
	}
	var zero [16]byte
	return HSalsa20Core(shared, zero), nil
}

// Decrypt opens one media payload. The first 8 bytes are the nonce tail; the
// 12-byte nonce is four zero bytes followed by those eight. The ChaCha20 block
// counter starts at zero.
func Decrypt(key [KeyLen]byte, payload []byte) ([]byte, error) {
	if len(payload) < nonceTailLen {
		return nil, fmt.Errorf("miss: payload too short to carry a nonce")
	}
	var nonce [chacha20.NonceSize]byte
	copy(nonce[4:], payload[:nonceTailLen])

	stream, err := chacha20.NewUnauthenticatedCipher(key[:], nonce[:])
	if err != nil {
		return nil, fmt.Errorf("miss: cannot open ChaCha20: %w", err)
	}
	out := make([]byte, len(payload)-nonceTailLen)
	stream.XORKeyStream(out, payload[nonceTailLen:])
	return out, nil
}

// Encrypt seals one payload, prefixing a fresh nonce tail.
func Encrypt(key [KeyLen]byte, plaintext []byte) ([]byte, error) {
	tail := make([]byte, nonceTailLen)
	if _, err := rand.Read(tail); err != nil {
		return nil, fmt.Errorf("miss: cannot generate a nonce: %w", err)
	}
	var nonce [chacha20.NonceSize]byte
	copy(nonce[4:], tail)

	stream, err := chacha20.NewUnauthenticatedCipher(key[:], nonce[:])
	if err != nil {
		return nil, fmt.Errorf("miss: cannot open ChaCha20: %w", err)
	}
	out := make([]byte, nonceTailLen+len(plaintext))
	copy(out, tail)
	stream.XORKeyStream(out[nonceTailLen:], plaintext)
	return out, nil
}

func decodeKey(hexKey string) ([KeyLen]byte, error) {
	var out [KeyLen]byte
	raw, err := hex.DecodeString(hexKey)
	if err != nil {
		return out, fmt.Errorf("miss: key is not hex: %w", err)
	}
	if len(raw) != KeyLen {
		return out, fmt.Errorf("miss: key is %d bytes, want %d", len(raw), KeyLen)
	}
	copy(out[:], raw)
	return out, nil
}
