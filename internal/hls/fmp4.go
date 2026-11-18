package hls

import "encoding/binary"

// Track identifiers: the video track is always 1, sound (when present) is 2.
const (
	trackVideo = 1
	trackAudio = 2
)

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

// unityMatrix is the identity transform every track declares: nine 32-bit
// fixed-point values, 36 bytes in total.
//
// It has to be exactly 36 bytes. Writing 32 makes mvhd 104 bytes instead of 108
// and tkhd 88 instead of 92 - sizes fixed by the specification. ffmpeg reads
// those boxes by field offset and copes; Chrome's MP4 parser validates them and
// rejects the init segment, which then fails every append that follows.
var unityMatrix = func() []byte {
	values := [9]uint32{
		0x00010000, 0, 0,
		0, 0x00010000, 0,
		0, 0, 0x40000000,
	}
	out := make([]byte, 0, 36)
	for _, v := range values {
		out = append(out, u32(v)...)
	}
	return out
}()

// buildInit assembles the initialisation segment: the boxes a player must have
// before it can decode any fragment. Everything here is constant for the life of
// a stream, which is why it is built once and cached.
//
// audioChannels above zero adds a second track carrying Opus. It is included from
// the start rather than added when the first audio packet arrives: changing the
// init mid-stream changes the segment names and forces every connected player to
// re-initialise.
func buildInit(info spsInfo, ps paramSets, audioChannels int) []byte {
	ftyp := box("ftyp",
		[]byte("iso5"), u32(0),
		[]byte("iso5"), []byte("iso6"), []byte("mp41"),
	)

	nextTrackID := uint32(2)
	if audioChannels > 0 {
		nextTrackID = 3
	}

	mvhd := fullBox("mvhd", 0, 0,
		u32(0), u32(0), // creation, modification
		u32(mp4Timescale), u32(0), // timescale, duration (fragmented: unknown)
		u32(0x00010000), u16(0x0100), u16(0), // rate, volume, reserved
		u32(0), u32(0), // reserved
		unityMatrix,
		u32(0), u32(0), u32(0), u32(0), u32(0), u32(0), // pre_defined
		u32(nextTrackID),
	)

	tkhd := fullBox("tkhd", 0, 0x3, // enabled, in movie
		u32(0), u32(0), // creation, modification
		u32(trackVideo), u32(0), // track_ID, reserved
		u32(0),         // duration
		u32(0), u32(0), // reserved
		u16(0), u16(0), u16(0), u16(0), // layer, alternate_group, volume, reserved
		unityMatrix,
		u32(uint32(info.DisplayWidth)<<16), u32(uint32(info.DisplayHeight)<<16),
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
		u32(trackVideo), // track_ID
		u32(1),          // default_sample_description_index
		u32(0), u32(0), u32(0),
	)
	boxes := [][]byte{mvhd, trak}
	mvexBoxes := [][]byte{trex}

	if audioChannels > 0 {
		boxes = append(boxes, buildAudioTrak(audioChannels))
		mvexBoxes = append(mvexBoxes, fullBox("trex", 0, 0,
			u32(trackAudio), u32(1), u32(0), u32(0), u32(0),
		))
	}

	mvex := box("mvex", mvexBoxes...)

	return append(ftyp, box("moov", append(boxes, mvex)...)...)
}

// buildAudioTrak is the second track: Opus, 48 kHz, mono or stereo.
func buildAudioTrak(channels int) []byte {
	tkhd := fullBox("tkhd", 0, 0x3,
		u32(0), u32(0),
		u32(trackAudio), u32(0),
		u32(0),
		u32(0), u32(0),
		u16(0), u16(0), u16(0x0100), u16(0), // layer, group, volume, reserved
		unityMatrix,
		u32(0), u32(0), // no width or height for sound
	)

	mdhd := fullBox("mdhd", 0, 0,
		u32(0), u32(0),
		u32(mp4Timescale), u32(0),
		u16(0x55C4), u16(0),
	)

	hdlr := fullBox("hdlr", 0, 0,
		u32(0),
		[]byte("soun"),
		u32(0), u32(0), u32(0),
		append([]byte("SoundHandler"), 0),
	)

	smhd := fullBox("smhd", 0, 0, u16(0), u16(0))

	dref := fullBox("dref", 0, 0, u32(1), fullBox("url ", 0, 1))
	dinf := box("dinf", dref)

	stsd := fullBox("stsd", 0, 0, u32(1), buildOpusSampleEntry(channels))
	stts := fullBox("stts", 0, 0, u32(0))
	stsc := fullBox("stsc", 0, 0, u32(0))
	stsz := fullBox("stsz", 0, 0, u32(0), u32(0))
	stco := fullBox("stco", 0, 0, u32(0))

	stbl := box("stbl", stsd, stts, stsc, stsz, stco)
	minf := box("minf", smhd, dinf, stbl)
	mdia := box("mdia", mdhd, hdlr, minf)

	return box("trak", tkhd, mdia)
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
	entry = append(entry, u16(info.DisplayWidth)...)
	entry = append(entry, u16(info.DisplayHeight)...)
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
		// array_completeness stays 0: the parameter sets also travel in-band, and
		// claiming the array is complete while they appear elsewhere is a
		// contradiction a strict demuxer is entitled to reject.
		b = append(b, byte(nalTypeVPS+i)) // array_completeness | reserved | type
		b = append(b, u16(1)...)          // numNalus
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
func buildFragment(sequence uint32, videoBase, audioBase uint64, video, audio []fragmentSample) []byte {
	mfhd := fullBox("mfhd", 0, 0, u32(sequence))

	// default-base-is-moof (0x020000) plus an explicit sample description index:
	// without the first, the data offset in trun is relative to the file start,
	// which is wrong for a fragment served on its own.
	videoTFHD := fullBox("tfhd", 0, 0x020000|0x02, u32(trackVideo), u32(1))
	videoTFDT := fullBox("tfdt", 1, 0, u64(videoBase))
	audioTFHD := fullBox("tfhd", 0, 0x020000|0x02, u32(trackAudio), u32(1))
	audioTFDT := fullBox("tfdt", 1, 0, u64(audioBase))

	var videoPayload, audioPayload []byte
	totalTicks := uint32(0)
	for _, s := range video {
		videoPayload = append(videoPayload, s.data...)
		totalTicks += s.duration
	}
	for _, s := range audio {
		audioPayload = append(audioPayload, s.data...)
	}

	// Two passes: the trun data offsets depend on the size of the moof that
	// contains them, and the size depends only on the sample counts, so the
	// second pass produces the same layout with the real offsets.
	build := func(videoOffset, audioOffset uint32) []byte {
		trafs := [][]byte{
			box("traf", videoTFHD, videoTFDT, buildTrun(video, videoOffset)),
		}
		if len(audio) > 0 {
			trafs = append(trafs, box("traf", audioTFHD, audioTFDT, buildTrun(audio, audioOffset)))
		}
		return box("moof", append([][]byte{mfhd}, trafs...)...)
	}

	moof := build(0, 0)
	videoOffset := uint32(len(moof)) + 8
	audioOffset := videoOffset + uint32(len(videoPayload))
	moof = build(videoOffset, audioOffset)

	payload := append(videoPayload, audioPayload...)

	// CMAF segments start with a segment type box, then an index, exactly the
	// order ffmpeg writes. Both are optional in the specification and ffmpeg-based
	// players ignore them, but matching the reference removes differences from the
	// files browser implementations are tested against.
	styp := box("styp",
		[]byte("iso5"), u32(0),
		[]byte("iso5"), []byte("iso6"), []byte("mp41"),
	)
	mdat := box("mdat", payload)
	sidx := buildSidx(uint32(len(moof)+len(mdat)), totalTicks)

	return append(append(append(styp, sidx...), moof...), mdat...)
}

// buildSidx is the segment index: one reference covering the fragment that
// follows it.
func buildSidx(referenceSize, duration uint32) []byte {
	body := make([]byte, 0, 28)
	body = append(body, u32(1)...)             // reference_ID
	body = append(body, u32(mp4Timescale)...)  // timescale
	body = append(body, u32(0)...)             // earliest_presentation_time
	body = append(body, u32(0)...)             // first_offset
	body = append(body, u16(0)...)             // reserved
	body = append(body, u16(1)...)             // reference_count
	body = append(body, u32(referenceSize)...) // reference_type 0 (media) + size
	body = append(body, u32(duration)...)      // subsegment_duration
	body = append(body, u32(0x90000000)...)    // starts_with_SAP, SAP type 1

	return fullBox("sidx", 0, 0, body)
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
