package pppp

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

const testUIDText = "XMSYCNR-145165-DSCRD"

// fakeCamera answers a broadcast search the way the real hardware does: the
// punch packet carries its UID, and the ready packet announces the transport.
// When serveTCP is set it also listens on TCP at the same port number, which is
// what a cs2+tcp camera does.
func fakeCamera(t *testing.T, uid UID, ready byte, serveTCP bool) *net.UDPAddr {
	t.Helper()

	cam, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("fake camera: %v", err)
	}
	t.Cleanup(func() { cam.Close() })

	addr := cam.LocalAddr().(*net.UDPAddr)
	punch := uid.Encode()

	var tcpListener net.Listener
	if serveTCP {
		tcpListener, err = net.Listen("tcp", addr.String())
		if err != nil {
			t.Fatalf("fake camera tcp: %v", err)
		}
		t.Cleanup(func() { tcpListener.Close() })

		go func() {
			for {
				conn, err := tcpListener.Accept()
				if err != nil {
					return
				}
				t.Cleanup(func() { conn.Close() })
			}
		}()
	}

	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := cam.ReadFromUDP(buf)
			if err != nil {
				return
			}
			frame, ok := ParseFrame(buf[:n])
			if !ok {
				continue
			}
			switch frame.Type {
			case MsgLANSrch:
				cam.WriteToUDP(EncodeFrame(MsgPunchPkt, punch[:]), from)
			case MsgPunchPkt:
				cam.WriteToUDP(EncodeFrame(ready, nil), from)
				return
			}
		}
	}()

	return addr
}

// isolate keeps the handshake on loopback: no broadcast packets leave the
// machine and the search goes to the fake camera's port.
func isolate(t *testing.T, port int) {
	t.Helper()
	oldPort, oldBroadcast := lanPort, lanBroadcast
	lanPort, lanBroadcast = port, false
	t.Cleanup(func() { lanPort, lanBroadcast = oldPort, oldBroadcast })
}

func TestLANHandshakeOverUDP(t *testing.T) {
	uid, err := ParseUID(testUIDText)
	if err != nil {
		t.Fatalf("ParseUID: %v", err)
	}
	cam := fakeCamera(t, uid, MsgP2PRdy, false)
	isolate(t, cam.Port)

	link, err := LANHandshake(uid, netip.MustParseAddr("127.0.0.1"), 2*time.Second, discardLogger())
	if err != nil {
		t.Fatalf("LANHandshake: %v", err)
	}
	defer link.Close()

	if link.Reliable() {
		t.Fatal("the UDP link must not claim reliable delivery")
	}
	if !strings.Contains(link.Describe(), "udp") {
		t.Fatalf("link = %s, want a udp link", link.Describe())
	}
}

func TestLANHandshakeOverTCP(t *testing.T) {
	uid, err := ParseUID(testUIDText)
	if err != nil {
		t.Fatalf("ParseUID: %v", err)
	}
	cam := fakeCamera(t, uid, MsgP2PRdyTCP, true)
	isolate(t, cam.Port)

	link, err := LANHandshake(uid, netip.MustParseAddr("127.0.0.1"), 3*time.Second, discardLogger())
	if err != nil {
		t.Fatalf("LANHandshake: %v", err)
	}
	defer link.Close()

	if !link.Reliable() {
		t.Fatal("the TCP link must report reliable delivery")
	}
	if !strings.Contains(link.Describe(), "tcp") {
		t.Fatalf("link = %s, want a tcp link", link.Describe())
	}
}

func TestLANHandshakeIgnoresOtherCameras(t *testing.T) {
	// The network answers, but with a different camera's UID.
	other, err := ParseUID("OTHERCAM-1-XXXXX")
	if err != nil {
		t.Fatalf("ParseUID: %v", err)
	}
	cam := fakeCamera(t, other, MsgP2PRdy, false)
	isolate(t, cam.Port)

	want, err := ParseUID(testUIDText)
	if err != nil {
		t.Fatalf("ParseUID: %v", err)
	}

	if _, err := LANHandshake(want, netip.MustParseAddr("127.0.0.1"), 1*time.Second, discardLogger()); err == nil {
		t.Fatal("a matching UID is required: attaching to the wrong camera would be worse than failing")
	} else if !strings.Contains(err.Error(), "none matched") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLANHandshakeTimesOut(t *testing.T) {
	// Grab a port, then close it so nothing answers.
	sock, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := sock.LocalAddr().(*net.UDPAddr).Port
	sock.Close()
	isolate(t, port)

	uid, err := ParseUID(testUIDText)
	if err != nil {
		t.Fatalf("ParseUID: %v", err)
	}

	start := time.Now()
	_, err = LANHandshake(uid, netip.MustParseAddr("127.0.0.1"), 500*time.Millisecond, discardLogger())
	if err == nil {
		t.Fatal("a silent network must time out")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("the handshake took %s, want it bounded by the timeout", elapsed)
	}
}

func TestLANHandshakeRejectsIPv6(t *testing.T) {
	uid, err := ParseUID(testUIDText)
	if err != nil {
		t.Fatalf("ParseUID: %v", err)
	}
	if _, err := LANHandshake(uid, netip.MustParseAddr("::1"), time.Second, discardLogger()); err == nil {
		t.Fatal("IPv6 must be rejected: CS2 is IPv4 only")
	}
}

func TestSearchTargetsCoverBroadcastAndUnicast(t *testing.T) {
	oldPort, oldBroadcast := lanPort, lanBroadcast
	lanPort, lanBroadcast = 32108, true
	t.Cleanup(func() { lanPort, lanBroadcast = oldPort, oldBroadcast })

	seen := make(map[string]bool)
	for _, target := range searchTargets(netip.MustParseAddr("192.168.0.109")) {
		seen[target.String()] = true
	}

	// Measured on real hardware: the camera ignores the unicast search and only
	// answers the broadcast.
	for _, want := range []string{"255.255.255.255:32108", "192.168.0.255:32108", "192.168.0.109:32108"} {
		if !seen[want] {
			t.Fatalf("missing search target %s (got %v)", want, seen)
		}
	}
}

// TestConnOverTCP checks that the session works over the TCP framing and that no
// DRW acknowledgements are sent, since TCP already guarantees delivery.
func TestConnOverTCP(t *testing.T) {
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

	// The camera pushes one channel-0 command.
	message := buildAuthMessage(t)
	payload := append([]byte{DRWMagic, ChannelCommand, 0x00, 0x01}, message...)
	if err := cameraLink.WriteFrame(EncodeFrame(MsgDRW, payload)); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	got, ok := conn.NextCommand(ctx)
	if !ok {
		t.Fatal("no command was delivered over TCP")
	}
	if len(got) != len(message)-4 {
		t.Fatalf("command = %d bytes, want %d", len(got), len(message)-4)
	}

	// No ack should come back; the only traffic allowed is a keepalive, and none
	// is due within this window.
	if err := cameraLink.SetReadDeadline(time.Now().Add(250 * time.Millisecond)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	buf := make([]byte, 512)
	if n, err := cameraLink.ReadFrame(buf); err == nil {
		t.Fatalf("expected no acknowledgement over TCP, got %x", buf[:n])
	}
}
