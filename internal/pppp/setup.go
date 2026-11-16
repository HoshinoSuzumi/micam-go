package pppp

import "fmt"

// SetupLen is the fixed size of the 0x28 linking message.
const SetupLen = 88

// IsSetupRequest reports whether a relay payload is the repeated 0x28 request.
// Our own reply must not match, or we would answer forever.
func IsSetupRequest(p []byte) bool {
	return len(p) >= SetupLen && p[0] == 0xA1 && p[1] == 0x01
}

// SetupExternalAddress reads the address the relay observed for us out of a
// 0x28 request.
func SetupExternalAddress(req []byte) Addr {
	if len(req) < SetupLen {
		return Addr{}
	}
	return DecodeAddr(req[40:])
}

// SetupReply rewrites a 0x28 request into the reply the relay expects.
//
// Not part of the published PPPP message set - this is Xiaomi's own linking
// step, decoded from a packet capture. The rewrite is deterministic:
//
//	[1]      01 -> 02
//	[4..39]  session id, tag, UID            unchanged
//	[40..55] our external address            unchanged
//	[56..59] fe ff ff ff
//	[60..63] session id, byte-reversed
//	[64..79] our local address               (16-byte block)
//	[80..87] our external address            (compact 8-byte form)
//
// Neglecting this step does not raise an error: the stream simply stops.
func SetupReply(req []byte, local Addr) ([]byte, error) {
	if len(req) < SetupLen {
		return nil, fmt.Errorf("pppp: setup request too short: %d bytes", len(req))
	}

	out := make([]byte, len(req))
	copy(out, req)

	out[1] = 0x02
	out[56], out[57], out[58], out[59] = 0xFE, 0xFF, 0xFF, 0xFF
	out[60] = req[7] // session id, reversed
	out[61] = req[6]
	out[62] = req[5]
	out[63] = req[4]

	localBlock := local.Encode()
	copy(out[64:80], localBlock[:])

	external := DecodeAddr(req[40:])
	compact := external.Compact()
	copy(out[80:88], compact[:])

	return out, nil
}
