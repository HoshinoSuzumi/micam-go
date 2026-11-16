package pppp

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"time"
)

// LANPort is where a CS2 camera listens for a local-network search.
const LANPort = 32108

const (
	lanSearchInterval = 200 * time.Millisecond
	lanDefaultTimeout = 3 * time.Second
)

// LANHandshake opens a direct session with a camera on the local network.
//
// The exchange is four steps and needs no cloud involvement beyond the keys the
// caller already fetched:
//
//	-> camera:32108  F1 30 0000        LAN_SEARCH
//	<-                F1 41 ...        PUNCH_PKT
//	->                the same packet  (echoed verbatim)
//	<-                F1 42 ...        P2P_RDY, now from the session port
//
// From there the socket carries ordinary DRW/MISS traffic, exactly like the
// relay path: no relay, and none of the 0x28 linking that only relays need.
//
// The returned address is the peer to hand to NewConn.
func LANHandshake(camera netip.Addr, port int, timeout time.Duration, log *slog.Logger) (*net.UDPConn, *net.UDPAddr, error) {
	if !camera.Is4() {
		return nil, nil, errors.New("pppp: the LAN handshake needs an IPv4 camera address")
	}
	if port == 0 {
		port = LANPort
	}
	if timeout <= 0 {
		timeout = lanDefaultTimeout
	}
	if log == nil {
		log = slog.Default()
	}

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		return nil, nil, fmt.Errorf("pppp: cannot open a UDP socket: %w", err)
	}

	target := &net.UDPAddr{IP: net.IP(camera.AsSlice()), Port: port}

	punch, err := searchCamera(conn, target, timeout, log)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}

	peer, err := openSession(conn, target, punch, timeout, log)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}

	// The handshake left a read deadline behind; the session loop sets its own.
	_ = conn.SetReadDeadline(time.Time{})
	return conn, peer, nil
}

// searchCamera broadcasts LAN_SEARCH until the camera answers with PUNCH_PKT,
// and returns that packet so it can be echoed back.
func searchCamera(conn *net.UDPConn, target *net.UDPAddr, timeout time.Duration, log *slog.Logger) ([]byte, error) {
	search := EncodeFrame(MsgLANSrch, nil)
	buf := make([]byte, rxBufferSize)
	deadline := time.Now().Add(timeout)
	nextSend := time.Now()

	for time.Now().Before(deadline) {
		if err := pump(conn, target, search, &nextSend, deadline); err != nil {
			return nil, err
		}

		frame, from, ok, err := readFrom(conn, target, buf, lanSearchInterval)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if frame.Type != MsgPunchPkt {
			continue
		}

		log.Debug("camera answered the LAN search", "from", from.String())
		echo := make([]byte, len(frame.raw))
		copy(echo, frame.raw)
		return echo, nil
	}
	return nil, fmt.Errorf("pppp: no camera answered on %s", target.String())
}

// openSession echoes the punch packet until the camera reports P2P_RDY from the
// port the session will use.
func openSession(conn *net.UDPConn, target *net.UDPAddr, punch []byte, timeout time.Duration, log *slog.Logger) (*net.UDPAddr, error) {
	buf := make([]byte, rxBufferSize)
	deadline := time.Now().Add(timeout)
	nextSend := time.Now()

	for time.Now().Before(deadline) {
		if err := pump(conn, target, punch, &nextSend, deadline); err != nil {
			return nil, err
		}

		frame, from, ok, err := readFrom(conn, target, buf, lanSearchInterval)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}

		switch frame.Type {
		case MsgP2PRdy:
			peer := &net.UDPAddr{IP: from.IP, Port: from.Port}
			log.Debug("camera opened a direct session", "peer", peer.String())
			return peer, nil
		case MsgP2PRdyTCP:
			return nil, errors.New("pppp: the camera only offers cs2+tcp, which is not implemented yet")
		}
	}
	return nil, errors.New("pppp: the camera did not open a direct session")
}

func pump(conn *net.UDPConn, target *net.UDPAddr, packet []byte, nextSend *time.Time, deadline time.Time) error {
	if time.Now().Before(*nextSend) {
		return nil
	}
	if _, err := conn.WriteToUDP(packet, target); err != nil {
		return fmt.Errorf("pppp: LAN send failed: %w", err)
	}
	*nextSend = time.Now().Add(lanSearchInterval)
	if nextSend.After(deadline) {
		*nextSend = deadline
	}
	return nil
}

// lanFrame is a parsed frame that still remembers its raw bytes, because the
// punch packet has to be echoed verbatim.
type lanFrame struct {
	Frame
	raw []byte
}

func readFrom(conn *net.UDPConn, target *net.UDPAddr, buf []byte, window time.Duration) (lanFrame, *net.UDPAddr, bool, error) {
	if err := conn.SetReadDeadline(time.Now().Add(window)); err != nil {
		return lanFrame{}, nil, false, nil
	}
	n, from, err := conn.ReadFromUDP(buf)
	if err != nil {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return lanFrame{}, nil, false, nil
		}
		return lanFrame{}, nil, false, fmt.Errorf("pppp: LAN receive failed: %w", err)
	}

	// Only the camera we asked may answer.
	if !from.IP.Equal(target.IP) {
		return lanFrame{}, nil, false, nil
	}

	frame, ok := ParseFrame(buf[:n])
	if !ok {
		return lanFrame{}, nil, false, nil
	}

	raw := make([]byte, n)
	copy(raw, buf[:n])
	return lanFrame{Frame: frame, raw: raw}, from, true, nil
}
