package pppp

import (
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

const testUIDText = "XMSYCNR-145165-DSCRD"

// fakeCamera answers a broadcast search the way the real hardware does: the
// punch packet carries its UID, and P2P_RDY arrives from the session port.
func fakeCamera(t *testing.T, uid UID, ready byte) *net.UDPAddr {
	t.Helper()

	cam, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("fake camera: %v", err)
	}
	t.Cleanup(func() { cam.Close() })

	punch := uid.Encode()

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

	return cam.LocalAddr().(*net.UDPAddr)
}

// isolate keeps the handshake on loopback: no broadcast packets leave the
// machine and the search goes to the fake camera's port.
func isolate(t *testing.T, port int) {
	t.Helper()
	oldPort, oldBroadcast := lanPort, lanBroadcast
	lanPort, lanBroadcast = port, false
	t.Cleanup(func() { lanPort, lanBroadcast = oldPort, oldBroadcast })
}

func TestLANHandshake(t *testing.T) {
	uid, err := ParseUID(testUIDText)
	if err != nil {
		t.Fatalf("ParseUID: %v", err)
	}
	cam := fakeCamera(t, uid, MsgP2PRdy)
	isolate(t, cam.Port)

	conn, peer, err := LANHandshake(uid, netip.MustParseAddr("127.0.0.1"), 2*time.Second, discardLogger())
	if err != nil {
		t.Fatalf("LANHandshake: %v", err)
	}
	defer conn.Close()

	if peer.Port != cam.Port {
		t.Fatalf("peer = %s, want the camera port %d", peer, cam.Port)
	}
	if !peer.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("peer ip = %s", peer.IP)
	}
}

func TestLANHandshakeIgnoresOtherCameras(t *testing.T) {
	// The network answers, but with a different camera's UID.
	other, err := ParseUID("OTHERCAM-1-XXXXX")
	if err != nil {
		t.Fatalf("ParseUID: %v", err)
	}
	cam := fakeCamera(t, other, MsgP2PRdy)
	isolate(t, cam.Port)

	want, err := ParseUID(testUIDText)
	if err != nil {
		t.Fatalf("ParseUID: %v", err)
	}

	_, _, err = LANHandshake(want, netip.MustParseAddr("127.0.0.1"), 1*time.Second, discardLogger())
	if err == nil {
		t.Fatal("a matching UID is required: attaching to the wrong camera would be worse than failing")
	}
	if !strings.Contains(err.Error(), "none matched") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLANHandshakeReportsTCPOnlyCameras(t *testing.T) {
	uid, err := ParseUID(testUIDText)
	if err != nil {
		t.Fatalf("ParseUID: %v", err)
	}
	cam := fakeCamera(t, uid, MsgP2PRdyTCP)
	isolate(t, cam.Port)

	_, _, err = LANHandshake(uid, netip.MustParseAddr("127.0.0.1"), 2*time.Second, discardLogger())
	if err == nil {
		t.Fatal("a TCP-only camera must be reported as unsupported")
	}
	if !strings.Contains(err.Error(), "cs2+tcp") {
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
	_, _, err = LANHandshake(uid, netip.MustParseAddr("127.0.0.1"), 500*time.Millisecond, discardLogger())
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
	if _, _, err := LANHandshake(uid, netip.MustParseAddr("::1"), time.Second, discardLogger()); err == nil {
		t.Fatal("IPv6 must be rejected: CS2 is IPv4 only")
	}
}

func TestSearchTargetsCoverBroadcastAndUnicast(t *testing.T) {
	oldPort, oldBroadcast := lanPort, lanBroadcast
	lanPort, lanBroadcast = 32108, true
	t.Cleanup(func() { lanPort, lanBroadcast = oldPort, oldBroadcast })

	targets := searchTargets(netip.MustParseAddr("192.168.0.109"))
	seen := make(map[string]bool, len(targets))
	for _, target := range targets {
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
