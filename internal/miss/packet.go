package miss

import (
	"encoding/binary"
	"fmt"
)

// HeaderLen is the size of the plaintext media header in front of every
// channel-2 message.
const HeaderLen = 32

// Codec identifiers reported in the media header.
const (
	CodecH264 uint32 = 4
	CodecH265 uint32 = 5
	CodecPCMA uint32 = 1027
	CodecOpus uint32 = 1032
)

// Packet is one decrypted media frame.
//
// The 32-byte header is plaintext and little-endian, unlike the surrounding
// PPPP framing; that inconsistency is a common source of silent corruption.
type Packet struct {
	Codec       uint32
	Seq         uint32
	Flags       uint32
	TimestampMS int64
	Payload     []byte
}

// Keyframe reports the flag bit the camera sets on a keyframe.
func (p Packet) Keyframe() bool { return p.Flags&1 != 0 }

// IsVideo reports whether the frame carries video.
func (p Packet) IsVideo() bool {
	return p.Codec == CodecH264 || p.Codec == CodecH265
}

// IsAudio reports whether the frame carries sound.
func (p Packet) IsAudio() bool {
	return p.Codec == CodecPCMA || p.Codec == CodecOpus
}

// CodecName renders the codec for logs.
func (p Packet) CodecName() string {
	switch p.Codec {
	case CodecH264:
		return "H264"
	case CodecH265:
		return "H265"
	case CodecPCMA:
		return "PCMA"
	case CodecOpus:
		return "OPUS"
	}
	return fmt.Sprintf("codec-%d", p.Codec)
}

// ParsePacket reads and decrypts one channel-2 message.
func ParsePacket(message []byte, key [KeyLen]byte) (Packet, error) {
	if len(message) < HeaderLen {
		return Packet{}, fmt.Errorf("miss: media message is shorter than its header")
	}

	p := Packet{
		Codec:       binary.LittleEndian.Uint32(message[4:8]),
		Seq:         binary.LittleEndian.Uint32(message[8:12]),
		Flags:       binary.LittleEndian.Uint32(message[12:16]),
		TimestampMS: int64(binary.LittleEndian.Uint64(message[16:24])),
	}

	payload, err := Decrypt(key, message[HeaderLen:])
	if err != nil {
		return Packet{}, err
	}
	p.Payload = payload
	return p, nil
}
