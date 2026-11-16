package pppp

import (
	"encoding/hex"
	"testing"

	"micam/internal/testvec"
)

func TestAddrRoundTrip(t *testing.T) {
	v := testvec.MustLoad(t)
	for _, c := range v.Addr {
		t.Run(c.ID, func(t *testing.T) {
			a, err := NewAddr(c.IP, c.Port)
			if err != nil {
				t.Fatalf("NewAddr: %v", err)
			}

			enc := a.Encode()
			if got := hex.EncodeToString(enc[:]); got != c.Hex {
				t.Fatalf("encode = %s, want %s", got, c.Hex)
			}
			if len(enc) != AddrLen {
				t.Fatalf("encoded length = %d, want %d", len(enc), AddrLen)
			}

			decoded := DecodeAddr(mustHex(t, c.Hex))
			if got := decoded.IP.String(); got != c.IP {
				t.Fatalf("decoded ip = %s, want %s", got, c.IP)
			}
			if decoded.Port != c.Port {
				t.Fatalf("decoded port = %d, want %d", decoded.Port, c.Port)
			}
		})
	}
}

func TestAddrRejectsIPv6(t *testing.T) {
	if _, err := NewAddr("::1", 1); err == nil {
		t.Fatal("NewAddr should reject IPv6")
	}
}
