package hls

import (
	"encoding/binary"
	"testing"
)

// TestInitBoxSizesMatchTheSpec pins the sizes the specification fixes for
// version 0 boxes.
//
// It exists because the matrix was once written as 32 bytes instead of 36, which
// silently produced a 104-byte mvhd and an 88-byte tkhd. ffmpeg reads those
// boxes by field offset and never noticed; Chrome validates them and refused the
// init segment, so every media segment appended afterwards failed.
func TestInitBoxSizesMatchTheSpec(t *testing.T) {
	info, err := parseSPS(mustHex(t, spsHex))
	if err != nil {
		t.Fatalf("parseSPS: %v", err)
	}
	ps := paramSets{
		vps: mustHex(t, vpsHex),
		sps: mustHex(t, spsHex),
		pps: mustHex(t, ppsHex),
	}

	init := buildInit(info, ps, 0)

	moov := child(init, 0, "moov")
	if moov == nil {
		t.Fatal("no moov")
	}
	if mvhd := child(moov, 8, "mvhd"); len(mvhd) != 108 {
		t.Fatalf("mvhd is %d bytes, want 108", len(mvhd))
	}
	trak := child(moov, 8, "trak")
	if trak == nil {
		t.Fatal("no trak")
	}
	if tkhd := child(trak, 8, "tkhd"); len(tkhd) != 92 {
		t.Fatalf("tkhd is %d bytes, want 92", len(tkhd))
	}

	// The track section should be byte-identical to what ffmpeg writes for the
	// same camera, which is the shape browser parsers are tested against.
	if len(trak) != 546 {
		t.Fatalf("trak is %d bytes, want 546", len(trak))
	}
}

// TestInitHasAnAudioTrackWhenAsked covers the second track: an Opus sample entry
// with its decoder configuration record, and a trex for it in mvex.
func TestInitHasAnAudioTrackWhenAsked(t *testing.T) {
	info, err := parseSPS(mustHex(t, spsHex))
	if err != nil {
		t.Fatalf("parseSPS: %v", err)
	}
	ps := paramSets{
		vps: mustHex(t, vpsHex),
		sps: mustHex(t, spsHex),
		pps: mustHex(t, ppsHex),
	}

	init := buildInit(info, ps, 2)

	moov := child(init, 0, "moov")
	if moov == nil {
		t.Fatal("no moov")
	}

	traks := children(moov, 8, "trak")
	if len(traks) != 2 {
		t.Fatalf("moov has %d traks, want 2", len(traks))
	}
	if len(children(init, 8, "trak")) != 0 {
		t.Fatal("a trak escaped the moov")
	}

	// The second track is the audio one: 'soun' handler, Opus entry, dOps record.
	hdlr := descend(traks[1], "mdia", "hdlr")
	if hdlr == nil {
		t.Fatal("the second trak has no hdlr")
	}
	if got := string(hdlr[16:20]); got != "soun" {
		t.Fatalf("handler type = %q, want soun", got)
	}

	stsd := descend(traks[1], "mdia", "minf", "stbl", "stsd")
	if stsd == nil {
		t.Fatal("no stsd in the audio track")
	}
	if got := string(stsd[20:24]); got != "Opus" {
		t.Fatalf("audio sample entry is %q, want Opus", got)
	}
	if child(stsd[16:], 36, "dOps") == nil {
		t.Fatal("the Opus entry has no dOps record")
	}

	// And no audio track when none was asked for.
	if traks := children(child(buildInit(info, ps, 0), 0, "moov"), 8, "trak"); len(traks) != 1 {
		t.Fatalf("video-only init has %d traks, want 1", len(traks))
	}
}

// TestFragmentCarriesBothTracks checks the two trafs and, more importantly, that
// the second track's samples start right after the first track's bytes.
func TestFragmentCarriesBothTracks(t *testing.T) {
	video := []fragmentSample{
		{data: make([]byte, 100), duration: 4500, key: true},
		{data: make([]byte, 50), duration: 4500, key: false},
	}
	audio := []fragmentSample{
		{data: make([]byte, 20), duration: 900, key: true},
		{data: make([]byte, 20), duration: 900, key: true},
	}

	data := buildFragment(1, 0, 0, video, audio)

	moof := child(data, 0, "moof")
	mdat := child(data, 0, "mdat")
	if moof == nil || mdat == nil {
		t.Fatal("missing moof or mdat")
	}

	trafs := children(moof, 8, "traf")
	if len(trafs) != 2 {
		t.Fatalf("moof has %d trafs, want 2", len(trafs))
	}

	videoOffset := int(int32(binary.BigEndian.Uint32(child(trafs[0], 8, "trun")[16:20])))
	audioOffset := int(int32(binary.BigEndian.Uint32(child(trafs[1], 8, "trun")[16:20])))

	if want := len(moof) + 8; videoOffset != want {
		t.Fatalf("video data offset = %d, want %d", videoOffset, want)
	}
	if want := videoOffset + 150; audioOffset != want {
		t.Fatalf("audio data offset = %d, want %d (after the video bytes)", audioOffset, want)
	}
	if got := len(mdat) - 8; got != 150+40 {
		t.Fatalf("mdat payload is %d bytes, want %d", got, 190)
	}
}

// descend walks a chain of boxes, each nested directly inside the previous one.
func descend(box []byte, path ...string) []byte {
	cur := box
	for _, name := range path {
		found := child(cur, 8, name)
		if found == nil {
			return nil
		}
		cur = found
	}
	return cur
}

// children returns every box of the given type starting at off.
func children(data []byte, off int, want string) [][]byte {
	var out [][]byte
	for off+8 <= len(data) {
		size := int(binary.BigEndian.Uint32(data[off : off+4]))
		if size < 8 || off+size > len(data) {
			return out
		}
		if string(data[off+4:off+8]) == want {
			out = append(out, data[off:off+size])
		}
		off += size
	}
	return out
}

// TestFragmentHasATraf covers the box that was missing once: tfhd, tfdt and trun
// belong inside a traf, and a moof that lists them directly is malformed.
//
// ffmpeg's demuxer scans for those boxes and happily decoded the result, so the
// segments looked fine until a browser refused to build a track from them.
func TestFragmentHasATraf(t *testing.T) {
	data := buildFragment(1, 0, 0, []fragmentSample{
		{data: []byte{0, 0, 0, 2, 0x40, 0x01}, duration: 4500, key: true},
		{data: []byte{0, 0, 0, 2, 0x02, 0x01}, duration: 4500, key: false},
	}, nil)

	moof := child(data, 0, "moof")
	if moof == nil {
		t.Fatal("no moof box")
	}

	if child(moof, 8, "mfhd") == nil {
		t.Fatal("moof has no mfhd")
	}

	traf := child(moof, 8, "traf")
	if traf == nil {
		t.Fatal("moof has no traf: tfhd/tfdt/trun must be nested inside one")
	}

	for _, name := range []string{"tfhd", "tfdt", "trun"} {
		if child(traf, 8, name) == nil {
			t.Fatalf("traf has no %s", name)
		}
	}
}

// TestFragmentDataOffsetPointsAtMdat checks the arithmetic between the moof size
// and the payload, which a player uses to find the first sample.
func TestFragmentDataOffsetPointsAtMdat(t *testing.T) {
	samples := []fragmentSample{
		{data: []byte{0, 0, 0, 2, 0x40, 0x01}, duration: 4500, key: true},
		{data: []byte{0, 0, 0, 2, 0x02, 0x01}, duration: 4500, key: false},
	}
	data := buildFragment(7, 0, 0, samples, nil)

	moof := child(data, 0, "moof")
	mdat := child(data, 0, "mdat")
	if moof == nil || mdat == nil {
		t.Fatal("fragment is missing moof or mdat")
	}

	traf := child(moof, 8, "traf")
	trun := child(traf, 8, "trun")
	if trun == nil {
		t.Fatal("no trun")
	}

	// Version 0, flags 0x000701: data-offset, duration, size and flags present.
	offset := int(int32(binary.BigEndian.Uint32(trun[16:20])))
	// The moof is the first box and the mdat follows it, so the payload starts
	// right after the moof plus the 8-byte mdat header.
	want := len(moof) + 8
	if offset != want {
		t.Fatalf("data offset = %d, want %d (the mdat payload)", offset, want)
	}
}

// findBox returns the offset of the first top-level box of the given type, or -1.
func findBox(data []byte, want string) int {
	off := 0
	for off+8 <= len(data) {
		size := int(binary.BigEndian.Uint32(data[off : off+4]))
		if size < 8 || off+size > len(data) {
			return -1
		}
		if string(data[off+4:off+8]) == want {
			return off
		}
		off += size
	}
	return -1
}

// child finds a box by type among the boxes starting at off.
func child(data []byte, off int, want string) []byte {
	for off+8 <= len(data) {
		size := int(binary.BigEndian.Uint32(data[off : off+4]))
		typ := string(data[off+4 : off+8])
		if size < 8 || off+size > len(data) {
			return nil
		}
		if typ == want {
			return data[off : off+size]
		}
		off += size
	}
	return nil
}
