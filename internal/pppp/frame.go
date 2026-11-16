package pppp

import "encoding/binary"

// Frame is one PPPP datagram: F1 | type | len:u16 BE | payload.
//
// The length covers the payload only. Packets are plaintext: the protocol
// family supports a PSK obfuscation layer, but the Xiaomi deployment does not
// use it.
const (
	FrameMagic     byte = 0xF1
	FrameHeaderLen      = 4
)

// Frame is a parsed PPPP datagram.
type Frame struct {
	Type    byte
	Payload []byte
}

// EncodeFrame serialises one datagram.
func EncodeFrame(t byte, payload []byte) []byte {
	out := make([]byte, FrameHeaderLen+len(payload))
	out[0] = FrameMagic
	out[1] = t
	binary.BigEndian.PutUint16(out[2:4], uint16(len(payload)))
	copy(out[FrameHeaderLen:], payload)
	return out
}

// Encode serialises the frame.
func (f Frame) Encode() []byte {
	return EncodeFrame(f.Type, f.Payload)
}

// ParseFrame parses a datagram. The second result is false when the buffer is
// not a PPPP frame at all (too short, or the wrong magic).
//
// A declared length longer than what arrived is truncated rather than
// rejected: UDP truncation and trailing padding both happen in the wild.
func ParseFrame(buf []byte) (Frame, bool) {
	if len(buf) < FrameHeaderLen || buf[0] != FrameMagic {
		return Frame{}, false
	}
	declared := int(binary.BigEndian.Uint16(buf[2:4]))
	avail := declared
	if limit := len(buf) - FrameHeaderLen; avail > limit {
		avail = limit
	}
	payload := make([]byte, avail)
	copy(payload, buf[FrameHeaderLen:FrameHeaderLen+avail])
	return Frame{Type: buf[1], Payload: payload}, true
}

// Name renders the message type for logs.
func (f Frame) Name() string {
	return MsgName(f.Type)
}
