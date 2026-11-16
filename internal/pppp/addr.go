package pppp

import (
	"encoding/binary"
	"fmt"
	"net/netip"
)

// AddrLen is the on-wire size of the PPPP address block.
const AddrLen = 16

// Addr is an IPv4 endpoint as PPPP spells it.
//
// The block is a sockaddr_in written by a little-endian host and never
// byte-swapped back: the family is big-endian, the port is little-endian, and
// the address is byte-reversed. Reading it as a normal sockaddr yields
// plausible but wrong addresses. See docs/PROTOCOL.md §8.3.
type Addr struct {
	IP   netip.Addr
	Port uint16
}

// NewAddr builds an Addr from text, rejecting anything that is not IPv4.
func NewAddr(ip string, port uint16) (Addr, error) {
	parsed, err := netip.ParseAddr(ip)
	if err != nil {
		return Addr{}, fmt.Errorf("pppp: bad IPv4 address %q: %w", ip, err)
	}
	if !parsed.Is4() {
		return Addr{}, fmt.Errorf("pppp: %q is not IPv4", ip)
	}
	return Addr{IP: parsed, Port: port}, nil
}

// MustAddr is NewAddr for constants and tests.
func MustAddr(ip string, port uint16) Addr {
	a, err := NewAddr(ip, port)
	if err != nil {
		panic(err)
	}
	return a
}

// Encode returns the 16-byte block.
func (a Addr) Encode() [AddrLen]byte {
	var out [AddrLen]byte
	out[0] = 0x00
	out[1] = 0x02 // AF_INET, big-endian
	out[2] = byte(a.Port)
	out[3] = byte(a.Port >> 8) // port, little-endian
	ip := a.IP.As4()
	out[4] = ip[3] // address, byte-reversed
	out[5] = ip[2]
	out[6] = ip[1]
	out[7] = ip[0]
	return out
}

// DecodeAddr reads a 16-byte block. A short buffer yields the zero Addr.
func DecodeAddr(b []byte) Addr {
	if len(b) < AddrLen {
		return Addr{}
	}
	port := binary.LittleEndian.Uint16(b[2:4])
	ip := netip.AddrFrom4([4]byte{b[7], b[6], b[5], b[4]})
	return Addr{IP: ip, Port: port}
}

// Compact returns the 8-byte form used inside the 0x28 reply. Unlike the
// 16-byte block, everything here is big-endian and the address is not
// reversed. See docs/PROTOCOL.md §9.
func (a Addr) Compact() [8]byte {
	var out [8]byte
	out[0] = 0x02
	out[1] = 0x00
	binary.BigEndian.PutUint16(out[2:4], a.Port)
	ip := a.IP.As4()
	copy(out[4:8], ip[:])
	return out
}

func (a Addr) String() string {
	if !a.IP.IsValid() {
		return "<invalid>"
	}
	return fmt.Sprintf("%s:%d", a.IP, a.Port)
}
