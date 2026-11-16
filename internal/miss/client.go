package miss

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MISS command identifiers; see docs/PROTOCOL.md §11.2.
const (
	CmdAuthReq    uint32 = 0x100
	CmdAuthRes    uint32 = 0x101
	CmdVideoStart uint32 = 0x102
	CmdVideoStop  uint32 = 0x103
	CmdAudioStart uint32 = 0x104
	CmdAudioStop  uint32 = 0x105
	CmdStreamCtrl uint32 = 0x109
	CmdPlayback   uint32 = 0x10D
	CmdDeviceInfo uint32 = 0x110
	// CmdEncoded wraps any command sent after authentication.
	CmdEncoded uint32 = 0x1001
)

// supportEncrypt is what the vendor app advertises. go2rtc sends 0 and still
// receives encrypted media, so it appears to be advisory.
const supportEncrypt = 15

// Transport is what a MISS session needs from the link below it. pppp.Conn
// implements it; tests substitute a fake.
type Transport interface {
	SendCommand(cmd uint32, body []byte) error
	NextCommand(ctx context.Context) ([]byte, bool)
	NextMedia(ctx context.Context) ([]byte, bool)
}

// Client drives one MISS session.
type Client struct {
	transport Transport
	key       [KeyLen]byte
	hasKey    bool
}

// NewClient wraps a transport.
func NewClient(t Transport) *Client { return &Client{transport: t} }

// UseSessionKey installs the key derived from the cloud key exchange. Required
// before any media or post-authentication command, which are all encrypted.
func (c *Client) UseSessionKey(key [KeyLen]byte) {
	c.key = key
	c.hasKey = true
}

// Authenticate runs the plaintext authentication handshake.
//
// The response text contains "result":"success" on success. UDP loses packets,
// so a silent 2s window means retry rather than failure.
func (c *Client) Authenticate(ctx context.Context, publicHex, signHex string, timeout time.Duration) (string, error) {
	uuid, err := newUUID()
	if err != nil {
		return "", err
	}

	// The spacing is significant: the camera validates the signature over these
	// exact bytes, and the reference implementation sends a space after the
	// first comma and none after the second.
	payload := []byte(fmt.Sprintf(
		`{"public_key":"%s", "sign":"%s","uuid":"%s","support_encrypt":%d}`,
		publicHex, signHex, uuid, supportEncrypt))

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := c.transport.SendCommand(CmdAuthReq, payload); err != nil {
			return "", err
		}

		waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		message, ok := c.transport.NextCommand(waitCtx)
		cancel()
		if !ok || len(message) < 4 {
			continue
		}
		if cmd := binary.BigEndian.Uint32(message[:4]); cmd != CmdAuthRes {
			continue
		}

		text := string(message[4:])
		if !strings.Contains(text, `"result":"success"`) {
			return "", fmt.Errorf("miss: camera rejected authentication: %s", text)
		}
		return text, nil
	}
	return "", fmt.Errorf("miss: no authentication response within %s", timeout)
}

// StartVideo asks the camera to start sending video.
//
// quality: 0 auto, 1 SD, 2 HD, 3 full.
func (c *Client) StartVideo(quality int, audio bool) error {
	enable := 0
	if audio {
		enable = 1
	}
	body := fmt.Sprintf(`{"videoquality":%d,"enableaudio":%d}`, quality, enable)
	return c.sendEncrypted(CmdVideoStart, body)
}

// StopVideo tells the camera to stop before the socket disappears, otherwise it
// keeps sending into a dead relay session for a while.
func (c *Client) StopVideo() error {
	return c.sendEncrypted(CmdVideoStop, "")
}

// NextMedia returns the next decrypted frame. The boolean is false when the
// context expires or the transport closes.
func (c *Client) NextMedia(ctx context.Context) (Packet, bool, error) {
	message, ok := c.transport.NextMedia(ctx)
	if !ok {
		return Packet{}, false, nil
	}
	packet, err := ParsePacket(message, c.key)
	if err != nil {
		return Packet{}, false, err
	}
	return packet, true, nil
}

// sendEncrypted wraps a command in the 0x1001 envelope.
func (c *Client) sendEncrypted(cmd uint32, body string) error {
	if !c.hasKey {
		return errors.New("miss: session key not set")
	}

	inner := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(inner[:4], cmd)
	copy(inner[4:], body)

	sealed, err := Encrypt(c.key, inner)
	if err != nil {
		return err
	}
	return c.transport.SendCommand(CmdEncoded, sealed)
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("miss: cannot generate a uuid: %w", err)
	}
	b[6] = (b[6] & 0x0F) | 0x40 // version 4
	b[8] = (b[8] & 0x3F) | 0x80 // variant
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]),
	), nil
}
