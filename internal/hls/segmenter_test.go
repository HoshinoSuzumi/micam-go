package hls

import (
	"bytes"
	"encoding/binary"
	"regexp"
	"strings"
	"testing"

	"micam/internal/miss"
)

// spsHexChanged is the same SPS with one extra trailing byte: it still parses,
// and it is different, which is all a re-announcement has to be.
const spsHexChanged = spsHex + "80"

// accessUnit builds one HEVC access unit: the parameter sets the camera sends
// before every keyframe, followed by one picture.
func accessUnit(t *testing.T, vclType int, tsMS int64, key bool) miss.Packet {
	t.Helper()
	return accessUnitWithSets(t, vclType, tsMS, key, vpsHex, spsHex, ppsHex)
}

func accessUnitWithSPS(t *testing.T, vclType int, tsMS int64, sps string) miss.Packet {
	t.Helper()
	return accessUnitWithSets(t, vclType, tsMS, true, vpsHex, sps, ppsHex)
}

func accessUnitWithSets(t *testing.T, vclType int, tsMS int64, key bool, vps, sps, pps string) miss.Packet {
	t.Helper()

	var payload []byte
	for _, h := range []string{vps, sps, pps} {
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
	s := New(DefaultWindow, false)

	for i := 0; i < 10; i++ {
		if err := s.Write(accessUnit(t, nalTypeTrailR, int64(i)*50, false)); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	// The init segment may already exist: it only describes the stream, and
	// having it early means a player that arrives before the first keyframe can
	// still fetch it. What must not exist is media, because there is nothing a
	// decoder could start from.
	if s.Segments() != 0 {
		t.Fatalf("got %d segments before any keyframe, want 0", s.Segments())
	}
	if s.Dropped() != 10 {
		t.Fatalf("dropped = %d, want 10", s.Dropped())
	}
	if playlist := string(s.Playlist("cam/")); strings.Contains(playlist, ".m4s") {
		t.Fatalf("playlist names media before any keyframe:\n%s", playlist)
	}
}

func TestKeyframeStartsTheStream(t *testing.T) {
	s := New(DefaultWindow, false)

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
	s := New(DefaultWindow, false)

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
	// CMAF: a segment type box and an index, then the fragment itself.
	if got := boxType(data, 0); got != "styp" {
		t.Fatalf("segment starts with %q, want styp", got)
	}
	if got := boxType(data, findBox(data, "sidx")); got != "sidx" {
		t.Fatal("segment has no sidx")
	}
	if findBox(data, "moof") < 0 {
		t.Fatal("segment has no moof")
	}

	// Two seconds of video, allowing for the frame that closed the segment.
	if d := s.Playlist(""); !strings.Contains(string(d), "#EXTINF:2.") {
		t.Fatalf("playlist does not report roughly two seconds:\n%s", d)
	}
}

func TestWindowSlidesAndForgets(t *testing.T) {
	s := New(3, false)

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
	s := New(DefaultWindow, false)
	s.Write(accessUnit(t, nalTypeIDRWRADL, 0, true))
	s.Write(accessUnit(t, nalTypeTrailR, 50, false))
	s.Write(accessUnit(t, nalTypeIDRWRADL, 2000, true))

	playlist := string(s.Playlist(""))
	for _, want := range []string{
		"#EXTM3U",
		"#EXT-X-VERSION:7",
		"#EXT-X-TARGETDURATION:",
		"#EXT-X-MAP:URI=\"init-",
		"#EXTINF:",
		"#EXTINF:",
	} {
		if !strings.Contains(playlist, want) {
			t.Errorf("playlist has no %q:\n%s", want, playlist)
		}
	}
}

func TestPlaylistURIsAreRelativeToThePlaylist(t *testing.T) {
	s := New(DefaultWindow, false)
	s.Write(accessUnit(t, nalTypeIDRWRADL, 0, true))
	s.Write(accessUnit(t, nalTypeTrailR, 50, false))
	s.Write(accessUnit(t, nalTypeIDRWRADL, 2000, true))

	// A playlist at /cam.m3u8 lives in "/", so a bare "init.mp4" would be fetched
	// from /init.mp4. The base has to carry the directory.
	playlist := string(s.Playlist("cam/"))
	if !strings.Contains(playlist, `#EXT-X-MAP:URI="cam/init-`) {
		t.Fatalf("init URI is not relative to the playlist:\n%s", playlist)
	}
	if !regexp.MustCompile(`(?m)^cam/seg[0-9a-f]{16}\.m4s$`).MatchString(playlist) {
		t.Fatalf("segment URI is not relative to the playlist:\n%s", playlist)
	}
}

// TestInitSurvivesAParameterSetChange covers the failure that only appears hours
// into a session: the camera re-sends its parameter sets, the init was dropped
// and never rebuilt, and every playlist after that had no EXT-X-MAP. Players
// could not build a decoder, so playback stopped working long after it had
// started.
func TestInitSurvivesAParameterSetChange(t *testing.T) {
	s := New(DefaultWindow, false)

	s.Write(accessUnit(t, nalTypeIDRWRADL, 0, true))
	s.Write(accessUnit(t, nalTypeTrailR, 50, false))
	if _, ok := s.Init(); !ok {
		t.Fatal("no init after the first keyframe")
	}
	firstInit, _ := s.Init()

	// A second SPS that parses the same way but differs by one byte, the way a
	// camera's re-announcement does.
	changed := accessUnitWithSPS(t, nalTypeIDRWRADL, 100, spsHexChanged)
	s.Write(changed)

	init, ok := s.Init()
	if !ok {
		t.Fatal("the init disappeared when the parameter sets changed")
	}
	if bytes.Equal(init, firstInit) {
		t.Fatal("the init was not rebuilt for the new parameter sets")
	}

	playlist := string(s.Playlist("cam/"))
	if !strings.Contains(playlist, "#EXT-X-MAP:URI=") {
		t.Fatalf("playlist lost its EXT-X-MAP:\n%s", playlist)
	}

	// And the fragment produced from the new keyframe is still reachable.
	s.Write(accessUnit(t, nalTypeIDRWRADL, 200, true))
	if s.Segments() == 0 {
		t.Fatal("no segment was produced after the parameter set change")
	}
}

// TestAudioTimelineFollowsTheVideo pins the two tracks to the same clock.
//
// It was broken once by returning the *latest* timestamp as the origin: every
// audio packet then looked like it happened at the start of the stream, so a
// 3-second segment carried an audio track claiming 106 seconds and starting a
// hundred seconds before the picture. Players played a couple of seconds and then
// waited forever for audio that was never going to arrive at the right time.
func TestAudioTimelineFollowsTheVideo(t *testing.T) {
	s := New(DefaultWindow, true)

	// Camera timestamps that do not start at zero, like the epoch milliseconds
	// some models report.
	base := int64(1_700_000_000_000)

	s.Write(accessUnit(t, nalTypeIDRWRADL, base, true))
	for i := 1; i <= 40; i++ {
		s.Write(accessUnit(t, nalTypeTrailR, base+int64(i)*50, false))
		if i%2 == 0 {
			s.Write(miss.Packet{
				Codec:       miss.CodecOpus,
				TimestampMS: base + int64(i)*50,
				Payload:     []byte{0xF8, 1, 2, 3},
			})
		}
	}
	s.Write(accessUnit(t, nalTypeIDRWRADL, base+41*50, true))

	// A second GOP, so the segment under test does not start at zero: the bug put
	// audio at tfdt 0 while the video was a hundred seconds in, which the first
	// segment cannot show.
	for i := 42; i <= 80; i++ {
		s.Write(accessUnit(t, nalTypeTrailR, base+int64(i)*50, false))
		if i%2 == 0 {
			s.Write(miss.Packet{
				Codec:       miss.CodecOpus,
				TimestampMS: base + int64(i)*50,
				Payload:     []byte{0xF8, 1, 2, 3},
			})
		}
	}
	s.Write(accessUnit(t, nalTypeIDRWRADL, base+81*50, true))

	segment, ok := s.Segment(1)
	if !ok {
		t.Fatal("the second segment was not produced")
	}

	timing := trafTiming(t, segment)
	video, okV := timing[uint32(trackVideo)]
	audio, okA := timing[uint32(trackAudio)]
	if !okV || !okA {
		t.Fatalf("timing found for video=%v audio=%v, want both", okV, okA)
	}

	// Both tracks cover the same wall-clock interval, so their decode times must
	// agree to within one audio packet.
	if delta := audio.tfdt - video.tfdt; delta < -18000 || delta > 18000 {
		t.Fatalf("audio starts %d ticks (%.2fs) from the video: audio tfdt %d, video tfdt %d",
			delta, float64(delta)/90000, audio.tfdt, video.tfdt)
	}

	// No single audio sample may be long: the bug gave one packet a duration of
	// two seconds to make the totals add up.
	if audio.max > 18000 {
		t.Fatalf("longest audio sample is %d ticks (%.2fs), want at most 0.2s",
			audio.max, float64(audio.max)/90000)
	}
}

// trackTiming is what one traf says about its track.
type trackTiming struct {
	tfdt  int64 // decode time of the first sample
	total int64 // sum of the sample durations
	max   int64 // longest single sample
}

// trafTiming returns the timing of each track in a segment.
func trafTiming(t *testing.T, segment []byte) map[uint32]trackTiming {
	t.Helper()

	moof := child(segment, 0, "moof")
	if moof == nil {
		t.Fatal("segment has no moof")
	}

	out := make(map[uint32]trackTiming)
	for _, traf := range children(moof, 8, "traf") {
		var track uint32
		var timing trackTiming

		if tfhd := child(traf, 8, "tfhd"); tfhd != nil {
			track = binary.BigEndian.Uint32(tfhd[12:16])
		}
		if tfdt := child(traf, 8, "tfdt"); tfdt != nil {
			timing.tfdt = int64(binary.BigEndian.Uint64(tfdt[12:20]))
		}
		if trun := child(traf, 8, "trun"); trun != nil {
			count := int(binary.BigEndian.Uint32(trun[12:16]))
			for i := 0; i < count; i++ {
				at := 20 + i*12
				duration := int64(binary.BigEndian.Uint32(trun[at : at+4]))
				timing.total += duration
				if duration > timing.max {
					timing.max = duration
				}
			}
		}

		out[track] = timing
	}

	return out
}

func TestIgnoresWhatItCannotPackage(t *testing.T) {
	s := New(DefaultWindow, false)

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
	s := New(DefaultWindow, false)
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
