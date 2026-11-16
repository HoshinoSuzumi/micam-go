// Package testvec loads the language-agnostic golden vectors in
// testdata/vectors.json. See docs/PROTOCOL.md for what each group asserts.
//
// The loader walks up from the working directory, so any test package at any
// depth can call MustLoad without knowing its own location in the tree. The
// file is parsed once and shared.
package testvec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Vectors mirrors testdata/vectors.json. Groups that no test consumes yet are
// simply absent here; encoding/json ignores the rest.
type Vectors struct {
	Meta    Meta          `json:"meta"`
	UID     []UIDCase     `json:"uid"`
	Addr    []AddrCase    `json:"addrblock"`
	Frame   FrameGroup    `json:"frame"`
	PortAck []PortAckCase `json:"relay_port_ack"`
	Init    []InitCase    `json:"initstring"`
	Setup   SetupCase     `json:"miss_setup"`
	DRW     DRWGroup      `json:"drw"`
}

type Meta struct {
	Schema         int    `json:"schema"`
	Spec           string `json:"spec"`
	Upstream       string `json:"upstream"`
	UpstreamCommit string `json:"upstream_commit"`
}

type UIDCase struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Prefix string `json:"prefix"`
	Serial uint32 `json:"serial"`
	Check  string `json:"check"`
	Hex    string `json:"hex"`
	Len    int    `json:"len"`
}

type AddrCase struct {
	ID   string `json:"id"`
	IP   string `json:"ip"`
	Port uint16 `json:"port"`
	Hex  string `json:"hex"`
}

type FrameGroup struct {
	Encode []FrameCase  `json:"encode"`
	Decode []FrameCase  `json:"decode"`
	Reject []RejectCase `json:"reject"`
}

type FrameCase struct {
	ID         string `json:"id"`
	Type       uint8  `json:"type"`
	TypeName   string `json:"type_name"`
	PayloadHex string `json:"payload_hex"`
	Hex        string `json:"hex"`
}

type RejectCase struct {
	ID     string `json:"id"`
	Hex    string `json:"hex"`
	Reason string `json:"reason"`
}

type PortAckCase struct {
	ID         string `json:"id"`
	PayloadHex string `json:"payload_hex"`
	TokenHex   string `json:"token_hex"`
	Port       uint16 `json:"port"`
}

type InitCase struct {
	ID      string   `json:"id"`
	Enc     string   `json:"enc"`
	Suffix  string   `json:"suffix"`
	Raw     string   `json:"raw"`
	Servers []string `json:"servers"`
}

type SetupCase struct {
	RequestHex string     `json:"request_hex"`
	ReplyHex   string     `json:"reply_hex"`
	Local      Endpoint   `json:"local"`
	External   Endpoint   `json:"external"`
	Len        int        `json:"len"`
	Compact    []AddrCase `json:"compact"`
}

type Endpoint struct {
	IP   string `json:"ip"`
	Port uint16 `json:"port"`
}

type DRWGroup struct {
	Acks         []AckCase `json:"acks"`
	AuthResponse AuthCase  `json:"auth_response"`
}

type AckCase struct {
	ID      string `json:"id"`
	Channel uint8  `json:"channel"`
	Seq     uint16 `json:"seq"`
	Hex     string `json:"hex"`
}

type AuthCase struct {
	ID          string `json:"id"`
	Cmd         uint32 `json:"cmd"`
	BodyJSON    string `json:"json"`
	JSONLen     int    `json:"json_len"`
	DeclaredLen int    `json:"declared_len"`
	PolledLen   int    `json:"polled_len"`
}

var (
	once    sync.Once
	cached  *Vectors
	loadErr error
)

// MustLoad returns the parsed vectors or fails the test.
func MustLoad(tb testing.TB) *Vectors {
	tb.Helper()
	v, err := Load()
	if err != nil {
		tb.Fatalf("testvec: %v", err)
	}
	return v
}

// Load parses testdata/vectors.json once per process.
func Load() (*Vectors, error) {
	once.Do(func() {
		path, err := find("testdata/vectors.json")
		if err != nil {
			loadErr = err
			return
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			loadErr = fmt.Errorf("read %s: %w", path, err)
			return
		}
		var v Vectors
		if err := json.Unmarshal(raw, &v); err != nil {
			loadErr = fmt.Errorf("parse %s: %w", path, err)
			return
		}
		if len(v.UID) == 0 || len(v.Addr) == 0 {
			loadErr = fmt.Errorf("parse %s: no vectors found", path)
			return
		}
		cached = &v
	})
	return cached, loadErr
}

func find(rel string) (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	start := dir
	for i := 0; i < 10; i++ {
		candidate := filepath.Join(dir, filepath.FromSlash(rel))
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("cannot find %s from %s", rel, start)
}
