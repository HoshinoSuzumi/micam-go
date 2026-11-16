package pppp

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"testing"
	"time"

	"micam/internal/testvec"
)

// The handshake is all timing and unacknowledged UDP, so it cannot be checked by
// inspecting data structures. This test stands up a fake rendezvous server, a
// fake relay and a fake camera session on the loopback interface and drives the
// whole nine-step exchange.

func listenUDP(t *testing.T) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func portOf(conn *net.UDPConn) int {
	return conn.LocalAddr().(*net.UDPAddr).Port
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// readUntil returns the first frame of the given type, or nil on timeout.
func readUntil(conn *net.UDPConn, want byte, timeout time.Duration) *Frame {
	deadline := time.Now().Add(timeout)
	buf := make([]byte, 4096)
	for time.Now().Before(deadline) {
		conn.SetReadDeadline(deadline)
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				return nil
			}
			return nil
		}
		frame, ok := ParseFrame(buf[:n])
		if !ok || frame.Type != want {
			continue
		}
		got := frame
		return &got
	}
	return nil
}

func TestRelayHandshakeAndSession(t *testing.T) {
	v := testvec.MustLoad(t)
	setupRequest := mustHex(t, v.Setup.RequestHex)

	rendezvous := listenUDP(t)
	relay := listenUDP(t)
	session := listenUDP(t)

	oldServer, oldRelay := serverPort, relayPort
	serverPort, relayPort = portOf(rendezvous), portOf(relay)
	t.Cleanup(func() { serverPort, relayPort = oldServer, oldRelay })

	uid, err := ParseUID("ABCDEF-123456-GHIJK")
	if err != nil {
		t.Fatalf("ParseUID: %v", err)
	}
	uidBytes := uid.Encode()

	token := [4]byte{0xAA, 0xBB, 0xCC, 0xDD}
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })

	// The session token the relay will allocate.
	sessionPayload := append([]byte{}, token[:]...)
	sessionPayload = append(sessionPayload, byte(portOf(session)>>8), byte(portOf(session)))

	// ---- fake rendezvous: answers LIST_REQ1 with RLY_TO ----
	go func() {
		buf := make([]byte, 2048)
		for {
			select {
			case <-stop:
				return
			default:
			}
			rendezvous.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
			n, from, err := rendezvous.ReadFromUDP(buf)
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return
				}
				continue
			}
			frame, ok := ParseFrame(buf[:n])
			if !ok || frame.Type != MsgListReq1 {
				continue
			}

			relayAddr, err := NewAddr("127.0.0.1", uint16(portOf(relay)))
			if err != nil {
				return
			}
			block := relayAddr.Encode()
			payload := append(block[:], token[:]...)
			rendezvous.WriteToUDP(EncodeFrame(MsgRelayTo, payload), from)
		}
	}()

	// ---- fake relay: registration, greeting, session port ----
	go func() {
		buf := make([]byte, 2048)
		for {
			select {
			case <-stop:
				return
			default:
			}
			relay.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
			n, from, err := relay.ReadFromUDP(buf)
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return
				}
				continue
			}
			frame, ok := ParseFrame(buf[:n])
			if !ok {
				continue
			}
			switch frame.Type {
			case MsgRelayHello:
				relay.WriteToUDP(EncodeFrame(MsgRelayHelloAck, nil), from)
			case MsgRelayPort:
				relay.WriteToUDP(EncodeFrame(MsgRelayPortAck, sessionPayload), from)
			}
		}
	}()

	// ---- fake camera: joins the session port, then drives the data path ----
	authMessage := buildAuthMessage(t)
	drwPayload := append([]byte{DRWMagic, ChannelCommand, 0, 0}, authMessage...)

	go func() {
		buf := make([]byte, 2048)
		for {
			select {
			case <-stop:
				return
			default:
			}
			session.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
			n, from, err := session.ReadFromUDP(buf)
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return
				}
				continue
			}
			frame, ok := ParseFrame(buf[:n])
			if !ok || frame.Type != MsgRelayPkt {
				continue
			}

			// The camera has joined: signal readiness, ask for a link (0x28) and
			// push one media command.
			session.WriteToUDP(EncodeFrame(MsgRelayRdy, uidBytes[:]), from)
			session.WriteToUDP(EncodeFrame(MsgSetup, setupRequest), from)
			session.WriteToUDP(EncodeFrame(MsgDRW, drwPayload), from)
			return
		}
	}()

	// ---- the real client under test ----
	sock, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("client socket: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sess, err := Dial(ctx, sock, uid, []netip.Addr{netip.MustParseAddr("127.0.0.1")},
		8*time.Second, discardLogger())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if got := sess.Relay().Port; got != portOf(session) {
		t.Fatalf("relay port = %d, want the allocated session port %d", got, portOf(session))
	}
	if sess.Token() != token {
		t.Fatalf("token = %x, want %x", sess.Token(), token)
	}

	local := AddrFromUDP(sock.LocalAddr().(*net.UDPAddr))
	conn := NewConn(sock, sess.Relay(), local, discardLogger())
	conn.Start()
	defer conn.Close()

	// The 0x28 reply must be the deterministic rewrite of the request.
	reply := readUntil(session, MsgSetup, 3*time.Second)
	if reply == nil {
		t.Fatal("no 0x28 reply: the stream would stall with no error anywhere")
	}
	want, err := SetupReply(setupRequest, local)
	if err != nil {
		t.Fatalf("SetupReply: %v", err)
	}
	if hex.EncodeToString(reply.Payload) != hex.EncodeToString(want) {
		t.Fatalf("0x28 reply = %s, want %s",
			hex.EncodeToString(reply.Payload), hex.EncodeToString(want))
	}

	// The DRW message must arrive reassembled.
	nextCtx, nextCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer nextCancel()
	message, ok := conn.NextCommand(nextCtx)
	if !ok {
		t.Fatal("no command was delivered")
	}
	if len(message) != len(authMessage)-4 {
		t.Fatalf("command = %d bytes, want %d", len(message), len(authMessage)-4)
	}
	if cmd := binary.BigEndian.Uint32(message[0:4]); cmd != 0x101 {
		t.Fatalf("command id = 0x%x, want 0x101", cmd)
	}

	// And the fragment must have been acknowledged.
	if ack := readUntil(session, MsgDRWAck, 3*time.Second); ack == nil {
		t.Fatal("the fragment was never acknowledged; the camera would retransmit forever")
	}
}

// buildAuthMessage frames the captured authentication response the same way the
// camera does: <u32 length><u32 command><body>.
func buildAuthMessage(t *testing.T) []byte {
	t.Helper()
	v := testvec.MustLoad(t)
	ar := v.DRW.AuthResponse
	body := []byte(ar.BodyJSON)

	message := make([]byte, 8+len(body))
	binary.BigEndian.PutUint32(message[0:4], uint32(4+len(body)))
	binary.BigEndian.PutUint32(message[4:8], ar.Cmd)
	copy(message[8:], body)
	return message
}

func TestRelayDialRejectsMissingServers(t *testing.T) {
	sock, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("socket: %v", err)
	}
	defer sock.Close()

	uid, err := ParseUID("ABCDEF-123456-GHIJK")
	if err != nil {
		t.Fatalf("ParseUID: %v", err)
	}
	if _, err := Dial(context.Background(), sock, uid, nil, time.Second, discardLogger()); err == nil {
		t.Fatal("Dial without rendezvous servers must fail")
	}
}
