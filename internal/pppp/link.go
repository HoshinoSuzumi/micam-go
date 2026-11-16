package pppp

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"
)

// Link carries PPPP frames. The relay path is always UDP; a camera on the local
// network may insist on TCP instead, which is what P2P_RDY_TCP announces.
type Link interface {
	// ReadFrame reads one frame into buf and returns its length.
	ReadFrame(buf []byte) (int, error)
	// WriteFrame writes one whole frame.
	WriteFrame(frame []byte) error
	SetReadDeadline(t time.Time) error
	Close() error
	// Reliable reports whether the transport guarantees delivery, in which case
	// DRW acknowledgements and sequence tracking are unnecessary.
	Reliable() bool
	Describe() string
}

// ------------------------------------------------------------------- UDP ---

type udpLink struct {
	conn *net.UDPConn
	peer *net.UDPAddr
}

// NewUDPLink wraps a connected UDP socket and its peer.
func NewUDPLink(conn *net.UDPConn, peer *net.UDPAddr) Link {
	return &udpLink{conn: conn, peer: peer}
}

func (l *udpLink) ReadFrame(buf []byte) (int, error) {
	n, _, err := l.conn.ReadFromUDP(buf)
	return n, err
}

func (l *udpLink) WriteFrame(frame []byte) error {
	_, err := l.conn.WriteToUDP(frame, l.peer)
	return err
}

func (l *udpLink) SetReadDeadline(t time.Time) error { return l.conn.SetReadDeadline(t) }
func (l *udpLink) Close() error                      { return l.conn.Close() }
func (l *udpLink) Reliable() bool                    { return false }

func (l *udpLink) Describe() string {
	if l.peer == nil {
		return "udp"
	}
	return "udp " + l.peer.String()
}

// ------------------------------------------------------------------- TCP ---

// tcpMarker sits in the fixed 8-byte TCP header. The first two bytes carry the
// payload length; the remaining five are unused.
const tcpMarker byte = 0x68

// tcpHeaderLen is the framing overhead on a TCP link.
const tcpHeaderLen = 8

type tcpLink struct {
	conn net.Conn
}

// DialTCP opens the TCP transport a camera offered in P2P_RDY_TCP.
func DialTCP(addr string, timeout time.Duration) (Link, error) {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, fmt.Errorf("pppp: cannot open the TCP session to %s: %w", addr, err)
	}
	return &tcpLink{conn: conn}, nil
}

func (l *tcpLink) ReadFrame(buf []byte) (int, error) {
	var header [tcpHeaderLen]byte
	if _, err := io.ReadFull(l.conn, header[:]); err != nil {
		return 0, err
	}

	n := int(binary.BigEndian.Uint16(header[0:2]))
	if n == 0 {
		return 0, nil
	}
	if n > len(buf) {
		return 0, fmt.Errorf("pppp: tcp frame of %d bytes does not fit in a %d-byte buffer", n, len(buf))
	}
	if _, err := io.ReadFull(l.conn, buf[:n]); err != nil {
		return 0, err
	}
	return n, nil
}

func (l *tcpLink) WriteFrame(frame []byte) error {
	buf := make([]byte, tcpHeaderLen+len(frame))
	binary.BigEndian.PutUint16(buf[0:2], uint16(len(frame)))
	buf[2] = tcpMarker
	copy(buf[tcpHeaderLen:], frame)

	_, err := l.conn.Write(buf)
	return err
}

func (l *tcpLink) SetReadDeadline(t time.Time) error { return l.conn.SetReadDeadline(t) }
func (l *tcpLink) Close() error                      { return l.conn.Close() }
func (l *tcpLink) Reliable() bool                    { return true }

func (l *tcpLink) Describe() string {
	if l.conn.RemoteAddr() == nil {
		return "tcp"
	}
	return "tcp " + l.conn.RemoteAddr().String()
}
