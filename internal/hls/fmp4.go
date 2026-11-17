package hls

import "encoding/binary"

// mp4Timescale is the media clock of the fMP4 track: the same 90kHz the TS muxer
// uses, so both outputs describe the same instants with the same numbers.
const mp4Timescale = 90_000

func u16(v uint16) []byte { return binary.BigEndian.AppendUint16(nil, v) }
func u32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
func u64(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }

// box assembles an ISO BMFF box: a 32-bit size, a four-character type, payload.
func box(typ string, parts ...[]byte) []byte {
	size := 8
	for _, p := range parts {
		size += len(p)
	}
	out := make([]byte, 0, size)
	out = binary.BigEndian.AppendUint32(out, uint32(size))
	out = append(out, typ...)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// fullBox is a box whose payload starts with a version and 24 flag bits.
func fullBox(typ string, version byte, flags uint32, parts ...[]byte) []byte {
	header := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	return box(typ, append([][]byte{header}, parts...)...)
}

// unityMatrix is the identity transform every track declares.
var unityMatrix = []byte{
	0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x40, 0x00, 0x00, 0x00,
}

// buildInit assembles the initialisation segment: the boxes a player must have
// before it can decode any fragment. Everything here is constant for the life of
// a stream, which is why it is built once and cached.
func buildInit(info spsInfo, ps paramSets) []byte {
	ftyp := box("ftyp",
		[]byte("iso5"), u32(0),
		[]byte("iso5"), []byte("iso6"), []byte("mp41"),
	)

	mvhd := fullBox("mvhd", 0, 0,
		u32(0), u32(0), // creation, modification
		u32(mp4Timescale), u32(0), // timescale, duration (fragmented: unknown)
		u32(0x00010000), u16(0x0100), u16(0), // rate, volume, reserved
		u32(0), u32(0), // reserved
		unityMatrix,
		u32(0), u32(0), u32(0), u32(0), u32(0), u32(0), // pre_defined
		u32(2), // next_track_ID
	)

	tkhd := fullBox("tkhd", 0, 0x3, // enabled, in movie
		u32(0), u32(0), // creation, modification
		u32(1), u32(0), // track_ID, reserved
		u32(0),         // duration
		u32(0), u32(0), // reserved
		u16(0), u16(0), u16(0), u16(0), // layer, alternate_group, volume, reserved
		unityMatrix,
		u32(uint32(info.Width)<<16), u32(uint32(info.Height)<<16),
	)

	mdhd := fullBox("mdhd", 0, 0,
		u32(0), u32(0),
		u32(mp4Timescale), u32(0),
		u16(0x55C4), // language "und"
		u16(0),
	)

	hdlr := fullBox("hdlr", 0, 0,
		u32(0),
		[]byte("vide"),
		u32(0), u32(0), u32(0),
		append([]byte("VideoHandler"), 0),
	)

	vmhd := fullBox("vmhd", 0, 1, u16(0), u16(0), u16(0), u16(0))

	dref := fullBox("dref", 0, 0, u32(1), fullBox("url ", 0, 1))
	dinf := box("dinf", dref)

	stsd := fullBox("stsd", 0, 0, u32(1), buildVisualSampleEntry(info, ps))
	stts := fullBox("stts", 0, 0, u32(0))
	stsc := fullBox("stsc", 0, 0, u32(0))
	stsz := fullBox("stsz", 0, 0, u32(0), u32(0))
	stco := fullBox("stco", 0, 0, u32(0))
	stbl := box("stbl", stsd, stts, stsc, stsz, stco)

	minf := box("minf", vmhd, dinf, stbl)
	mdia := box("mdia", mdhd, hdlr, minf)
	trak := box("trak", tkhd, mdia)

	trex := fullBox("trex", 0, 0,
		u32(1), // track_ID
		u32(1), // default_sample_description_index
		u32(0), u32(0), u32(0),
	)
	mvex := box("mvex", trex)

	return append(ftyp, box("moov", mvhd, trak, mvex)...)
}

// buildVisualSampleEntry is the hvc1 entry: the fixed fields of a visual sample
// entry followed by the decoder configuration record.
func buildVisualSampleEntry(info spsInfo, ps paramSets) []byte {
	entry := make([]byte, 0, 86+len(ps.vps)+len(ps.sps)+len(ps.pps))

	entry = append(entry, 0, 0, 0, 0, 0, 0) // reserved
	entry = append(entry, u16(1)...)        // data_reference_index
	entry = append(entry, u16(0)...)        // pre_defined
	entry = append(entry, u16(0)...)        // reserved
	entry = append(entry, u32(0)...)        // pre_defined[3]
	entry = append(entry, u32(0)...)
	entry = append(entry, u32(0)...)
	entry = append(entry, u16(info.Width)...)
	entry = append(entry, u16(info.Height)...)
	entry = append(entry, u32(0x00480000)...)  // horizresolution 72dpi
	entry = append(entry, u32(0x00480000)...)  // vertresolution 72dpi
	entry = append(entry, u32(0)...)           // reserved
	entry = append(entry, u16(1)...)           // frame_count
	entry = append(entry, make([]byte, 32)...) // compressorname
	entry = append(entry, u16(0x0018)...)      // depth
	entry = append(entry, 0xFF, 0xFF)          // pre_defined = -1

	return box("hev1", append(entry, buildHvcC(info, ps)...))
}

// buildHvcC is ISO/IEC 14496-15 §8.3.3.1: the decoder configuration record that
// carries the parameter sets a player needs before the first fragment.
func buildHvcC(info spsInfo, ps paramSets) []byte {
	b := []byte{1} // configurationVersion
	b = append(b, info.ProfileSpace<<6|info.TierFlag<<5|info.ProfileIDC)
	b = append(b, u32(info.CompatFlags)...)
	b = append(b,
		byte(info.Constraint>>40), byte(info.Constraint>>32), byte(info.Constraint>>24),
		byte(info.Constraint>>16), byte(info.Constraint>>8), byte(info.Constraint),
	)
	b = append(b, info.LevelIDC)
	b = append(b, u16(0xF000)...)           // min_spatial_segmentation_idc = 0
	b = append(b, 0xFC)                     // parallelismType = 0
	b = append(b, 0xFC|info.ChromaFormat)   // chromaFormat
	b = append(b, 0xF8|info.BitDepthLuma)   // bitDepthLumaMinus8
	b = append(b, 0xF8|info.BitDepthChroma) // bitDepthChromaMinus8
	b = append(b, u16(0)...)                // avgFrameRate, unknown
	b = append(b, 0<<6|1<<3|1<<2|3)         // constantFrameRate, numTemporalLayers, temporalIdNested, lengthSizeMinusOne
	b = append(b, 3)                        // numOfArrays

	for i, nal := range [][]byte{ps.vps, ps.sps, ps.pps} {
		if len(nal) == 0 {
			continue
		}
		b = append(b, 0x80|byte(nalTypeVPS+i)) // array_completeness | NAL_unit_type
		b = append(b, u16(1)...)               // numNalus
		b = append(b, u16(uint16(len(nal)))...)
		b = append(b, nal...)
	}

	return box("hvcC", b)
}

// fragmentSample is one encoded frame inside a fragment.
type fragmentSample struct {
	data     []byte // length-prefixed NAL units
	duration uint32 // in mp4Timescale ticks
	key      bool
}

// buildFragment wraps samples in moof+mdat, the shape a player consumes as one
// HLS segment.
func buildFragment(sequence uint32, baseTime uint64, samples []fragmentSample) []byte {
	mfhd := fullBox("mfhd", 0, 0, u32(sequence))

	// default-base-is-moof (0x020000) plus an explicit sample description index:
	// without the first, the data offset in trun is relative to the file start,
	// which is wrong for a fragment served on its own.
	tfhd := fullBox("tfhd", 0, 0x020000|0x02, u32(1), u32(1))
	tfdt := fullBox("tfdt", 1, 0, u64(baseTime))

	trun := buildTrun(samples, 0)
	moof := box("moof", mfhd, box("traf", tfhd, tfdt, trun))

	// The data offset is relative to the start of the moof box, and the mdat
	// header sits between them. Rebuilding is cheap: the size does not change.
	trun = buildTrun(samples, uint32(len(moof))+8)
	moof = box("moof", mfhd, box("traf", tfhd, tfdt, trun))

	var payload []byte
	for _, s := range samples {
		payload = append(payload, s.data...)
	}

	// CMAF segments start with a segment type box. Players that expect it are
	// usually tolerant, but the cost is 24 bytes and it removes a difference from
	// the output ffmpeg produces, which is the shape browsers are tested against.
	styp := box("styp",
		[]byte("iso5"), u32(0),
		[]byte("iso5"), []byte("iso6"), []byte("mp41"),
	)

	return append(append(styp, moof...), box("mdat", payload)...)
}

func buildTrun(samples []fragmentSample, dataOffset uint32) []byte {
	body := make([]byte, 0, 8+len(samples)*12)
	body = append(body, u32(uint32(len(samples)))...)
	body = append(body, u32(dataOffset)...)

	for _, s := range samples {
		flags := uint32(0x01010000) // depends_on = 1 (another frame), non-sync
		if s.key {
			flags = 0x02000000 // depends_on = 2 (an I-frame), sync
		}
		body = append(body, u32(s.duration)...)
		body = append(body, u32(uint32(len(s.data)))...)
		body = append(body, u32(flags)...)
	}

	// data-offset-present | sample-duration-present | sample-size-present |
	// sample-flags-present
	return fullBox("trun", 0, 0x000001|0x000100|0x000200|0x000400, body)
}
