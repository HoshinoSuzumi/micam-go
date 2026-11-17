package hls

import (
	"encoding/hex"
	"testing"
)

// The parameter sets of a xiaomi.camera.92ac1 at 2880x1624, captured from the
// camera. They are the only input the init segment needs, so keeping them here
// makes the whole packaging path testable without a device.
const (
	vpsHex = "40010c01ffff016000000300900000030000030096aa0240"
	spsHex = "420101016000000300900000030000030096a00168200659f796aa493088040000030004000003005020"
	ppsHex = "4401c072f02240"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad fixture: %v", err)
	}
	return b
}

func TestSplitNALs(t *testing.T) {
	// Three-byte and four-byte start codes, with a trailing zero that belongs to
	// the next start code rather than to the NAL.
	data := []byte{
		0x00, 0x00, 0x00, 0x01, 0x40, 0x01, 0xAA,
		0x00, 0x00, 0x01, 0x42, 0x01, 0xBB, 0xCC,
		0x00, 0x00, 0x00, 0x01, 0x44, 0x01,
	}

	nals := splitNALs(data)
	if len(nals) != 3 {
		t.Fatalf("got %d NALs, want 3: %x", len(nals), nals)
	}
	if got := nalType(nals[0]); got != nalTypeVPS {
		t.Fatalf("first NAL is type %d, want VPS(%d)", got, nalTypeVPS)
	}
	if got := nalType(nals[1]); got != nalTypeSPS {
		t.Fatalf("second NAL is type %d, want SPS(%d)", got, nalTypeSPS)
	}
	if len(nals[2]) != 2 {
		t.Fatalf("trailing NAL is %d bytes, want 2", len(nals[2]))
	}
}

func TestSplitNALsHandlesMissingStartCode(t *testing.T) {
	if got := splitNALs([]byte{0x40, 0x01, 0xAA}); len(got) != 0 {
		t.Fatalf("got %d NALs from data without a start code, want 0", len(got))
	}
}

func TestParseSPS(t *testing.T) {
	info, err := parseSPS(mustHex(t, spsHex))
	if err != nil {
		t.Fatalf("parseSPS: %v", err)
	}

	// Main profile, level 5.0, 4:2:0, 8-bit, and the coded size the camera
	// reports. The coded height is 1624; the conformance window crops it to the
	// 1620 a player displays.
	if info.ProfileIDC != 1 {
		t.Errorf("profile_idc = %d, want 1 (Main)", info.ProfileIDC)
	}
	if info.LevelIDC != 150 {
		t.Errorf("level_idc = %d, want 150 (5.0)", info.LevelIDC)
	}
	if info.ChromaFormat != 1 {
		t.Errorf("chroma_format = %d, want 1 (4:2:0)", info.ChromaFormat)
	}
	if info.BitDepthLuma != 0 || info.BitDepthChroma != 0 {
		t.Errorf("bit depths = %d/%d, want 8/8", info.BitDepthLuma+8, info.BitDepthChroma+8)
	}
	if info.Width != 2880 || info.Height != 1624 {
		t.Errorf("coded size = %dx%d, want 2880x1624", info.Width, info.Height)
	}
	if info.TierFlag != 0 {
		t.Errorf("tier = %d, want 0 (Main)", info.TierFlag)
	}
}

func TestParseSPSRejectsGarbage(t *testing.T) {
	if _, err := parseSPS([]byte{0x42, 0x01}); err == nil {
		t.Fatal("a two-byte SPS should not parse")
	}
	if _, err := parseSPS([]byte{0x42, 0x01, 0xFF, 0xFF, 0xFF}); err == nil {
		t.Fatal("random bytes should not parse")
	}
}

func TestUnescape(t *testing.T) {
	// 00 00 03 is emulation prevention; the parser must see 00 00.
	in := []byte{0x01, 0x00, 0x00, 0x03, 0x02}
	out := unescape(in)
	want := []byte{0x01, 0x00, 0x00, 0x02}
	if len(out) != len(want) {
		t.Fatalf("got %x, want %x", out, want)
	}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("got %x, want %x", out, want)
		}
	}
}
