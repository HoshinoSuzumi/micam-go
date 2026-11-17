package hls

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"micam/internal/miss"
)

// TestAgainstACapture runs the segmenter over a real elementary stream.
//
// The other tests build frames by hand, which proves the boxes are well formed
// but not that a decoder accepts them. Point MICAM_HEVC_SAMPLE at a capture
// (`ffmpeg -i http://nas:8554/cam.ts -c copy -f hevc cap.h265`) to check the
// bytes are actually playable, and MICAM_HEVC_OUT at a directory to keep the
// segments so ffprobe can be run against them.
func TestAgainstACapture(t *testing.T) {
	sample := os.Getenv("MICAM_HEVC_SAMPLE")
	if sample == "" {
		t.Skip("set MICAM_HEVC_SAMPLE to a .h265 capture to check real frames")
	}

	raw, err := os.ReadFile(sample)
	if err != nil {
		t.Fatalf("read sample: %v", err)
	}

	aus := splitAccessUnits(splitNALs(raw))
	if len(aus) < 2 {
		t.Fatalf("sample has %d access units, need at least 2", len(aus))
	}

	segmenter := New(DefaultWindow)

	// The capture is 20 fps; the real timestamps matter only for monotonicity.
	for i, au := range aus {
		p := miss.Packet{
			Codec:       miss.CodecH265,
			TimestampMS: int64(i) * 50,
			Payload:     annexB(au),
		}
		if isKeyframeAU(au) {
			p.Flags = 1
		}
		if err := segmenter.Write(p); err != nil {
			t.Fatalf("write frame %d: %v", i, err)
		}
	}
	// A live stream would leave the last segment open until the next keyframe.
	segmenter.Flush()
	seg := segmenter

	init, ok := seg.Init()
	if !ok {
		t.Fatal("no init segment was built")
	}
	if typ := boxType(init, 0); typ != "ftyp" {
		t.Fatalf("init starts with %q, want ftyp", typ)
	}
	if !bytes.Contains(init, []byte("hvcC")) {
		t.Fatal("init has no hvcC record")
	}

	if got := seg.Segments(); got < 2 {
		t.Fatalf("produced %d segments, want at least 2", got)
	}

	playlist := seg.Playlist("cam/")
	if !bytes.HasPrefix(playlist, []byte("#EXTM3U")) {
		t.Fatalf("playlist does not start with #EXTM3U:\n%s", playlist)
	}
	if !bytes.Contains(playlist, []byte(`#EXT-X-MAP:URI="cam/init.mp4"`)) {
		t.Fatalf("playlist has no EXT-X-MAP:\n%s", playlist)
	}

	// Every segment must be a self-contained fragment: a player fetches one and
	// expects the moof that describes its own samples.
	for i := 0; i < seg.Segments(); i++ {
		data, ok := seg.Segment(uint64(i))
		if !ok {
			t.Fatalf("segment %d is missing", i)
		}
		if typ := boxType(data, 0); typ != "moof" {
			t.Fatalf("segment %d starts with %q, want moof", i, typ)
		}
	}

	if out := os.Getenv("MICAM_HEVC_OUT"); out != "" {
		if err := dump(out, init, playlist, seg); err != nil {
			t.Fatalf("dump: %v", err)
		}
		if err := writeVODPlaylist(out, playlist); err != nil {
			t.Fatalf("dump vod playlist: %v", err)
		}
		t.Logf("wrote segments to %s", out)
	}
}

// dump writes the playlist, init and segments in the layout the HTTP handlers
// serve, so ffprobe can be pointed at the directory.
func dump(dir string, init, playlist []byte, seg *Segmenter) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// The segments go in a cam/ subdirectory because that is what the playlist
	// names: "cam/init.mp4" relative to a playlist sitting one level up.
	media := filepath.Join(dir, "cam")
	if err := os.MkdirAll(media, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(media, "init.mp4"), init, 0o644); err != nil {
		return err
	}
	for i := 0; i < seg.Segments(); i++ {
		data, ok := seg.Segment(uint64(i))
		if !ok {
			return fmt.Errorf("segment %d vanished", i)
		}
		name := filepath.Join(media, fmt.Sprintf("seg%d.m4s", i))
		if err := os.WriteFile(name, data, 0o644); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(dir, "index.m3u8"), playlist, 0o644)
}

// writeVODPlaylist appends ENDLIST so offline tools stop instead of waiting for
// the next segment, which a live playlist never promises to deliver.
func writeVODPlaylist(dir string, playlist []byte) error {
	vod := append(append([]byte{}, playlist...), []byte("#EXT-X-ENDLIST\n")...)
	return os.WriteFile(filepath.Join(dir, "vod.m3u8"), vod, 0o644)
}

// splitAccessUnits groups NAL units into access units.
//
// An HEVC picture starts at a VCL NAL whose first slice header bit
// (first_slice_segment_in_pic_flag) is set.
func splitAccessUnits(nals [][]byte) [][][]byte {
	var aus [][][]byte
	var current [][]byte

	for _, nal := range nals {
		if isVCL(nalType(nal)) && len(nal) > 2 && nal[2]&0x80 != 0 && len(current) > 0 {
			aus = append(aus, current)
			current = nil
		}
		current = append(current, nal)
	}
	if len(current) > 0 {
		aus = append(aus, current)
	}

	return aus
}

func annexB(nals [][]byte) []byte {
	var out []byte
	for _, nal := range nals {
		out = append(out, 0, 0, 0, 1)
		out = append(out, nal...)
	}
	return out
}

func isKeyframeAU(nals [][]byte) bool {
	for _, nal := range nals {
		if t := nalType(nal); t >= nalTypeBlaWLp && t <= nalTypeCRA {
			return true
		}
	}
	return false
}

// boxType reads the four-character type of the box at offset.
func boxType(data []byte, offset int) string {
	if offset+8 > len(data) {
		return ""
	}
	return string(data[offset+4 : offset+8])
}

var _ = strings.TrimSpace
