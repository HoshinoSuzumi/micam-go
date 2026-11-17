package pppp

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// tcpFramed prepends the TCP header WriteFrame would send, so a test can hand the
// transport a frame in whatever pieces it likes.
func tcpFramed(frame []byte) []byte {
	out := make([]byte, tcpHeaderLen+len(frame))
	binary.BigEndian.PutUint16(out[0:2], uint16(len(frame)))
	out[2] = tcpMarker
	copy(out[tcpHeaderLen:], frame)
	return out
}

// A read deadline used to interrupt a frame in the middle, and the bytes already
// consumed were lost: the next header would then be payload, and the session
// would never parse a frame again. The link must resume instead.
func TestTCPLinkResumesAFrameAcrossAReadDeadline(t *testing.T) {
	server, station := net.Pipe()
	defer server.Close()
	defer station.Close()

	link := &tcpLink{conn: station}
	defer link.Close()

	frame := EncodeFrame(MsgDRW, []byte{0x01, 0x02, 0x03, 0x04, 0x05})
	framed := tcpFramed(frame)
	go func() {
		if _, err := server.Write(framed[:2]); err != nil {
			return
		}
		// Stall in the middle of the header so the read deadline fires there.
		time.Sleep(300 * time.Millisecond)
		_, _ = server.Write(framed[2:])
	}()

	buf := make([]byte, 512)
	if err := link.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	if _, err := link.ReadFrame(buf); err == nil {
		t.Fatal("the first read should have timed out in the middle of the header")
	}
	if link.hdrN != 2 {
		t.Fatalf("kept %d bytes of a 2-byte partial header", link.hdrN)
	}

	if err := link.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	n, err := link.ReadFrame(buf)
	if err != nil {
		t.Fatalf("cannot resume the interrupted frame: %v", err)
	}
	if !bytes.Equal(buf[:n], frame) {
		t.Fatalf("resumed frame = %x, want %x", buf[:n], frame)
	}
}

func TestTCPLinkReadsConsecutiveFrames(t *testing.T) {
	server, station := net.Pipe()
	defer server.Close()
	defer station.Close()

	link := &tcpLink{conn: station}
	defer link.Close()

	first := EncodeFrame(MsgDRW, []byte("one"))
	second := EncodeFrame(MsgAlive, nil)
	go func() {
		camera := &tcpLink{conn: server}
		_ = camera.WriteFrame(first)
		_ = camera.WriteFrame(second)
	}()

	if err := link.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("deadline: %v", err)
	}

	buf := make([]byte, 512)
	for i, want := range [][]byte{first, second} {
		n, err := link.ReadFrame(buf)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if !bytes.Equal(buf[:n], want) {
			t.Fatalf("frame %d = %x, want %x", i, buf[:n], want)
		}
	}
}
