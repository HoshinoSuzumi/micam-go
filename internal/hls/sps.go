package hls

import (
	"errors"
	"fmt"
)

// spsInfo is everything the init segment needs from the sequence parameter set:
// the profile triple for hvcC, the chroma/depth fields, and the coded size for
// the sample entry.
type spsInfo struct {
	ProfileSpace   uint8
	TierFlag       uint8
	ProfileIDC     uint8
	CompatFlags    uint32
	Constraint     uint64 // 48 bits: progressive/interlaced flags plus reserved bits
	LevelIDC       uint8
	ChromaFormat   uint8
	BitDepthLuma   uint8 // minus 8, as stored in hvcC
	BitDepthChroma uint8
	Width          uint16
	Height         uint16
}

// bitReader reads fields out of a NAL unit, most significant bit first.
type bitReader struct {
	data []byte
	bit  int
}

func (r *bitReader) u(n int) (uint64, error) {
	if n < 0 || n > 64 {
		return 0, fmt.Errorf("hls: cannot read %d bits", n)
	}
	var v uint64
	for i := 0; i < n; i++ {
		byteIndex := r.bit / 8
		if byteIndex >= len(r.data) {
			return 0, errors.New("hls: SPS ended in the middle of a field")
		}
		shift := 7 - uint(r.bit%8)
		v = v<<1 | uint64((r.data[byteIndex]>>shift)&1)
		r.bit++
	}
	return v, nil
}

// ue reads an Exp-Golomb unsigned value, as used by the SPS body.
func (r *bitReader) ue() (uint64, error) {
	zeros := 0
	for {
		b, err := r.u(1)
		if err != nil {
			return 0, err
		}
		if b == 1 {
			break
		}
		zeros++
		if zeros > 31 {
			return 0, errors.New("hls: Exp-Golomb value is unreasonably long")
		}
	}
	if zeros == 0 {
		return 0, nil
	}
	rest, err := r.u(zeros)
	if err != nil {
		return 0, err
	}
	return (1 << uint(zeros)) - 1 + rest, nil
}

// unescape removes the emulation prevention bytes (00 00 03) that keep a NAL
// payload from containing a start code. The parser sees the original bits.
func unescape(b []byte) []byte {
	out := make([]byte, 0, len(b))
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c == 3 {
			zeros = 0
			continue
		}
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
		out = append(out, c)
	}
	return out
}

// parseSPS extracts the fields the fMP4 init segment needs.
//
// Only the leading part of the SPS is read: everything after the bit depth
// fields describes scaling lists, tiles and short-term references, none of which
// matters for packaging.
func parseSPS(nal []byte) (spsInfo, error) {
	var info spsInfo
	if len(nal) < 3 {
		return info, errors.New("hls: SPS is too short")
	}

	r := &bitReader{data: unescape(nal[2:])}

	if _, err := r.u(4); err != nil { // sps_video_parameter_set_id
		return info, err
	}
	maxSubLayersMinus1, err := r.u(3)
	if err != nil {
		return info, err
	}
	if _, err := r.u(1); err != nil { // sps_temporal_id_nesting_flag
		return info, err
	}

	// profile_tier_level(profilePresentFlag=1, maxNumSubLayersMinus1)
	space, err := r.u(2)
	if err != nil {
		return info, err
	}
	tier, err := r.u(1)
	if err != nil {
		return info, err
	}
	profile, err := r.u(5)
	if err != nil {
		return info, err
	}
	compat, err := r.u(32)
	if err != nil {
		return info, err
	}
	constraint, err := r.u(48)
	if err != nil {
		return info, err
	}
	level, err := r.u(8)
	if err != nil {
		return info, err
	}

	info.ProfileSpace = uint8(space)
	info.TierFlag = uint8(tier)
	info.ProfileIDC = uint8(profile)
	info.CompatFlags = uint32(compat)
	info.Constraint = constraint
	info.LevelIDC = uint8(level)

	// Sub-layer profile/level structures sit between the base profile and the
	// rest of the SPS. Cameras usually report a single layer, but skipping this
	// only works if it is handled: a multi-layer SPS would otherwise be parsed
	// as if its first sub-layer bytes were the chroma format.
	if maxSubLayersMinus1 > 0 {
		if maxSubLayersMinus1 > 7 {
			return info, errors.New("hls: SPS declares too many sub-layers")
		}
		for i := maxSubLayersMinus1; i < 8; i++ {
			if _, err := r.u(2); err != nil {
				return info, err
			}
		}

		subProfile := make([]bool, maxSubLayersMinus1+1)
		subLevel := make([]bool, maxSubLayersMinus1+1)
		for i := range subProfile {
			v, err := r.u(1)
			if err != nil {
				return info, err
			}
			subProfile[i] = v == 1
		}
		for i := range subLevel {
			v, err := r.u(1)
			if err != nil {
				return info, err
			}
			subLevel[i] = v == 1
		}
		for i := 0; i <= int(maxSubLayersMinus1); i++ {
			if subProfile[i] {
				if _, err := r.u(2 + 1 + 5 + 32 + 48); err != nil {
					return info, err
				}
			}
			if subLevel[i] {
				if _, err := r.u(8); err != nil {
					return info, err
				}
			}
		}
	}

	if _, err := r.ue(); err != nil { // sps_seq_parameter_set_id
		return info, err
	}
	chroma, err := r.ue()
	if err != nil {
		return info, err
	}
	if chroma > 3 {
		return info, fmt.Errorf("hls: unsupported chroma_format_idc %d", chroma)
	}
	info.ChromaFormat = uint8(chroma)

	if chroma == 3 {
		if _, err := r.u(1); err != nil { // separate_colour_plane_flag
			return info, err
		}
	}

	width, err := r.ue()
	if err != nil {
		return info, err
	}
	height, err := r.ue()
	if err != nil {
		return info, err
	}
	if width == 0 || height == 0 || width > 65535 || height > 65535 {
		return info, fmt.Errorf("hls: implausible coded size %dx%d", width, height)
	}
	info.Width = uint16(width)
	info.Height = uint16(height)

	conformance, err := r.u(1)
	if err != nil {
		return info, err
	}
	if conformance == 1 {
		for i := 0; i < 4; i++ { // left, right, top, bottom offsets
			if _, err := r.ue(); err != nil {
				return info, err
			}
		}
	}

	lumaDepth, err := r.ue()
	if err != nil {
		return info, err
	}
	chromaDepth, err := r.ue()
	if err != nil {
		return info, err
	}
	if lumaDepth > 7 || chromaDepth > 7 {
		return info, fmt.Errorf("hls: unsupported bit depth %d/%d", lumaDepth+8, chromaDepth+8)
	}
	info.BitDepthLuma = uint8(lumaDepth)
	info.BitDepthChroma = uint8(chromaDepth)

	return info, nil
}
