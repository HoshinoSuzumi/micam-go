// Package audio turns the camera's audio frames into something a player can
// open without transcoding.
//
// The camera speaks G.711 A-law (codec 1027) or Opus (1032). A-law is the case
// this package handles: it is 8-bit companded PCM, so expanding it to 16-bit
// linear is a 256-entry table lookup, not a codec.
//
// Why a separate endpoint rather than a track inside the video: MPEG-TS has no
// stream type for G.711 (verified: ffmpeg writes it as a private stream and
// cannot read it back), and A-law is not a legal MP4 sample entry either. Putting
// it in /cam.ts would mean writing a PID nothing can decode.
package audio

// alawTable maps an A-law octet to linear 16-bit PCM.
//
// G.711 A-law: the octet is sign(1) | exponent(3) | mantissa(4), with the bits
// inverted before transmission. Built once at start rather than storing 512 bytes
// of table in source.
var alawTable = func() [256]int16 {
	var table [256]int16
	for i := range table {
		table[i] = decodeALawOctet(byte(i))
	}
	return table
}()

// decodeALawOctet expands one A-law octet.
func decodeALawOctet(octet byte) int16 {
	v := octet ^ 0x55 // the transmitted bits are inverted

	sign := v & 0x80
	exponent := (v >> 4) & 0x07
	mantissa := v & 0x0F

	var magnitude int32
	if exponent == 0 {
		magnitude = int32(mantissa)<<4 + 8
	} else {
		magnitude = (int32(mantissa)<<4 + 0x108) << (exponent - 1)
	}

	// The sign bit survives the XOR (0x55 does not touch bit 7) and is set for
	// positive samples, which is the opposite of what "sign bit" suggests.
	if sign == 0 {
		return int16(-magnitude)
	}
	return int16(magnitude)
}

// DecodeALaw expands A-law bytes to little-endian 16-bit PCM.
//
// dst must have room for 2*len(src) bytes; it is grown if it does not.
func DecodeALaw(dst, src []byte) []byte {
	need := len(src) * 2
	if cap(dst) < need {
		dst = make([]byte, need)
	}
	dst = dst[:need]

	for i, octet := range src {
		sample := alawTable[octet]
		dst[i*2] = byte(uint16(sample))
		dst[i*2+1] = byte(uint16(sample) >> 8)
	}

	return dst
}
