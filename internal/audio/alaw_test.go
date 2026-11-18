package audio

import (
	"encoding/hex"
	"testing"
)

// referencePCM is ffmpeg's own decode of the 256 A-law octets 0x00..0xFF:
//
//	printf '\x00...\xff' > all.alaw
//	ffmpeg -f alaw -ar 8000 -ac 1 -i all.alaw -f s16le all.pcm
//
// Byte-for-byte agreement with the reference implementation is the only thing
// that makes a hand-rolled companding table trustworthy.
const referencePCM = "" +
	"80ea80eb80e880e980ee80ef80ec80ed80e280e380e080e180e680e780e480e540f5c0f540f4c0f440f7c0f740f6c0f640f1c0f140f0c0f040f3c0f340f2c0f2" +
	"00aa00ae00a200a600ba00be00b200b6008a008e00820086009a009e0092009600d500d700d100d300dd00df00d900db00c500c700c100c300cd00cf00c900cb" +
	"a8feb8fe88fe98fee8fef8fec8fed8fe28fe38fe08fe18fe68fe78fe48fe58fea8ffb8ff88ff98ffe8fff8ffc8ffd8ff28ff38ff08ff18ff68ff78ff48ff58ff" +
	"a0fae0fa20fa60faa0fbe0fb20fb60fba0f8e0f820f860f8a0f9e0f920f960f950fd70fd10fd30fdd0fdf0fd90fdb0fd50fc70fc10fc30fcd0fcf0fc90fcb0fc" +
	"80158014801780168011801080138012801d801c801f801e80198018801b801ac00a400ac00b400bc0084008c0094009c00e400ec00f400fc00c400cc00d400d" +
	"00560052005e005a00460042004e004a00760072007e007a00660062006e006a002b0029002f002d0023002100270025003b0039003f003d0033003100370035" +
	"58014801780168011801080138012801d801c801f801e80198018801b801a80158004800780068001800080038002800d800c800f800e80098008800b800a800" +
	"60052005e005a00560042004e004a00460072007e007a00760062006e006a006b0029002f002d0023002100270025002b0039003f003d0033003100370035003"

func TestDecodeALawMatchesFFmpeg(t *testing.T) {
	src := make([]byte, 256)
	for i := range src {
		src[i] = byte(i)
	}

	want, err := hex.DecodeString(referencePCM)
	if err != nil {
		t.Fatalf("bad fixture: %v", err)
	}
	if len(want) != 512 {
		t.Fatalf("fixture is %d bytes, want 512", len(want))
	}

	got := DecodeALaw(nil, src)
	if len(got) != len(want) {
		t.Fatalf("decoded %d bytes, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("octet 0x%02x: byte %d is 0x%02x, ffmpeg says 0x%02x", src[i/2], i, got[i], want[i])
		}
	}
}

func TestDecodeALawReusesTheBuffer(t *testing.T) {
	buf := make([]byte, 0, 8)
	out := DecodeALaw(buf, []byte{0x00, 0xFF})
	if &out[0] != &buf[:8][0] {
		t.Fatal("a caller-supplied buffer with enough room should be reused")
	}
	if len(out) != 4 {
		t.Fatalf("decoded %d bytes, want 4", len(out))
	}
}

func TestWAVHeader(t *testing.T) {
	h := WAVHeader(8000, 1)

	if len(h) != 44 {
		t.Fatalf("header is %d bytes, want 44", len(h))
	}
	if string(h[0:4]) != "RIFF" || string(h[8:12]) != "WAVE" {
		t.Fatalf("not a RIFF/WAVE header: % x", h[:12])
	}
	if string(h[12:16]) != "fmt " || string(h[36:40]) != "data" {
		t.Fatalf("chunks are wrong: % x", h)
	}
	if got := uint16(h[22]); got != 1 {
		t.Fatalf("channels = %d, want 1", got)
	}
	if got := binaryLE32(h[24:28]); got != 8000 {
		t.Fatalf("sample rate = %d, want 8000", got)
	}
	if got := binaryLE32(h[28:32]); got != 16000 {
		t.Fatalf("byte rate = %d, want 16000", got)
	}
	if got := uint16(h[32]); got != 2 {
		t.Fatalf("block align = %d, want 2", got)
	}
	if got := uint16(h[34]); got != 16 {
		t.Fatalf("bits per sample = %d, want 16", got)
	}
	// A stream never ends: both sizes must say "unknown" rather than 0, which
	// some players read as an empty file.
	if got := binaryLE32(h[4:8]); got != 0xFFFFFFFF {
		t.Fatalf("RIFF size = 0x%x, want 0xFFFFFFFF", got)
	}
	if got := binaryLE32(h[40:44]); got != 0xFFFFFFFF {
		t.Fatalf("data size = 0x%x, want 0xFFFFFFFF", got)
	}
}

func binaryLE32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}
