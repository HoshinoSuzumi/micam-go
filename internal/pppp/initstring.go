package pppp

import (
	"encoding/hex"
	"fmt"
	"net/netip"
	"strings"
)

// initLUT is the 54-byte lookup table from PPPP_DecodeString. Every byte of it
// matters; a single wrong entry turns the rendezvous IP list into garbage.
var initLUT = mustLUT(
	"4959433db5bf6da347534f6165e371e9677f02030badb3892b2f35c16b8b95" +
		"9711e5a70deff1050783fb9d3bc5c713171d1f2529d3df",
)

func mustLUT(s string) [54]byte {
	raw, err := hex.DecodeString(s)
	if err != nil {
		panic("pppp: init LUT is not valid hex: " + err.Error())
	}
	if len(raw) != 54 {
		panic(fmt.Sprintf("pppp: init LUT must be 54 bytes, got %d", len(raw)))
	}
	var out [54]byte
	copy(out[:], raw)
	return out
}

// DecodeInitString decodes the obfuscated rendezvous server list.
//
// The encoding is a nibble alphabet A-P; each output byte is XORed with the
// fixed table and with the running XOR of every previous output byte, with an
// initial value of 0x39. Anything after ':' is an optional secret key and is
// ignored. The result keeps its trailing comma - the original C parser
// requires it, so it is not noise.
func DecodeInitString(enc string) (string, error) {
	if i := strings.IndexByte(enc, ':'); i >= 0 {
		enc = enc[:i]
	}
	if len(enc) == 0 {
		return "", fmt.Errorf("pppp: empty init string")
	}
	if len(enc)%2 != 0 {
		return "", fmt.Errorf("pppp: init string has odd length %d", len(enc))
	}

	out := make([]byte, len(enc)/2)
	for i := range out {
		running := byte(0x39)
		for j := 0; j < i; j++ {
			running ^= out[j]
		}
		hi, err := nibble(enc[2*i])
		if err != nil {
			return "", err
		}
		lo, err := nibble(enc[2*i+1])
		if err != nil {
			return "", err
		}
		out[i] = running ^ initLUT[i%len(initLUT)] ^ byte(hi*16+lo)
	}
	return string(out), nil
}

func nibble(c byte) (int, error) {
	if c < 'A' || c > 'P' {
		return 0, fmt.Errorf("pppp: init string character %q is outside A-P", c)
	}
	return int(c - 'A'), nil
}

// Servers decodes an init string into the rendezvous addresses.
func Servers(enc string) ([]netip.Addr, error) {
	raw, err := DecodeInitString(enc)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(raw, ",")
	out := make([]netip.Addr, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		ip, err := netip.ParseAddr(part)
		if err != nil {
			return nil, fmt.Errorf("pppp: bad rendezvous address %q: %w", part, err)
		}
		out = append(out, ip)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("pppp: init string decoded to no servers")
	}
	return out, nil
}
