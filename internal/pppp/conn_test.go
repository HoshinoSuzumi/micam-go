package pppp

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"testing"
	"time"
)

// drwMessage wraps an application message in the length prefix the DRW channel
// uses to delimit messages.
func drwMessage(body string) []byte {
	out := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(out[:4], uint32(len(body)))
	copy(out[4:], body)
	return out
}

// A camera that vanishes without closing the connection - power loss, Wi-Fi drop -
// leaves a socket that never errors. The session must still end, or the relay sits
// in front of a frozen picture forever and never retries.
func TestConnWatchdogEndsAQuietSession(t *testing.T) {
	oldIdle, oldInterval := mediaIdleTimeout, watchdogInterval
	mediaIdleTimeout, watchdogInterval = 50*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { mediaIdleTimeout, watchdogInterval = oldIdle, oldInterval })

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	link, err := DialTCP(listener.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("DialTCP: %v", err)
	}

	conn := NewConn(link, Addr{}, discardLogger())
	conn.Start()
	defer conn.Close()

	camera, err := listener.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	cameraLink := &tcpLink{conn: camera}
	defer cameraLink.Close()

	// One media message, then silence, with the socket left open.
	payload := append([]byte{DRWMagic, ChannelMedia, 0x00, 0x01}, drwMessage("frame")...)
	if err := cameraLink.WriteFrame(EncodeFrame(MsgDRW, payload)); err != nil {
		t.Fatalf("write: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, ok := conn.NextMedia(ctx); !ok {
		t.Fatal("the media message was not delivered")
	}

	if _, ok := conn.NextMedia(ctx); ok {
		t.Fatal("the session kept delivering media after the camera went quiet")
	}
	if !errors.Is(conn.Err(), ErrMediaIdle) {
		t.Fatalf("Err() = %v, want ErrMediaIdle", conn.Err())
	}
}

// Before the first frame the channel is legitimately silent: the watchdog must not
// kill a session that is still completing the handshake.
func TestConnWatchdogWaitsForTheFirstFrame(t *testing.T) {
	oldIdle, oldInterval := mediaIdleTimeout, watchdogInterval
	mediaIdleTimeout, watchdogInterval = 30*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { mediaIdleTimeout, watchdogInterval = oldIdle, oldInterval })

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	link, err := DialTCP(listener.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("DialTCP: %v", err)
	}

	conn := NewConn(link, Addr{}, discardLogger())
	conn.Start()
	defer conn.Close()

	camera, err := listener.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	defer camera.Close()

	// A handshake-length silence, ten times the idle timeout, must be tolerated.
	select {
	case <-conn.Done():
		t.Fatal("the session ended before any media arrived")
	case <-time.After(300 * time.Millisecond):
	}
	if err := conn.Err(); err != nil {
		t.Fatalf("Err() = %v, want nil before the first frame", err)
	}
}

// The reason a session ended has to survive to the log: "stopped sending" with no
// cause is what made the original failure opaque.
func TestConnReportsWhyThePeerLeft(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	link, err := DialTCP(listener.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("DialTCP: %v", err)
	}

	conn := NewConn(link, Addr{}, discardLogger())
	conn.Start()
	defer conn.Close()

	camera, err := listener.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	cameraLink := &tcpLink{conn: camera}
	defer cameraLink.Close()

	if err := cameraLink.WriteFrame(EncodeFrame(MsgClose, nil)); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case <-conn.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the session did not end after the camera closed it")
	}
	if !errors.Is(conn.Err(), ErrPeerClosed) {
		t.Fatalf("Err() = %v, want ErrPeerClosed", conn.Err())
	}
}

// A local Close is not a failure and must not be reported as one.
func TestConnErrIsNilAfterALocalClose(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	link, err := DialTCP(listener.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("DialTCP: %v", err)
	}

	conn := NewConn(link, Addr{}, discardLogger())
	conn.Start()

	camera, err := listener.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	defer camera.Close()

	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := conn.Err(); err != nil {
		t.Fatalf("Err() = %v, want nil after a local close", err)
	}
}
