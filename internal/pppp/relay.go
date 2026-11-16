package pppp

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"time"
)

// Ports on the CS2 relay infrastructure. Variables rather than constants so
// tests can bind ephemeral ports.
var (
	serverPort = 32100
	relayPort  = 25000
)

const (
	// discoveryWindow drains rendezvous replies. Each server issues a different
	// token per relay, so the whole window must be collected.
	discoveryWindow = 2500 * time.Millisecond
	helloWindow     = 2500 * time.Millisecond
	portWindow      = 2000 * time.Millisecond
	joinWindow      = 2000 * time.Millisecond

	discoveryRepeats = 4
	// sessionRepeats opens (and keeps open) the NAT mapping for the session
	// port. Skipping it leaves RLY_REQ_ACK arriving and RLY_RDY never.
	sessionRepeats = 24
	helloAttempts  = 4
	portAttempts   = 4
	joinAttempts   = 6

	rxBufferSize = 2048
)

// Session is an established relay session. The socket stays open and bound to
// the same local port, ready for the MISS session to run over it.
type Session struct {
	conn     *net.UDPConn
	uid      UID
	uidBytes [UIDLen]byte
	servers  []netip.Addr
	log      *slog.Logger

	relay *net.UDPAddr
	token [4]byte
}

// Dial runs the CS2 handshake documented in docs/PROTOCOL.md §8.5 and returns
// once the camera has joined the relay.
//
// The same socket must be handed to Conn afterwards: a new socket would present
// a different external endpoint and the session would not carry media.
func Dial(ctx context.Context, conn *net.UDPConn, uid UID, servers []netip.Addr,
	timeout time.Duration, log *slog.Logger) (*Session, error) {

	if conn == nil {
		return nil, errors.New("pppp: nil socket")
	}
	if len(servers) == 0 {
		return nil, errors.New("pppp: no rendezvous servers")
	}
	if log == nil {
		log = slog.Default()
	}
	if timeout <= 0 {
		timeout = 90 * time.Second
	}

	s := &Session{
		conn:     conn,
		uid:      uid,
		uidBytes: uid.Encode(),
		servers:  servers,
		log:      log,
	}

	deadline := time.Now().Add(timeout)
	lastFailure := "timed out before the first attempt"

	for round := 1; time.Now().Before(deadline); round++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if round > 1 {
			s.log.Debug("retrying relay discovery", "attempt", round)
		}

		candidates, err := s.discoverRelays(deadline)
		if err != nil {
			return nil, err
		}
		if len(candidates) == 0 {
			lastFailure = "no relay candidates returned - the camera may be offline"
			continue
		}

		chosen, err := s.greetRelays(candidates, deadline)
		if err != nil {
			return nil, err
		}
		if chosen == nil {
			lastFailure = "no RLY_HELLO_ACK - relays did not accept our registration"
			continue
		}

		if err := s.allocateSessionPort(chosen, deadline); err != nil {
			return nil, err
		}
		if s.relay == nil {
			lastFailure = "no RLY_PORT_ACK from " + chosen.IP.String()
			continue
		}

		joined, err := s.awaitDeviceJoin(deadline)
		if err != nil {
			return nil, err
		}
		if !joined {
			lastFailure = "no RLY_RDY - the camera never joined the relay"
			continue
		}

		s.log.Info("relay session established",
			"relay", s.relay.String(), "token", hex.EncodeToString(s.token[:]))
		return s, nil
	}

	return nil, errors.New("pppp: relay handshake failed: " + lastFailure)
}

// Relay returns the relay endpoint carrying the session.
func (s *Session) Relay() *net.UDPAddr { return s.relay }

// Token returns the session token the relay allocated.
func (s *Session) Token() [4]byte { return s.token }

// Socket returns the socket to hand to Conn.
func (s *Session) Socket() *net.UDPConn { return s.conn }

// ------------------------------------------------------------- discovery ---

type candidate struct {
	addr   *net.UDPAddr
	tokens [][4]byte
}

func (s *Session) discoverRelays(deadline time.Time) ([]candidate, error) {
	for _, server := range s.servers {
		to := &net.UDPAddr{IP: net.IP(server.AsSlice()), Port: serverPort}
		if err := s.send(to, MsgListReq1, s.uidBytes[:]); err != nil {
			return nil, err
		}
	}

	drainUntil := time.Now().Add(discoveryWindow)
	if drainUntil.After(deadline) {
		drainUntil = deadline
	}

	var out []candidate
	index := make(map[string]int)

	for {
		rx, err := s.receive(drainUntil)
		if err != nil {
			return nil, err
		}
		if rx == nil {
			break
		}
		if rx.frame.Type != MsgRelayTo || len(rx.frame.Payload) < 20 {
			continue
		}

		payload := rx.frame.Payload
		addr := DecodeAddr(payload[0:16])
		var token [4]byte
		copy(token[:], payload[16:20])

		key := addr.String()
		at, seen := index[key]
		if !seen {
			out = append(out, candidate{addr: &net.UDPAddr{
				IP:   net.IP(addr.IP.AsSlice()),
				Port: int(addr.Port),
			}})
			at = len(out) - 1
			index[key] = at
		}
		if !hasToken(out[at].tokens, token) {
			out[at].tokens = append(out[at].tokens, token)
		}
	}

	for _, c := range out {
		s.log.Debug("relay candidate", "addr", c.addr.String(), "tokens", len(c.tokens))
	}
	return out, nil
}

func hasToken(tokens [][4]byte, want [4]byte) bool {
	for _, t := range tokens {
		if t == want {
			return true
		}
	}
	return false
}

// ------------------------------------------------------------ registration ---

// registerWithRelays makes each relay aware of us before RLY_HELLO: a relay
// silently drops a greeting from a client it has not registered.
func (s *Session) registerWithRelays(candidates []candidate) error {
	for _, c := range candidates {
		for _, token := range c.tokens {
			body := registrationBody(token, s.uidBytes)
			for i := 0; i < discoveryRepeats; i++ {
				if err := s.sendRaw(c.addr, EncodeFrame(MsgRelayPkt, body)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// registrationBody is token(4) + uid(20) + four zero bytes.
func registrationBody(token [4]byte, uid [UIDLen]byte) []byte {
	body := make([]byte, 4+UIDLen+4)
	copy(body[0:4], token[:])
	copy(body[4:4+UIDLen], uid[:])
	return body
}

func (s *Session) greetRelays(candidates []candidate, deadline time.Time) (*net.UDPAddr, error) {
	for attempt := 1; attempt <= helloAttempts && time.Now().Before(deadline); attempt++ {
		// Re-send the registration each time: if the relay never saw it, the
		// greeting that follows is ignored for exactly the same reason.
		if err := s.registerWithRelays(candidates); err != nil {
			return nil, err
		}
		for _, c := range candidates {
			if err := s.send(c.addr, MsgRelayHello, nil); err != nil {
				return nil, err
			}
		}

		rx, err := s.awaitType(MsgRelayHelloAck, boundedSlice(deadline, helloWindow))
		if err != nil {
			return nil, err
		}
		if rx != nil {
			return &net.UDPAddr{IP: rx.from.IP, Port: relayPort}, nil
		}
		s.log.Debug("no RLY_HELLO_ACK yet", "attempt", attempt, "of", helloAttempts)
	}
	return nil, nil
}

func (s *Session) allocateSessionPort(chosen *net.UDPAddr, deadline time.Time) error {
	var ack *Frame
	for attempt := 1; attempt <= portAttempts && time.Now().Before(deadline); attempt++ {
		if err := s.send(chosen, MsgRelayPort, nil); err != nil {
			return err
		}
		rx, err := s.awaitType(MsgRelayPortAck, boundedSlice(deadline, portWindow))
		if err != nil {
			return err
		}
		if rx != nil && len(rx.frame.Payload) >= 6 {
			ack = &rx.frame
			break
		}
	}
	if ack == nil {
		s.relay = nil
		return nil
	}

	payload := ack.Payload
	copy(s.token[:], payload[0:4])
	// The session port is big-endian here, unlike the port inside an address
	// block.
	port := int(binary.BigEndian.Uint16(payload[4:6]))
	s.relay = &net.UDPAddr{IP: chosen.IP, Port: port}
	s.log.Debug("relay allocated a session port", "relay", s.relay.String())
	return nil
}

func (s *Session) awaitDeviceJoin(deadline time.Time) (bool, error) {
	request := make([]byte, UIDLen+AddrLen+4)
	copy(request[0:UIDLen], s.uidBytes[:])
	block := AddrFromUDP(s.relay).Encode()
	copy(request[UIDLen:UIDLen+AddrLen], block[:])
	copy(request[UIDLen+AddrLen:], s.token[:])

	keepAlive := EncodeFrame(MsgRelayPkt, registrationBody(s.token, s.uidBytes))

	for attempt := 1; attempt <= joinAttempts && time.Now().Before(deadline); attempt++ {
		for _, server := range s.servers {
			to := &net.UDPAddr{IP: net.IP(server.AsSlice()), Port: serverPort}
			if err := s.send(to, MsgRelayReq, request); err != nil {
				return false, err
			}
		}
		// Opens the mapping for the session port: under a symmetric NAT the relay
		// cannot reach us until we send there first.
		for i := 0; i < sessionRepeats; i++ {
			if err := s.sendRaw(s.relay, keepAlive); err != nil {
				return false, err
			}
		}

		rx, err := s.awaitType(MsgRelayRdy, boundedSlice(deadline, joinWindow))
		if err != nil {
			return false, err
		}
		if rx != nil {
			s.log.Debug("camera joined the relay", "attempt", attempt)
			return true, nil
		}
	}
	return false, nil
}

// --------------------------------------------------------------- transport ---

type rx struct {
	frame Frame
	from  *net.UDPAddr
}

func (s *Session) receive(deadline time.Time) (*rx, error) {
	buf := make([]byte, rxBufferSize)
	for time.Now().Before(deadline) {
		if err := s.conn.SetReadDeadline(deadline); err != nil {
			return nil, err
		}
		n, from, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				return nil, nil
			}
			return nil, fmt.Errorf("pppp: receive failed: %w", err)
		}
		frame, ok := ParseFrame(buf[:n])
		if !ok {
			continue
		}
		s.log.Debug("recv", "type", MsgName(frame.Type), "from", from.String(), "len", len(frame.Payload))
		return &rx{frame: frame, from: from}, nil
	}
	return nil, nil
}

func (s *Session) awaitType(want byte, deadline time.Time) (*rx, error) {
	for {
		rx, err := s.receive(deadline)
		if err != nil || rx == nil {
			return nil, err
		}
		if rx.frame.Type == want {
			return rx, nil
		}
	}
}

func (s *Session) send(to *net.UDPAddr, msgType byte, payload []byte) error {
	if err := s.sendRaw(to, EncodeFrame(msgType, payload)); err != nil {
		return err
	}
	s.log.Debug("send", "type", MsgName(msgType), "to", to.String())
	return nil
}

func (s *Session) sendRaw(to *net.UDPAddr, datagram []byte) error {
	if _, err := s.conn.WriteToUDP(datagram, to); err != nil {
		return fmt.Errorf("pppp: send to %s failed: %w", to, err)
	}
	return nil
}

// boundedSlice never runs past the overall deadline.
func boundedSlice(deadline time.Time, window time.Duration) time.Time {
	end := time.Now().Add(window)
	if end.After(deadline) {
		return deadline
	}
	return end
}

// AddrFromUDP converts a socket address to the PPPP form.
func AddrFromUDP(u *net.UDPAddr) Addr {
	if u == nil {
		return Addr{}
	}
	ip, ok := netip.AddrFromSlice(u.IP)
	if !ok {
		return Addr{}
	}
	return Addr{IP: ip.Unmap(), Port: uint16(u.Port)}
}
