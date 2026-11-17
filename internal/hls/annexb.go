package hls

// HEVC NAL unit types that the segmenter distinguishes.
const (
	nalTypeTrailN = 0
	nalTypeTrailR = 1

	// nalTypeBlaWLp .. nalTypeRsvIRAP are the IRAP pictures: types 16-23 in the
	// spec, which covers the IDR and CRA pictures a camera actually sends.
	nalTypeBlaWLp   = 16
	nalTypeBlaWRadl = 17
	nalTypeBlaNLp   = 18
	nalTypeIDRWRADL = 19
	nalTypeIDRNLP   = 20
	nalTypeCRA      = 21
	nalTypeRsvIRAP  = 23

	nalTypeVPS = 32
	nalTypeSPS = 33
	nalTypePPS = 34
)

// splitNALs returns the NAL units of an Annex-B buffer, without start codes.
//
// A trailing zero that belongs to the next start code is trimmed, otherwise the
// last NAL of a frame would carry a stray byte and fail to parse.
func splitNALs(data []byte) [][]byte {
	var nals [][]byte
	start := -1

	for i := 0; i+3 <= len(data); i++ {
		var header int
		switch {
		case data[i] == 0 && data[i+1] == 0 && data[i+2] == 1:
			header = i + 3
		case i+4 <= len(data) && data[i] == 0 && data[i+1] == 0 && data[i+2] == 0 && data[i+3] == 1:
			header = i + 4
		default:
			continue
		}

		if start >= 0 {
			// trimZeroes returns a length, not an index: slicing with it directly
			// would cut the NAL at its own length instead of its end.
			if n := trimZeroes(data[start:i]); n > 0 {
				nals = append(nals, data[start:start+n])
			}
		}
		start = header
		i = header - 1
	}

	if start >= 0 {
		if end := trimZeroes(data[start:]); end > 0 {
			nals = append(nals, data[start:start+end])
		}
	}

	return nals
}

// trimZeroes returns the length of b without its trailing zero bytes.
func trimZeroes(b []byte) int {
	end := len(b)
	for end > 0 && b[end-1] == 0 {
		end--
	}
	return end
}

// nalType reads the 6-bit type from an HEVC NAL header.
func nalType(nal []byte) int {
	if len(nal) < 2 {
		return -1
	}
	return int(nal[0]>>1) & 0x3F
}

// isVCL reports whether the NAL carries picture data.
func isVCL(t int) bool { return t >= 0 && t <= 31 }
