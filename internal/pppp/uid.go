package pppp

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
)

// UIDLen is the on-wire size of a camera's P2P identifier.
const UIDLen = 20

// UID is a camera's P2P identifier, e.g. ABCDEF-123456-GHIJK.
//
// On the wire it is 20 bytes: an 8-byte NUL-padded prefix, the serial as a
// big-endian uint32, then an 8-byte NUL-padded check value. It is not a UUID,
// and the serial is not ASCII.
type UID struct {
	Prefix string
	Serial uint32
	Check  string
}

// ParseUID parses the textual form PREFIX-SERIAL-CHECK.
func ParseUID(s string) (UID, error) {
	parts := strings.Split(strings.TrimSpace(s), "-")
	if len(parts) != 3 {
		return UID{}, fmt.Errorf("pppp: UID must be PREFIX-SERIAL-CHECK, got %q", s)
	}
	serial, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil {
		return UID{}, fmt.Errorf("pppp: bad UID serial %q: %w", parts[1], err)
	}
	return UID{Prefix: parts[0], Serial: uint32(serial), Check: parts[2]}, nil
}

// Encode returns the 20-byte wire form. Over-long fields are truncated, as the
// reference implementation does.
func (u UID) Encode() [UIDLen]byte {
	var out [UIDLen]byte
	copy(out[0:8], u.Prefix)
	binary.BigEndian.PutUint32(out[8:12], u.Serial)
	copy(out[12:20], u.Check)
	return out
}

// DecodeUID reads a 20-byte wire UID. A short buffer yields the zero UID.
func DecodeUID(b []byte) UID {
	if len(b) < UIDLen {
		return UID{}
	}
	return UID{
		Prefix: trimNUL(b[0:8]),
		Serial: binary.BigEndian.Uint32(b[8:12]),
		Check:  trimNUL(b[12:20]),
	}
}

func (u UID) String() string {
	return fmt.Sprintf("%s-%d-%s", u.Prefix, u.Serial, u.Check)
}

func trimNUL(b []byte) string {
	i := 0
	for i < len(b) && b[i] != 0 {
		i++
	}
	return string(b[:i])
}
