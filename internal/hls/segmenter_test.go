package hls

import (
	"bytes"
	"strings"
	"testing"

	"micam/internal/miss"
)

// accessUnit builds one HEVC access unit: the parameter sets the camera sends
// before every keyframe, followed by one picture.
func accessUnit(t *testing.T, vclType int, tsMS int64, key bool) miss.Packet {
	t.Helper()

	var payload []byte
	for _, h := range []string{vpsHex, spsHex, ppsHex} {
		payload = append(payload, 0x00, 0x00, 0x00, 0x01)
		payload = append(payload, mustHex(t, h)...)
	}

	// 0xAA in the first slice-header byte sets first_slice_segment_in_pic_flag,
	// which is how a real slice starts.
	payload = append(payload, 0x00, 0x00, 0x00, 0x01, byte(vclType<<1), 0x01, 0xAA, 0x02, 0x03)

	p := miss.Packet{Codec: miss.CodecH265, TimestampMS: tsMS, Payload: payload}
	if key {
		p.Flags = 1
	}
	return p
}

func TestNothingIsPublishedBeforeAKeyframe(t *testing.T) {
	s := New(DefaultWindow)

	for i := 0; i < 10; i++ {
		if err := s.Write(accessUnit(t, nalTypeTrailR, int64(i)*50, false)); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	if _, ok := s.Init(); ok {
		t.Fatal("an init segment was built without a keyframe")
	}
	if s.Segments() != 0 {
		t.Fatalf("got %d segments before any keyframe, want 0", s.Segments())
	}
	if s.Dropped() != 10 {
		t.Fatalf("dropped = %d, want 10", s.Dropped())
	}
}

func TestKeyframeStartsTheStream(t *testing.T) {
	s := New(DefaultWindow)

	if err := s.Write(accessUnit(t, nalTypeIDRWRADL, 0, true)); err != nil {
		t.Fatalf("write: %v", err)
	}

	init, ok := s.Init()
	if !ok {
		t.Fatal("no init segment after a keyframe")
	}
	if !bytes.Contains(init, []byte("hvcC")) {
		t.Fatal("init has no hvcC record")
	}
}

func TestSegmentIsClosedByTheNextKeyframe(t *testing.T) {
	s := New(DefaultWindow)

	s.Write(accessUnit(t, nalTypeIDRWRADL, 0, true))
	for i := 1; i <= 40; i++ { // 40 frames at 20fps: two seconds
		s.Write(accessUnit(t, nalTypeTrailR, int64(i)*50, false))
	}

	if s.Segments() != 0 {
		t.Fatalf("got %d segments while the first one is still open, want 0", s.Segments())
	}

	s.Write(accessUnit(t, nalTypeIDRWRADL, 2050, true))

	if s.Segments() != 1 {
		t.Fatalf("got %d segments after the second keyframe, want 1", s.Segments())
	}

	data, ok := s.Segment(0)
	if !ok {
		t.Fatal("segment 0 is missing")
	}
	// CMAF: a segment type box, then the fragment itself.
	if got := boxType(data, 0); got != "styp" {
		t.Fatalf("segment starts with %q, want styp", got)
	}
	if got := boxType(data, stypLen); got != "moof" {
		t.Fatalf("segment has %q after styp, want moof", got)
	}

	// Two seconds of video, allowing for the frame that closed the segment.
	if d := s.Playlist(""); !strings.Contains(string(d), "#EXTINF:2.") {
		t.Fatalf("playlist does not report roughly two seconds:\n%s", d)
	}
}

func TestWindowSlidesAndForgets(t *testing.T) {
	s := New(3)

	for i := 0; i < 5; i++ {
		s.Write(accessUnit(t, nalTypeIDRWRADL, int64(i)*2000, true))
		s.Write(accessUnit(t, nalTypeTrailR, int64(i)*2000+50, false))
		s.Write(accessUnit(t, nalTypeTrailR, int64(i)*2000+100, false))
	}
	s.Flush()

	if s.Segments() != 3 {
		t.Fatalf("window holds %d segments, want 3", s.Segments())
	}
	if _, ok := s.Segment(0); ok {
		t.Fatal("segment 0 should have slid out of the window")
	}
	if _, ok := s.Segment(4); !ok {
		t.Fatal("the newest segment is missing")
	}

	playlist := string(s.Playlist(""))
	if !strings.Contains(playlist, "#EXT-X-MEDIA-SEQUENCE:2") {
		t.Fatalf("playlist does not start at segment 2:\n%s", playlist)
	}
}

func TestPlaylistShape(t *testing.T) {
	s := New(DefaultWindow)
	s.Write(accessUnit(t, nalTypeIDRWRADL, 0, true))
	s.Write(accessUnit(t, nalTypeTrailR, 50, false))
	s.Write(accessUnit(t, nalTypeIDRWRADL, 2000, true))

	playlist := string(s.Playlist(""))
	for _, want := range []string{
		"#EXTM3U",
		"#EXT-X-VERSION:7",
		"#EXT-X-TARGETDURATION:",
		"#EXT-X-MAP:URI=\"init.mp4\"",
		"#EXTINF:",
		"seg0.m4s",
	} {
		if !strings.Contains(playlist, want) {
			t.Errorf("playlist has no %q:\n%s", want, playlist)
		}
	}
}

func TestPlaylistURIsAreRelativeToThePlaylist(t *testing.T) {
	s := New(DefaultWindow)
	s.Write(accessUnit(t, nalTypeIDRWRADL, 0, true))
	s.Write(accessUnit(t, nalTypeTrailR, 50, false))
	s.Write(accessUnit(t, nalTypeIDRWRADL, 2000, true))

	// A playlist at /cam.m3u8 lives in "/", so a bare "init.mp4" would be fetched
	// from /init.mp4. The base has to carry the directory.
	playlist := string(s.Playlist("cam/"))
	if !strings.Contains(playlist, `#EXT-X-MAP:URI="cam/init.mp4"`) {
		t.Fatalf("init URI is not relative to the playlist:\n%s", playlist)
	}
	if !strings.Contains(playlist, "\ncam/seg0.m4s\n") {
		t.Fatalf("segment URI is not relative to the playlist:\n%s", playlist)
	}
}

func TestIgnoresWhatItCannotPackage(t *testing.T) {
	s := New(DefaultWindow)

	// Audio frames are dropped.
	s.Write(miss.Packet{Codec: miss.CodecPCMA, TimestampMS: 0, Payload: []byte{1, 2, 3}})

	// H.264 needs an avcC record instead of hvcC; until that exists these must
	// not produce a broken init segment.
	s.Write(miss.Packet{Codec: miss.CodecH264, TimestampMS: 0, Flags: 1, Payload: []byte{0, 0, 0, 1, 0x65, 0x88}})

	if _, ok := s.Init(); ok {
		t.Fatal("init was built from frames that cannot be packaged")
	}
}

func TestFlushClosesTheOpenSegment(t *testing.T) {
	s := New(DefaultWindow)
	s.Write(accessUnit(t, nalTypeIDRWRADL, 0, true))
	s.Write(accessUnit(t, nalTypeTrailR, 50, false))

	if s.Segments() != 0 {
		t.Fatalf("got %d segments before flushing, want 0", s.Segments())
	}

	s.Flush()

	if s.Segments() != 1 {
		t.Fatalf("got %d segments after flushing, want 1", s.Segments())
	}
}
