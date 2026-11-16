package pppp

import (
	"encoding/hex"
	"testing"

	"micam/internal/testvec"
)

func TestSetupReply(t *testing.T) {
	v := testvec.MustLoad(t)
	s := v.Setup

	req := mustHex(t, s.RequestHex)
	if len(req) != s.Len {
		t.Fatalf("request length = %d, want %d", len(req), s.Len)
	}
	if !IsSetupRequest(req) {
		t.Fatal("the fixture is not recognised as a 0x28 request")
	}

	local, err := NewAddr(s.Local.IP, s.Local.Port)
	if err != nil {
		t.Fatalf("local address: %v", err)
	}
	reply, err := SetupReply(req, local)
	if err != nil {
		t.Fatalf("SetupReply: %v", err)
	}
	if len(reply) != s.Len {
		t.Fatalf("reply length = %d, want %d (the rewrite must not resize)", len(reply), s.Len)
	}
	if got := hex.EncodeToString(reply); got != s.ReplyHex {
		t.Fatalf("reply = %s\n want %s", got, s.ReplyHex)
	}
	if IsSetupRequest(reply) {
		t.Fatal("our own reply must not look like a request, or we answer forever")
	}
}

func TestSetupExternalAddress(t *testing.T) {
	v := testvec.MustLoad(t)
	s := v.Setup

	req := mustHex(t, s.RequestHex)
	ext := SetupExternalAddress(req)
	if got := ext.IP.String(); got != s.External.IP {
		t.Fatalf("external ip = %s, want %s", got, s.External.IP)
	}
	if ext.Port != s.External.Port {
		t.Fatalf("external port = %d, want %d", ext.Port, s.External.Port)
	}
}

func TestAddrCompact(t *testing.T) {
	v := testvec.MustLoad(t)
	for _, c := range v.Setup.Compact {
		t.Run(c.ID, func(t *testing.T) {
			a, err := NewAddr(c.IP, c.Port)
			if err != nil {
				t.Fatalf("NewAddr: %v", err)
			}
			compact := a.Compact()
			if got := hex.EncodeToString(compact[:]); got != c.Hex {
				t.Fatalf("compact = %s, want %s", got, c.Hex)
			}
		})
	}
}

func TestSetupReplyRejectsShortInput(t *testing.T) {
	if _, err := SetupReply(make([]byte, SetupLen-1), MustAddr("10.0.0.2", 1)); err == nil {
		t.Fatal("SetupReply should reject a short request")
	}
}
