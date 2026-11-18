package ts

import (
	"bytes"
	"os"
	"testing"

	"micam/internal/miss"
)

// TestAudioAppearsInThePMT checks the declaration: a private stream on its own
// PID, with the registration descriptor that names Opus.
func TestAudioAppearsInThePMT(t *testing.T) {
	var buf bytes.Buffer
	m := NewMuxerWithAudio(&buf, miss.CodecH265, 100, true)

	if err := m.Write(frame(0, true, 500, 0)); err != nil {
		t.Fatalf("write video: %v", err)
	}

	out := buf.Bytes()
	pmt := packetOfPID(t, out, pidPMT)
	if pmt == nil {
		t.Fatal("no PMT was written")
	}

	if want := pidAudio; !bytes.Contains(pmt, []byte{0xE0 | byte(want>>8), byte(want)}) {
		t.Fatalf("PMT does not point at PID 0x%04x: % x", want, pmt[:40])
	}
	if !bytes.Contains(pmt, []byte{streamTypePrivate}) {
		t.Fatal("PMT has no private stream type")
	}
	if !bytes.Contains(pmt, []byte("Opus")) {
		t.Fatal("PMT has no Opus registration descriptor")
	}
}

// TestAudioGoesOnItsOwnPID checks the payload side: one PES with stream_id 0xBD
// and a real length, on the audio PID.
func TestAudioGoesOnItsOwnPID(t *testing.T) {
	var buf bytes.Buffer
	m := NewMuxerWithAudio(&buf, miss.CodecH265, 100, true)

	if err := m.Write(frame(0, true, 500, 0)); err != nil {
		t.Fatalf("write video: %v", err)
	}
	if err := m.WriteAudio(miss.Packet{
		Codec:       miss.CodecOpus,
		TimestampMS: 40,
		Payload:     []byte{0xF8, 1, 2, 3},
	}); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	out := buf.Bytes()
	audio := packetOfPID(t, out, pidAudio)
	if audio == nil {
		t.Fatal("no packet on the audio PID")
	}

	// PUSI set, and the payload starts with a private-stream PES header. A short
	// PES gets an adaptation field for stuffing, so the payload does not always
	// begin at byte 4.
	if audio[1]&0x40 == 0 {
		t.Fatal("the audio packet does not start a PES")
	}
	offset := 4
	if (audio[3]>>4)&0x2 != 0 {
		offset = 5 + int(audio[4])
	}
	if offset >= len(audio) {
		t.Fatalf("adaptation field claims %d bytes", offset)
	}

	payload := audio[offset:]
	if len(payload) < 14 {
		t.Fatalf("audio payload is %d bytes, too short for a PES header", len(payload))
	}
	if payload[0] != 0x00 || payload[1] != 0x00 || payload[2] != 0x01 || payload[3] != streamIDPrivate {
		t.Fatalf("audio payload does not look like a PES: % x", payload[:8])
	}
	length := int(payload[4])<<8 | int(payload[5])
	if want := 3 + 5 + 4; length != want {
		t.Fatalf("PES length = %d, want %d", length, want)
	}
}

// TestAudioIsIgnoredWithoutTheFlag keeps a muxer built for video only from
// growing an extra stream behind the PMT's back.
func TestAudioIsIgnoredWithoutTheFlag(t *testing.T) {
	var buf bytes.Buffer
	m := NewMuxerWithLead(&buf, miss.CodecH265, 100)

	if err := m.Write(frame(0, true, 500, 0)); err != nil {
		t.Fatalf("write video: %v", err)
	}
	before := buf.Len()

	if err := m.WriteAudio(miss.Packet{Codec: miss.CodecOpus, Payload: []byte{0xF8}}); err != nil {
		t.Fatalf("write audio: %v", err)
	}
	if buf.Len() != before {
		t.Fatalf("a video-only muxer wrote %d extra bytes", buf.Len()-before)
	}
}

// TestAudioStreamAgainstACapture writes a real transport stream so ffprobe can be
// pointed at it; the container is the part that matters, the audio frames are
// synthetic.
func TestAudioStreamAgainstACapture(t *testing.T) {
	out := os.Getenv("MICAM_TS_OUT")
	if out == "" {
		t.Skip("set MICAM_TS_OUT to write a transport stream with an Opus track")
	}

	var buf bytes.Buffer
	m := NewMuxerWithAudio(&buf, miss.CodecH265, 100, true)

	for i := 0; i < 40; i++ {
		key := i == 0
		if err := m.Write(frame(uint32(i), key, 800, int64(i)*50)); err != nil {
			t.Fatalf("write video: %v", err)
		}
		if err := m.WriteAudio(miss.Packet{
			Codec:       miss.CodecOpus,
			TimestampMS: int64(i)*50 + 10,
			Payload:     append([]byte{0xF8}, make([]byte, 40)...),
		}); err != nil {
			t.Fatalf("write audio: %v", err)
		}
	}

	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write %s: %v", out, err)
	}
	t.Logf("wrote %d bytes to %s", buf.Len(), out)
}

// packetOfPID returns the first whole TS packet with this PID.
func packetOfPID(t *testing.T, ts []byte, pid uint16) []byte {
	t.Helper()

	for off := 0; off+packetSize <= len(ts); off += packetSize {
		if pidOf(ts, off/packetSize) == pid {
			return ts[off : off+packetSize]
		}
	}
	return nil
}
