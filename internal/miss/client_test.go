package miss

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"
	"time"
)

// fakeTransport records what the client sends and replays queued responses.
type fakeTransport struct {
	sent     [][]byte
	commands [][]byte
	media    [][]byte
}

func (f *fakeTransport) SendCommand(cmd uint32, body []byte) error {
	msg := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(msg[:4], cmd)
	copy(msg[4:], body)
	f.sent = append(f.sent, msg)
	return nil
}

func (f *fakeTransport) NextCommand(context.Context) ([]byte, bool) {
	if len(f.commands) == 0 {
		return nil, false
	}
	msg := f.commands[0]
	f.commands = f.commands[1:]
	return msg, true
}

func (f *fakeTransport) NextMedia(context.Context) ([]byte, bool) {
	if len(f.media) == 0 {
		return nil, false
	}
	msg := f.media[0]
	f.media = f.media[1:]
	return msg, true
}

func authResponse(body string) []byte {
	msg := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(msg[:4], CmdAuthRes)
	copy(msg[4:], body)
	return msg
}

func TestAuthenticateSuccess(t *testing.T) {
	transport := &fakeTransport{
		commands: [][]byte{authResponse(`{"result":"success", "channel": "0000000000000"}`)},
	}
	client := NewClient(transport)

	text, err := client.Authenticate(context.Background(), "aabbcc", "ddeeff", time.Second)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if !strings.Contains(text, `"result":"success"`) {
		t.Fatalf("response = %q", text)
	}

	if len(transport.sent) != 1 {
		t.Fatalf("sent %d commands, want 1", len(transport.sent))
	}
	if cmd := binary.BigEndian.Uint32(transport.sent[0][:4]); cmd != CmdAuthReq {
		t.Fatalf("command = 0x%x, want 0x%x", cmd, CmdAuthReq)
	}
	payload := string(transport.sent[0][4:])
	if !strings.HasPrefix(payload, `{"public_key":"aabbcc", "sign":"ddeeff","uuid":"`) {
		t.Fatalf("auth payload spacing is part of the protocol: %q", payload)
	}
	if !strings.Contains(payload, `"support_encrypt":15`) {
		t.Fatalf("support_encrypt must be 15: %q", payload)
	}
}

func TestAuthenticateRejected(t *testing.T) {
	transport := &fakeTransport{
		commands: [][]byte{authResponse(`{"result":"fail"}`)},
	}
	client := NewClient(transport)

	_, err := client.Authenticate(context.Background(), "aa", "bb", time.Second)
	if err == nil {
		t.Fatal("a rejected authentication must surface as an error")
	}
	if !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStartVideoIsEncrypted(t *testing.T) {
	transport := &fakeTransport{}
	client := NewClient(transport)

	var key [KeyLen]byte
	for i := range key {
		key[i] = byte(i)
	}
	client.UseSessionKey(key)

	if err := client.StartVideo(2, false); err != nil {
		t.Fatalf("StartVideo: %v", err)
	}
	if len(transport.sent) != 1 {
		t.Fatalf("sent %d commands, want 1", len(transport.sent))
	}

	envelope := transport.sent[0]
	if cmd := binary.BigEndian.Uint32(envelope[:4]); cmd != CmdEncoded {
		t.Fatalf("envelope command = 0x%x, want 0x%x", cmd, CmdEncoded)
	}

	inner, err := Decrypt(key, envelope[4:])
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if cmd := binary.BigEndian.Uint32(inner[:4]); cmd != CmdVideoStart {
		t.Fatalf("inner command = 0x%x, want 0x%x", cmd, CmdVideoStart)
	}
	if body := string(inner[4:]); body != `{"videoquality":2,"enableaudio":0}` {
		t.Fatalf("body = %q", body)
	}
}

func TestCommandsRequireASessionKey(t *testing.T) {
	client := NewClient(&fakeTransport{})
	if err := client.StartVideo(2, false); err == nil {
		t.Fatal("sending an encrypted command without a key must fail")
	}
}

func TestNextMediaDecodes(t *testing.T) {
	var key [KeyLen]byte
	sealed, err := Encrypt(key, []byte{0x00, 0x00, 0x01, 0x02})
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	message := make([]byte, HeaderLen+len(sealed))
	binary.LittleEndian.PutUint32(message[4:8], CodecH264)
	binary.LittleEndian.PutUint32(message[8:12], 7)
	binary.LittleEndian.PutUint32(message[12:16], 1)
	binary.LittleEndian.PutUint64(message[16:24], 5000)
	copy(message[HeaderLen:], sealed)

	transport := &fakeTransport{media: [][]byte{message}}
	client := NewClient(transport)
	client.UseSessionKey(key)

	packet, ok, err := client.NextMedia(context.Background())
	if err != nil {
		t.Fatalf("NextMedia: %v", err)
	}
	if !ok {
		t.Fatal("a queued frame must be returned")
	}
	if packet.Codec != CodecH264 || packet.Seq != 7 || !packet.Keyframe() {
		t.Fatalf("decoded packet = %+v", packet)
	}
	if got := string(packet.Payload); got != "\x00\x00\x01\x02" {
		t.Fatalf("payload = %q", got)
	}
}

func TestNewUUIDShape(t *testing.T) {
	uuid, err := newUUID()
	if err != nil {
		t.Fatalf("newUUID: %v", err)
	}
	if len(uuid) != 36 {
		t.Fatalf("uuid = %q (len %d), want 36 characters", uuid, len(uuid))
	}
	if uuid[14] != '4' {
		t.Fatalf("uuid = %q, want a version 4 uuid", uuid)
	}
}
