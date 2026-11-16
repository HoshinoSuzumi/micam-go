package ts

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"testing"

	"micam/internal/miss"
	"micam/internal/testvec"
)

// ------------------------------------------------------------- helpers ---

func muxPacket(t *testing.T, codec uint32, packets ...miss.Packet) []byte {
	t.Helper()
	var buf bytes.Buffer
	m := NewMuxer(&buf, codec)
	for _, p := range packets {
		if err := m.Write(p); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := m.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	return buf.Bytes()
}

func frame(seq uint32, keyframe bool, size int, timestampMS int64) miss.Packet {
	payload := make([]byte, size)
	copy(payload, []byte{0x00, 0x00, 0x00, 0x01})
	flags := uint32(0)
	if keyframe {
		flags = 1
	}
	return miss.Packet{Codec: miss.CodecH265, Seq: seq, Flags: flags, TimestampMS: timestampMS, Payload: payload}
}

func pidOf(ts []byte, packetIndex int) uint16 {
	off := packetIndex * packetSize
	return uint16(ts[off+1]&0x1F)<<8 | uint16(ts[off+2])
}

func firstPacketOf(t *testing.T, ts []byte, want uint16) int {
	t.Helper()
	for off := 0; off < len(ts); off += packetSize {
		if pidOf(ts, off/packetSize) == want {
			return off
		}
	}
	t.Fatalf("no packet for PID 0x%x", want)
	return -1
}

func readPCR(ts []byte, packetOffset int) int64 {
	at := packetOffset + 6 // skip the length byte and the flags byte
	return int64(ts[at])<<25 |
		int64(ts[at+1])<<17 |
		int64(ts[at+2])<<9 |
		int64(ts[at+3])<<1 |
		int64(ts[at+4]&0x80>>7)
}

func readPTS(ts []byte, packetOffset int) int64 {
	adaptationLength := int(ts[packetOffset+4])
	pes := packetOffset + 5 + adaptationLength
	at := pes + 9
	return int64(ts[at]&0x0E)<<29 |
		int64(ts[at+1])<<22 |
		int64(ts[at+2]&0xFE)<<14 |
		int64(ts[at+3])<<7 |
		int64(ts[at+4]&0xFE>>1)
}

func presentationTimestamps(ts []byte) []int64 {
	var out []int64
	for off := 0; off < len(ts); off += packetSize {
		unitStart := ts[off+1]&0x40 != 0
		if unitStart && pidOf(ts, off/packetSize) == pidVideo {
			out = append(out, readPTS(ts, off))
		}
	}
	return out
}

// ----------------------------------------------------------------- cases ---

func TestCRC32MPEG2(t *testing.T) {
	c := testvec.MustTS(t)
	got := fmt.Sprintf("%08x", crc32Mpeg([]byte(c.CRC.InputASCII)))
	if got != c.CRC.ExpectedHex {
		t.Fatalf("crc = %s, want %s", got, c.CRC.ExpectedHex)
	}
}

func TestTimestampEncoding(t *testing.T) {
	c := testvec.MustTS(t)

	for _, tc := range c.PTSCases {
		buf := make([]byte, 5)
		writePTS(buf, 0, tc.Value, 0x02)
		if got := hex.EncodeToString(buf); got != tc.Hex {
			t.Fatalf("%s: encoded %s, want %s", tc.ID, got, tc.Hex)
		}
		if got := decodePTS(buf); got != tc.Value {
			t.Fatalf("%s: round trip gave %d, want %d", tc.ID, got, tc.Value)
		}
	}

	for _, tc := range c.PCRCases {
		buf := encodePCR(tc.Value)
		if got := hex.EncodeToString(buf); got != tc.Hex {
			t.Fatalf("%s: encoded %s, want %s", tc.ID, got, tc.Hex)
		}
		if got := decodePCR(buf); got != tc.Value {
			t.Fatalf("%s: round trip gave %d, want %d", tc.ID, got, tc.Value)
		}
	}

	if c.Gap.DeltaMS*c.ClockHz/1000 != c.Gap.Ticks {
		t.Fatalf("fixture inconsistency: %dms at %dHz is not %d ticks",
			c.Gap.DeltaMS, c.ClockHz, c.Gap.Ticks)
	}
}

// decodePTS and encodePCR mirror what a demuxer does, so the round trip proves
// the marker bits are where a player expects them.
func decodePTS(b []byte) int64 {
	return int64(b[0]&0x0E)<<29 |
		int64(b[1])<<22 |
		int64(b[2]&0xFE)<<14 |
		int64(b[3])<<7 |
		int64(b[4]&0xFE>>1)
}

func encodePCR(value int64) []byte {
	return []byte{
		byte(value >> 25),
		byte(value >> 17),
		byte(value >> 9),
		byte(value >> 1),
		byte((value&1)<<7 | 0x7E),
		0x00,
	}
}

func decodePCR(b []byte) int64 {
	return int64(b[0])<<25 | int64(b[1])<<17 | int64(b[2])<<9 | int64(b[3])<<1 | int64(b[4]&0x80>>7)
}

func TestPTSLeadIsConfigurable(t *testing.T) {
	cases := []struct {
		lead int
		want int64
	}{
		{0, 0},
		{100, 9_000},
		{300, 27_000},
	}

	for _, tc := range cases {
		var buf bytes.Buffer
		muxer := NewMuxerWithLead(&buf, miss.CodecH265, tc.lead)
		if err := muxer.Write(frame(0, true, 400, 1000)); err != nil {
			t.Fatalf("Write: %v", err)
		}
		ts := buf.Bytes()
		off := firstPacketOf(t, ts, 0x0100)

		if got := readPTS(ts, off); got != tc.want {
			t.Fatalf("lead %dms produced PTS %d, want %d", tc.lead, got, tc.want)
		}
		if got := readPCR(ts, off); got != 0 {
			t.Fatalf("lead %dms moved the PCR to %d; it must stay at 0", tc.lead, got)
		}
	}
}

func TestPTSLeadIsClamped(t *testing.T) {
	low := NewMuxer(io.Discard, miss.CodecH265)
	if low.ptsOffset != int64(DefaultPTSLeadMS)*clockHz/1000 {
		t.Fatalf("default lead = %d ticks", low.ptsOffset)
	}

	negative := NewMuxerWithLead(io.Discard, miss.CodecH265, -50)
	if negative.ptsOffset != 0 {
		t.Fatalf("a negative lead must clamp to 0, got %d", negative.ptsOffset)
	}

	huge := NewMuxerWithLead(io.Discard, miss.CodecH265, 999_999)
	if huge.ptsOffset != int64(maxPTSLeadMS)*clockHz/1000 {
		t.Fatalf("a huge lead must clamp to %d ms, got %d ticks", maxPTSLeadMS, huge.ptsOffset)
	}
}

func TestEmitsWellFormedPackets(t *testing.T) {
	ts := muxPacket(t, miss.CodecH265,
		frame(0, true, 5000, 1000),
		frame(1, false, 300, 1050),
		frame(2, false, 300, 1100))

	if len(ts)%packetSize != 0 {
		t.Fatalf("%d bytes is not a whole number of %d-byte packets", len(ts), packetSize)
	}
	for off := 0; off < len(ts); off += packetSize {
		if ts[off] != syncByte {
			t.Fatalf("missing sync byte at offset %d", off)
		}
	}
}

func TestTablesComeFirst(t *testing.T) {
	c := testvec.MustTS(t)
	ts := muxPacket(t, miss.CodecH265, frame(0, true, 500, 1000))

	if got := pidOf(ts, 0); int(got) != c.PIDs.PAT {
		t.Fatalf("first packet PID = 0x%x, want PAT 0x%x", got, c.PIDs.PAT)
	}
	if got := pidOf(ts, 1); int(got) != c.PIDs.PMT {
		t.Fatalf("second packet PID = 0x%x, want PMT 0x%x", got, c.PIDs.PMT)
	}
	if got := int(ts[packetSize+c.PMTOff]); got != c.Streams.H265 {
		t.Fatalf("stream_type = 0x%x, want H.265 0x%x", got, c.Streams.H265)
	}
}

func TestDeclaresH264(t *testing.T) {
	c := testvec.MustTS(t)
	ts := muxPacket(t, miss.CodecH264, frame(0, true, 400, 1000))
	if got := int(ts[packetSize+c.PMTOff]); got != c.Streams.H264 {
		t.Fatalf("stream_type = 0x%x, want H.264 0x%x", got, c.Streams.H264)
	}
}

func TestContinuityCounters(t *testing.T) {
	ts := muxPacket(t, miss.CodecH265, frame(0, true, 4000, 1000), frame(1, false, 4000, 1050))

	var counters []byte
	for off := 0; off < len(ts); off += packetSize {
		if pidOf(ts, off/packetSize) == 0x0100 {
			counters = append(counters, ts[off+3]&0x0F)
		}
	}
	if len(counters) <= 4 {
		t.Fatalf("expected several video packets, got %d", len(counters))
	}
	for i := 1; i < len(counters); i++ {
		if counters[i] != (counters[i-1]+1)&0x0F {
			t.Fatalf("continuity counter jumped at index %d: %d -> %d", i, counters[i-1], counters[i])
		}
	}
}

func TestKeyframeIsMarked(t *testing.T) {
	ts := muxPacket(t, miss.CodecH265, frame(0, true, 500, 1000))
	off := firstPacketOf(t, ts, 0x0100)

	if got := (ts[off+3] >> 4) & 0x03; got != 0x03 {
		t.Fatalf("adaptation control = %d, want 3 (adaptation field plus payload)", got)
	}
	if ts[off+5]&0x40 == 0 {
		t.Fatal("random_access_indicator must be set on a keyframe")
	}
	if ts[off+5]&0x10 == 0 {
		t.Fatal("PCR_flag must be set on the first packet of an access unit")
	}
}

func TestTimestampsAreRebased(t *testing.T) {
	lead := int64(DefaultPTSLeadMS) * clockHz / 1000

	huge := int64(167_947_400) // large enough to overflow a 33-bit PTS
	ts := muxPacket(t, miss.CodecH265,
		frame(0, true, 500, huge),
		frame(1, false, 500, huge+50))

	pts := readPTS(ts, firstPacketOf(t, ts, 0x0100))
	if pts >= 1<<33 {
		t.Fatalf("PTS %d does not fit in 33 bits", pts)
	}
	if pts != lead {
		t.Fatalf("first PTS = %d, want %d", pts, lead)
	}

	if got := readPCR(ts, firstPacketOf(t, ts, 0x0100)); got != 0 {
		t.Fatalf("PCR = %d, want 0", got)
	}
	if delta := pts - readPCR(ts, firstPacketOf(t, ts, 0x0100)); delta != lead {
		t.Fatalf("PTS leads PCR by %d ticks, want %d", delta, lead)
	}
}

func TestClockSurvivesRepeatsAndBackwards(t *testing.T) {
	c := testvec.MustTS(t)
	ts := muxPacket(t, miss.CodecH265,
		frame(0, true, 400, 100_000),
		frame(1, false, 400, 100_039),
		frame(2, false, 400, 100_092),
		frame(3, false, 400, 100_145),
		frame(4, false, 400, 100_142), // backwards
		frame(5, false, 400, 100_242),
		frame(6, false, 400, 100_242), // repeated
		frame(7, false, 400, 100_242)) // repeated

	pts := presentationTimestamps(ts)
	if len(pts) != 8 {
		t.Fatalf("%d PES starts, want 8", len(pts))
	}
	for i := 1; i < len(pts); i++ {
		if pts[i] <= pts[i-1] {
			t.Fatalf("PTS must increase at frame %d (%d -> %d)", i, pts[i-1], pts[i])
		}
	}

	// The repaired elapsed values are the fixture's, offset by the PTS lead.
	lead := int64(DefaultPTSLeadMS) * clockHz / 1000
	for i, elapsed := range c.Monotonic.Elapsed {
		want := lead + elapsed*c.ClockHz/1000
		if pts[i] != want {
			t.Fatalf("frame %d PTS = %d, want %d (elapsed %dms)", i, pts[i], want, elapsed)
		}
	}
}

func TestNegativeFirstTimestampStillAdvances(t *testing.T) {
	ts := muxPacket(t, miss.CodecH265,
		frame(0, true, 400, -1),
		frame(1, false, 400, -1),
		frame(2, false, 400, 5),
		frame(3, false, 400, 60))

	pts := presentationTimestamps(ts)
	if len(pts) != 4 {
		t.Fatalf("%d PES starts, want 4", len(pts))
	}
	for i := 1; i < len(pts); i++ {
		if pts[i] <= pts[i-1] {
			t.Fatalf("PTS must advance at frame %d (%d -> %d)", i, pts[i-1], pts[i])
		}
	}
}

func TestFollowsAPlausibleCameraClock(t *testing.T) {
	c := testvec.MustTS(t)
	ts := muxPacket(t, miss.CodecH265,
		frame(0, true, 400, 1_000_000),
		frame(1, false, 400, 1_000_080))

	pts := presentationTimestamps(ts)
	if len(pts) != 2 {
		t.Fatalf("%d PES starts, want 2", len(pts))
	}
	if delta := pts[1] - pts[0]; delta != c.Gap.DeltaMS*c.ClockHz/1000 {
		t.Fatalf("80ms gap became %d ticks, want %d", delta, c.Gap.Ticks)
	}
}

func TestTablesAreRepeated(t *testing.T) {
	var buf bytes.Buffer
	m := NewMuxer(&buf, miss.CodecH265)
	for i := 0; i < tableIntervalFrames+5; i++ {
		if err := m.Write(frame(uint32(i), false, 200, int64(1000+i*50))); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	patCount := 0
	ts := buf.Bytes()
	for off := 0; off < len(ts); off += packetSize {
		if pidOf(ts, off/packetSize) == 0x0000 {
			patCount++
		}
	}
	if patCount != 2 {
		t.Fatalf("PAT appeared %d times, want 2 (first frame and frame %d)", patCount, tableIntervalFrames)
	}
}
