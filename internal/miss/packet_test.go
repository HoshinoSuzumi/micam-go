package miss

import (
	"encoding/binary"
	"testing"
)

func TestParsePacket(t *testing.T) {
	var key [KeyLen]byte
	for i := range key {
		key[i] = byte(i * 3)
	}

	annexB := []byte{0x00, 0x00, 0x00, 0x01, 0x40, 0x01, 0x0c}
	sealed, err := Encrypt(key, annexB)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	message := make([]byte, HeaderLen+len(sealed))
	binary.LittleEndian.PutUint32(message[0:4], uint32(len(sealed)))
	binary.LittleEndian.PutUint32(message[4:8], CodecH265)
	binary.LittleEndian.PutUint32(message[8:12], 42)
	binary.LittleEndian.PutUint32(message[12:16], 1) // keyframe
	binary.LittleEndian.PutUint64(message[16:24], 100145)
	copy(message[HeaderLen:], sealed)

	p, err := ParsePacket(message, key)
	if err != nil {
		t.Fatalf("ParsePacket: %v", err)
	}
	if p.Codec != CodecH265 {
		t.Fatalf("codec = %d, want %d", p.Codec, CodecH265)
	}
	if p.Seq != 42 {
		t.Fatalf("seq = %d, want 42", p.Seq)
	}
	if !p.Keyframe() {
		t.Fatal("flag bit 0 must mean keyframe")
	}
	if !p.IsVideo() {
		t.Fatal("H.265 must count as video")
	}
	if p.TimestampMS != 100145 {
		t.Fatalf("timestamp = %d, want 100145", p.TimestampMS)
	}
	if got := string(p.Payload); got != string(annexB) {
		t.Fatalf("payload = %x, want %x", p.Payload, annexB)
	}
	if name := p.CodecName(); name != "H265" {
		t.Fatalf("codec name = %s, want H265", name)
	}
}

func TestParsePacketRejectsShortMessage(t *testing.T) {
	var key [KeyLen]byte
	if _, err := ParsePacket(make([]byte, HeaderLen-1), key); err == nil {
		t.Fatal("a message shorter than the header must be rejected")
	}
}

func TestPacketClassification(t *testing.T) {
	if (Packet{Codec: CodecPCMA}).IsVideo() {
		t.Fatal("audio is not video")
	}
	if (Packet{Codec: CodecH264}).Keyframe() {
		t.Fatal("flag 0 is not a keyframe")
	}
	if got := (Packet{Codec: 999}).CodecName(); got != "codec-999" {
		t.Fatalf("unknown codec name = %s", got)
	}
}
