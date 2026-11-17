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

	// Partial-frame state. A read deadline can interrupt a frame in the middle;
	// dropping the bytes already consumed would lose the framing for the rest of
	// the session, because the next "header" would be payload. Keeping them here
	// makes ReadFrame resumable instead.
	hdr   [tcpHeaderLen]byte
	hdrN  int
	body  []byte
	bodyN int
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

// ReadFrame returns one whole frame, resuming a frame whose read was interrupted.
func (l *tcpLink) ReadFrame(buf []byte) (int, error) {
	if l.hdrN < tcpHeaderLen {
		n, err := io.ReadFull(l.conn, l.hdr[l.hdrN:])
		l.hdrN += n
		if err != nil {
			return 0, err
		}

		size := int(binary.BigEndian.Uint16(l.hdr[0:2]))
		if size > len(buf) {
			l.hdrN = 0
			return 0, fmt.Errorf("pppp: tcp frame of %d bytes does not fit in a %d-byte buffer", size, len(buf))
		}
		if cap(l.body) < size {
			l.body = make([]byte, size)
		} else {
			l.body = l.body[:size]
		}
		l.bodyN = 0
	}

	if l.bodyN < len(l.body) {
		n, err := io.ReadFull(l.conn, l.body[l.bodyN:])
		l.bodyN += n
		if err != nil {
			return 0, err
		}
	}

	n := copy(buf, l.body)
	l.hdrN, l.bodyN = 0, 0
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
