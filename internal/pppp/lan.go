package pppp

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"time"
)

// LANPort is where a CS2 camera listens for a local-network search.
const LANPort = 32108

const (
	lanSearchInterval = 200 * time.Millisecond
	lanDefaultTimeout = 3 * time.Second
	lanTCPTimeout     = 1500 * time.Millisecond
)

// Variables so tests can keep packets on loopback.
var (
	lanPort      = LANPort
	lanBroadcast = true
)

// LANHandshake opens a direct session with a camera on the local network.
//
// Cameras answer a broadcast search, not a unicast one: measured on a
// xiaomi.camera.92ac1, a packet sent straight to its address is ignored while
// the same packet to the broadcast address is answered within a millisecond.
//
//	-> 255.255.255.255:32108  F1 30 0000        LAN_SEARCH
//	<-                        F1 41 ...        PUNCH_PKT, payload = the camera UID
//	->                        the same packet  (echoed verbatim, to its source port)
//	<-                        F1 42 / F1 43    P2P_RDY over UDP, or P2P_RDY_TCP
//
// The UID in the punch packet is what identifies the camera: several cameras on
// one network all answer, and attaching to the wrong one would be worse than
// failing.
func LANHandshake(uid UID, hint netip.Addr, timeout time.Duration, log *slog.Logger) (Link, error) {
	if hint.IsValid() && !hint.Is4() {
		return nil, errors.New("pppp: the LAN handshake needs an IPv4 camera address")
	}
	if timeout <= 0 {
		timeout = lanDefaultTimeout
	}
	if log == nil {
		log = slog.Default()
	}

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		return nil, fmt.Errorf("pppp: cannot open a UDP socket: %w", err)
	}
	if err := enableBroadcast(conn); err != nil {
		log.Debug("cannot enable broadcast, the search may be limited to unicast", "err", err)
	}

	want := uid.Encode()
	punch, from, err := searchCamera(conn, hint, want, timeout, log)
	if err != nil {
		conn.Close()
		return nil, err
	}

	link, err := openSession(conn, from, punch, timeout, log)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return link, nil
}

// searchTargets is every address worth asking: the limited broadcast, the
// directed broadcast of the camera's /24, and the camera itself.
func searchTargets(hint netip.Addr) []*net.UDPAddr {
	var out []*net.UDPAddr

	if lanBroadcast {
		out = append(out, &net.UDPAddr{IP: net.IPv4bcast, Port: lanPort})
		if hint.Is4() {
			o := hint.As4()
			out = append(out, &net.UDPAddr{
				IP:   net.IPv4(o[0], o[1], o[2], 255),
				Port: lanPort,
			})
		}
	}
	if hint.Is4() {
		out = append(out, &net.UDPAddr{IP: net.IP(hint.AsSlice()), Port: lanPort})
	}
	return out
}

// searchCamera broadcasts LAN_SEARCH until a punch packet carrying our UID
// arrives, and returns that packet so it can be echoed back.
func searchCamera(conn *net.UDPConn, hint netip.Addr, uid [UIDLen]byte, timeout time.Duration, log *slog.Logger) ([]byte, *net.UDPAddr, error) {
	search := EncodeFrame(MsgLANSrch, nil)
	targets := searchTargets(hint)
	buf := make([]byte, rxBufferSize)
	deadline := time.Now().Add(timeout)
	nextSend := time.Now()

	answers := 0
	for time.Now().Before(deadline) {
		if !time.Now().Before(nextSend) {
			for _, target := range targets {
				if _, err := conn.WriteToUDP(search, target); err != nil {
					log.Debug("LAN search send failed", "to", target.String(), "err", err)
				}
			}
			nextSend = time.Now().Add(lanSearchInterval)
		}

		frame, from, ok := readReply(conn, nextSend, buf)
		if !ok {
			continue
		}
		if frame.Type != MsgPunchPkt {
			continue
		}

		answers++
		if !bytes.Equal(frame.Payload, uid[:]) {
			// Another camera on the same network, or a different device.
			log.Debug("LAN answer from a different device",
				"from", from.String(), "id", hex.EncodeToString(frame.Payload))
			continue
		}

		log.Debug("camera answered the LAN search", "from", from.String())
		echo := make([]byte, len(frame.raw))
		copy(echo, frame.raw)
		return echo, from, nil
	}

	if answers > 0 {
		return nil, nil, fmt.Errorf("pppp: %d device(s) answered but none matched %s", answers, uid)
	}
	return nil, nil, errors.New("pppp: no camera answered the LAN search")
}

// openSession echoes the punch packet until the camera reports which transport
// carries the session.
func openSession(conn *net.UDPConn, camera *net.UDPAddr, punch []byte, timeout time.Duration, log *slog.Logger) (Link, error) {
	buf := make([]byte, rxBufferSize)
	deadline := time.Now().Add(timeout)
	nextSend := time.Now()

	for time.Now().Before(deadline) {
		if !time.Now().Before(nextSend) {
			if _, err := conn.WriteToUDP(punch, camera); err != nil {
				return nil, fmt.Errorf("pppp: LAN send failed: %w", err)
			}
			nextSend = time.Now().Add(lanSearchInterval)
		}

		frame, from, ok := readReply(conn, nextSend, buf)
		if !ok {
			continue
		}

		switch frame.Type {
		case MsgP2PRdy:
			peer := &net.UDPAddr{IP: from.IP, Port: from.Port}
			log.Debug("camera opened a direct session", "peer", peer.String(), "transport", "udp")
			return &udpLink{conn: conn, peer: peer}, nil

		case MsgP2PRdyTCP:
			// The camera will not carry media over UDP. Connect over TCP instead
			// and let the search socket go.
			link, err := dialSessionTCP(frame.Payload, from, camera, log)
			if err != nil {
				return nil, err
			}
			conn.Close()
			return link, nil
		}
	}
	return nil, errors.New("pppp: the camera did not open a direct session")
}

// tcpCandidates lists plausible TCP endpoints, most likely first: where the
// P2P_RDY_TCP came from, the punch address, then the compact address the packet
// may embed.
func tcpCandidates(payload []byte, from, camera *net.UDPAddr) []string {
	var out []string
	add := func(addr string) {
		if addr == "" {
			return
		}
		for _, existing := range out {
			if existing == addr {
				return
			}
		}
		out = append(out, addr)
	}

	add(from.String())
	add(camera.String())

	// The compact form is the same 8 bytes the 0x28 reply uses: 02 00, port
	// big-endian, IPv4 in order.
	if len(payload) >= 32 && payload[24] == 0x02 && payload[25] == 0x00 {
		port := int(binary.BigEndian.Uint16(payload[26:28]))
		ip := net.IPv4(payload[28], payload[29], payload[30], payload[31])
		if port > 0 && !ip.Equal(net.IPv4zero) {
			add(net.JoinHostPort(ip.String(), strconv.Itoa(port)))
		}
	}
	return out
}

func dialSessionTCP(payload []byte, from, camera *net.UDPAddr, log *slog.Logger) (Link, error) {
	candidates := tcpCandidates(payload, from, camera)
	for _, addr := range candidates {
		link, err := DialTCP(addr, lanTCPTimeout)
		if err == nil {
			log.Debug("camera opened a direct session", "peer", addr, "transport", "tcp")
			return link, nil
		}
		log.Debug("TCP attempt failed", "addr", addr, "err", err)
	}
	return nil, fmt.Errorf("pppp: the camera offers cs2+tcp but none of %v accepted a connection", candidates)
}

// lanFrame keeps the raw datagram because the punch packet must be echoed
// verbatim.
type lanFrame struct {
	Frame
	raw []byte
}

func readReply(conn *net.UDPConn, until time.Time, buf []byte) (lanFrame, *net.UDPAddr, bool) {
	deadline := until
	if deadline.After(time.Now().Add(lanSearchInterval)) {
		deadline = time.Now().Add(lanSearchInterval)
	}
	if err := conn.SetReadDeadline(deadline); err != nil {
		return lanFrame{}, nil, false
	}

	n, from, err := conn.ReadFromUDP(buf)
	if err != nil {
		return lanFrame{}, nil, false
	}

	frame, ok := ParseFrame(buf[:n])
	if !ok {
		return lanFrame{}, nil, false
	}

	raw := make([]byte, n)
	copy(raw, buf[:n])
	return lanFrame{Frame: frame, raw: raw}, from, true
}
