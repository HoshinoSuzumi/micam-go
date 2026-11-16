package pppp

import (
	"encoding/binary"
	"encoding/hex"
	"testing"

	"micam/internal/testvec"
)

func TestDRWAckFrames(t *testing.T) {
	v := testvec.MustLoad(t)
	for _, c := range v.DRW.Acks {
		t.Run(c.ID, func(t *testing.T) {
			got := hex.EncodeToString(AckFrame(int(c.Channel), c.Seq))
			if got != c.Hex {
				t.Fatalf("ack = %s, want %s", got, c.Hex)
			}
		})
	}
}

// authMessage rebuilds the framed channel-0 message the fixture describes:
// <u32 BE length><u32 BE command><body>.
func authMessage(t *testing.T, ar testvec.AuthCase) []byte {
	t.Helper()

	body := []byte(ar.BodyJSON)
	if len(body) != ar.JSONLen {
		t.Fatalf("fixture body is %d bytes, expected %d", len(body), ar.JSONLen)
	}

	msg := make([]byte, 8+len(body))
	binary.BigEndian.PutUint32(msg[0:4], uint32(4+len(body)))
	binary.BigEndian.PutUint32(msg[4:8], ar.Cmd)
	copy(msg[8:], body)
	return msg
}

func TestDRWAuthResponse(t *testing.T) {
	v := testvec.MustLoad(t)
	ar := v.DRW.AuthResponse
	msg := authMessage(t, ar)

	if got := int(binary.BigEndian.Uint32(msg[0:4])); got != ar.DeclaredLen {
		t.Fatalf("declared length = %d, want %d", got, ar.DeclaredLen)
	}

	// A polled message is command word plus body: the length prefix is consumed.
	expectedPolled := ar.PolledLen
	if expectedPolled != 4+len(ar.BodyJSON) {
		t.Fatalf("fixture inconsistency: polled_len=%d but cmd+body=%d",
			expectedPolled, 4+len(ar.BodyJSON))
	}

	t.Run("single fragment", func(t *testing.T) {
		ch := NewChannel(ChannelCommand)
		if !ch.Push(0, msg) {
			t.Fatal("a contiguous first fragment must advance the stream")
		}
		got := ch.Poll()
		if len(got) != expectedPolled {
			t.Fatalf("polled %d bytes, want %d", len(got), expectedPolled)
		}
		if cmd := binary.BigEndian.Uint32(got[0:4]); cmd != ar.Cmd {
			t.Fatalf("command = 0x%x, want 0x%x", cmd, ar.Cmd)
		}
		if body := string(got[4:]); body != ar.BodyJSON {
			t.Fatalf("body = %q, want %q", body, ar.BodyJSON)
		}
		if ch.Poll() != nil {
			t.Fatal("one fragment must yield exactly one message")
		}
	})

	t.Run("split across fragments", func(t *testing.T) {
		ch := NewChannel(ChannelCommand)
		ch.Push(0, msg[:10])
		if ch.Poll() != nil {
			t.Fatal("an incomplete message must not surface")
		}
		ch.Push(1, msg[10:])
		if got := ch.Poll(); len(got) != expectedPolled {
			t.Fatalf("reassembled %d bytes, want %d", len(got), expectedPolled)
		}
	})

	t.Run("out of order", func(t *testing.T) {
		ch := NewChannel(ChannelCommand)
		if ch.Push(1, msg[20:]) {
			t.Fatal("a fragment that arrives early cannot advance the stream")
		}
		if ch.PendingFragments() != 1 {
			t.Fatalf("pending = %d, want 1", ch.PendingFragments())
		}
		if ch.Poll() != nil {
			t.Fatal("nothing is complete yet")
		}
		if !ch.Push(0, msg[:20]) {
			t.Fatal("the gap filler must advance the stream")
		}
		if ch.PendingFragments() != 0 {
			t.Fatalf("pending = %d after the gap was filled, want 0", ch.PendingFragments())
		}
		if got := ch.Poll(); len(got) != expectedPolled {
			t.Fatalf("reassembled %d bytes, want %d", len(got), expectedPolled)
		}
	})

	t.Run("duplicate", func(t *testing.T) {
		ch := NewChannel(ChannelCommand)
		if !ch.Push(0, msg) {
			t.Fatal("first fragment must be accepted")
		}
		if ch.Push(0, msg) {
			t.Fatal("a duplicate must be dropped")
		}
		if got := ch.Poll(); len(got) != expectedPolled {
			t.Fatalf("duplicate corrupted the stream: %d bytes", len(got))
		}
		if ch.Poll() != nil {
			t.Fatal("a duplicate must not produce a second message")
		}
	})

	t.Run("two messages in one fragment", func(t *testing.T) {
		ch := NewChannel(ChannelCommand)
		both := append(append([]byte(nil), msg...), msg...)
		ch.Push(0, both)
		if got := ch.Poll(); len(got) != expectedPolled {
			t.Fatalf("first message = %d bytes", len(got))
		}
		if got := ch.Poll(); len(got) != expectedPolled {
			t.Fatalf("second message = %d bytes", len(got))
		}
		if ch.Poll() != nil {
			t.Fatal("exactly two messages were present")
		}
	})

	t.Run("sequence wraparound", func(t *testing.T) {
		ch := NewChannel(ChannelCommand)
		for i := 0; i <= 0xFFFF; i++ {
			ch.Push(uint16(i), nil)
		}
		if !ch.Push(0, msg) {
			t.Fatal("the sequence must wrap back to zero")
		}
		if got := ch.Poll(); len(got) != expectedPolled {
			t.Fatalf("message after wraparound = %d bytes", len(got))
		}
	})
}

func TestParseDRW(t *testing.T) {
	v := testvec.MustLoad(t)
	ar := v.DRW.AuthResponse
	msg := authMessage(t, ar)

	payload := append([]byte{DRWMagic, ChannelCommand, 0x00, 0x07}, msg...)
	channel, seq, data, ok := ParseDRW(payload)
	if !ok {
		t.Fatal("ParseDRW rejected a valid payload")
	}
	if channel != ChannelCommand {
		t.Fatalf("channel = %d, want %d", channel, ChannelCommand)
	}
	if seq != 7 {
		t.Fatalf("seq = %d, want 7", seq)
	}
	if len(data) != len(msg) {
		t.Fatalf("data = %d bytes, want %d", len(data), len(msg))
	}

	if _, _, _, ok := ParseDRW([]byte{0x00, 0x00, 0x00, 0x00}); ok {
		t.Fatal("ParseDRW must reject a payload without the D1 magic")
	}
}
