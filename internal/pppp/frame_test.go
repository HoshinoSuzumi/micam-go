package pppp

import (
	"encoding/hex"
	"testing"

	"micam/internal/testvec"
)

func TestFrameEncode(t *testing.T) {
	v := testvec.MustLoad(t)
	for _, c := range v.Frame.Encode {
		t.Run(c.ID, func(t *testing.T) {
			payload := mustHex(t, c.PayloadHex)
			got := EncodeFrame(c.Type, payload)
			if hex.EncodeToString(got) != c.Hex {
				t.Fatalf("encode = %s, want %s", hex.EncodeToString(got), c.Hex)
			}

			parsed, ok := ParseFrame(mustHex(t, c.Hex))
			if !ok {
				t.Fatalf("ParseFrame rejected our own encoding")
			}
			if parsed.Type != c.Type {
				t.Fatalf("parsed type = 0x%02x, want 0x%02x", parsed.Type, c.Type)
			}
			if got := hex.EncodeToString(parsed.Payload); got != c.PayloadHex {
				t.Fatalf("parsed payload = %s, want %s", got, c.PayloadHex)
			}
			if name := MsgName(parsed.Type); name != c.TypeName {
				t.Fatalf("MsgName = %s, want %s", name, c.TypeName)
			}
		})
	}
}

func TestFrameDecode(t *testing.T) {
	v := testvec.MustLoad(t)
	for _, c := range v.Frame.Decode {
		t.Run(c.ID, func(t *testing.T) {
			parsed, ok := ParseFrame(mustHex(t, c.Hex))
			if !ok {
				t.Fatalf("ParseFrame rejected a valid frame")
			}
			if parsed.Type != c.Type {
				t.Fatalf("parsed type = 0x%02x, want 0x%02x", parsed.Type, c.Type)
			}
			if got := hex.EncodeToString(parsed.Payload); got != c.PayloadHex {
				t.Fatalf("parsed payload = %s, want %s", got, c.PayloadHex)
			}
			if name := MsgName(parsed.Type); name != c.TypeName {
				t.Fatalf("MsgName = %s, want %s", name, c.TypeName)
			}
			if got := uidFromPayload(parsed.Payload); got != "ABCDEF-123456-GHIJK" {
				t.Fatalf("UID payload decoded to %q", got)
			}
		})
	}
}

func TestFrameReject(t *testing.T) {
	v := testvec.MustLoad(t)
	for _, c := range v.Frame.Reject {
		t.Run(c.ID, func(t *testing.T) {
			if _, ok := ParseFrame(mustHex(t, c.Hex)); ok {
				t.Fatalf("ParseFrame accepted an invalid frame (%s)", c.Reason)
			}
		})
	}
}

// uidFromPayload is a test-only convenience for the RLY_RDY fixture, whose
// payload is a bare wire UID.
func uidFromPayload(payload []byte) string {
	return DecodeUID(payload).String()
}
