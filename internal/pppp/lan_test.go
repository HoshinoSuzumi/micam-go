package pppp

import (
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// fakeCamera answers the LAN handshake the way a camera does, then keeps the
// socket open so a session could follow.
func fakeCamera(t *testing.T, reply byte) (*net.UDPConn, int) {
	t.Helper()

	cam, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("fake camera: %v", err)
	}
	t.Cleanup(func() { cam.Close() })

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
				cam.WriteToUDP(EncodeFrame(MsgPunchPkt, []byte{0xDE, 0xAD, 0xBE, 0xEF}), from)
			case MsgPunchPkt:
				cam.WriteToUDP(EncodeFrame(reply, nil), from)
				return
			}
		}
	}()

	return cam, cam.LocalAddr().(*net.UDPAddr).Port
}

func TestLANHandshake(t *testing.T) {
	cam, port := fakeCamera(t, MsgP2PRdy)

	conn, peer, err := LANHandshake(netip.MustParseAddr("127.0.0.1"), port, 2*time.Second, discardLogger())
	if err != nil {
		t.Fatalf("LANHandshake: %v", err)
	}
	defer conn.Close()

	if peer.Port != port {
		t.Fatalf("peer = %s, want the camera port %d", peer, port)
	}
	if !peer.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("peer ip = %s", peer.IP)
	}

	// The socket must be usable straight away, with no deadline left behind.
	if err := conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatalf("socket: %v", err)
	}
	_ = cam.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
}

func TestLANHandshakeReportsTCPOnlyCameras(t *testing.T) {
	_, port := fakeCamera(t, MsgP2PRdyTCP)

	_, _, err := LANHandshake(netip.MustParseAddr("127.0.0.1"), port, 2*time.Second, discardLogger())
	if err == nil {
		t.Fatal("a TCP-only camera must be reported as unsupported")
	}
	if !strings.Contains(err.Error(), "cs2+tcp") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLANHandshakeTimesOut(t *testing.T) {
	// Nothing listens here.
	cam, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := cam.LocalAddr().(*net.UDPAddr).Port
	cam.Close()

	start := time.Now()
	_, _, err = LANHandshake(netip.MustParseAddr("127.0.0.1"), port, 400*time.Millisecond, discardLogger())
	if err == nil {
		t.Fatal("a silent network must time out")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("the handshake took %s, want it bounded by the timeout", elapsed)
	}
}

func TestLANHandshakeRejectsIPv6(t *testing.T) {
	if _, _, err := LANHandshake(netip.MustParseAddr("::1"), LANPort, time.Second, discardLogger()); err == nil {
		t.Fatal("IPv6 must be rejected: CS2 is IPv4 only")
	}
}
