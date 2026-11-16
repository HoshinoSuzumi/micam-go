package pppp

import "fmt"

// PPPP message types. Names follow the published CS2 dissector; see
// docs/PROTOCOL.md §8.2 for which ones matter on this path.
const (
	MsgHello       byte = 0x00
	MsgHelloAck    byte = 0x01
	MsgQueryDID    byte = 0x08
	MsgQueryDIDAck byte = 0x09

	MsgP2PReq    byte = 0x20
	MsgP2PReqAck byte = 0x21
	MsgP2PReqDSK byte = 0x26

	// MsgSetup is Xiaomi's MISS session setup, carried inside the relay session.
	MsgSetup   byte = 0x28
	MsgLANSrch byte = 0x30

	MsgPunchTo  byte = 0x40
	MsgPunchPkt byte = 0x41
	MsgP2PRdy   byte = 0x42

	MsgListReq1   byte = 0x67
	MsgListReq    byte = 0x68
	MsgListReqAck byte = 0x69

	MsgRelayHello    byte = 0x70
	MsgRelayHelloAck byte = 0x71
	MsgRelayPort     byte = 0x72
	MsgRelayPortAck  byte = 0x73

	MsgRelayReq    byte = 0x80
	MsgRelayReqAck byte = 0x81
	MsgRelayTo     byte = 0x82
	// MsgRelayPkt doubles as relay registration and as relayed data.
	MsgRelayPkt byte = 0x83
	MsgRelayRdy byte = 0x84

	MsgDRW    byte = 0xD0
	MsgDRWAck byte = 0xD1

	MsgAlive    byte = 0xE0
	MsgAliveAck byte = 0xE1
	MsgClose    byte = 0xF0
)

var msgNames = map[byte]string{
	MsgHello:         "HELLO",
	MsgHelloAck:      "HELLO_ACK",
	MsgQueryDID:      "QUERY_DID",
	MsgQueryDIDAck:   "QUERY_DID_ACK",
	MsgP2PReq:        "P2P_REQ",
	MsgP2PReqAck:     "P2P_REQ_ACK",
	MsgP2PReqDSK:     "P2P_REQ_DSK",
	MsgSetup:         "MISS_SETUP",
	MsgLANSrch:       "LAN_SEARCH",
	MsgPunchTo:       "PUNCH_TO",
	MsgPunchPkt:      "PUNCH_PKT",
	MsgP2PRdy:        "P2P_RDY",
	MsgListReq1:      "LIST_REQ1",
	MsgListReq:       "LIST_REQ",
	MsgListReqAck:    "LIST_REQ_ACK",
	MsgRelayHello:    "RLY_HELLO",
	MsgRelayHelloAck: "RLY_HELLO_ACK",
	MsgRelayPort:     "RLY_PORT",
	MsgRelayPortAck:  "RLY_PORT_ACK",
	MsgRelayReq:      "RLY_REQ",
	MsgRelayReqAck:   "RLY_REQ_ACK",
	MsgRelayTo:       "RLY_TO",
	MsgRelayPkt:      "RLY_PKT",
	MsgRelayRdy:      "RLY_RDY",
	MsgDRW:           "DRW",
	MsgDRWAck:        "DRW_ACK",
	MsgAlive:         "ALIVE",
	MsgAliveAck:      "ALIVE_ACK",
	MsgClose:         "CLOSE",
}

// MsgName renders a message type for logs.
func MsgName(t byte) string {
	if name, ok := msgNames[t]; ok {
		return name
	}
	return fmt.Sprintf("0x%02x", t)
}
