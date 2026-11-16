package pppp

import (
	"encoding/hex"
	"testing"

	"micam/internal/testvec"
)

func TestUIDRoundTrip(t *testing.T) {
	v := testvec.MustLoad(t)
	for _, c := range v.UID {
		t.Run(c.ID, func(t *testing.T) {
			u, err := ParseUID(c.Text)
			if err != nil {
				t.Fatalf("ParseUID: %v", err)
			}
			if u.Prefix != c.Prefix || u.Serial != c.Serial || u.Check != c.Check {
				t.Fatalf("parsed %+v, want prefix=%q serial=%d check=%q",
					u, c.Prefix, c.Serial, c.Check)
			}

			enc := u.Encode()
			if got := hex.EncodeToString(enc[:]); got != c.Hex {
				t.Fatalf("encode = %s, want %s", got, c.Hex)
			}
			if len(enc) != c.Len {
				t.Fatalf("encoded length = %d, want %d", len(enc), c.Len)
			}

			raw := mustHex(t, c.Hex)
			if got := DecodeUID(raw).String(); got != c.Text {
				t.Fatalf("decode = %s, want %s", got, c.Text)
			}
		})
	}
}

func TestParseUIDRejectsMalformed(t *testing.T) {
	for _, s := range []string{"", "ABCDEF", "ABCDEF-123456", "ABCDEF-notanumber-GHIJK"} {
		if _, err := ParseUID(s); err == nil {
			t.Fatalf("ParseUID(%q) should have failed", s)
		}
	}
}
