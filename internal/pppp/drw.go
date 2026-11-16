package pppp

import "encoding/binary"

// DRW is a reliable, ordered stream carried inside PPPP frames:
//
//	F1 D0 <len:u16> | D1 | channel:u8 | seq:u16 BE | <u32 BE length> | <message>
//
// Sequence numbers are 16-bit and wrap, so comparisons are done modulo 2^16.
// One UDP fragment may carry zero, one, or several whole messages, or only
// part of one.
const (
	DRWMagic byte = 0xD1

	ChannelCommand = 0 // commands and responses
	ChannelMedia   = 2 // inbound media
	ChannelAudio   = 3 // outbound audio, not implemented
)

// Channel reassembles one DRW channel into whole application messages.
type Channel struct {
	id       int
	expected uint16
	pending  map[uint16][]byte
	buf      []byte
	messages [][]byte
}

// NewChannel builds a reassembler for the given channel id.
func NewChannel(id int) *Channel {
	return &Channel{id: id, pending: make(map[uint16][]byte)}
}

// ID returns the channel number.
func (c *Channel) ID() int { return c.id }

// Push accepts one fragment. It reports whether the fragment advanced the
// stream. A duplicate returns false but must still be acknowledged: the peer
// only retransmits because it never saw our previous ack.
func (c *Channel) Push(seq uint16, data []byte) bool {
	if seqBefore(seq, c.expected) {
		return false // already consumed
	}
	if seq != c.expected {
		if _, held := c.pending[seq]; !held {
			c.pending[seq] = append([]byte(nil), data...)
		}
		return false // arrived early, held back
	}

	c.buf = append(c.buf, data...)
	c.expected++
	for {
		next, held := c.pending[c.expected]
		if !held {
			break
		}
		delete(c.pending, c.expected)
		c.buf = append(c.buf, next...)
		c.expected++
	}
	c.extract()
	return true
}

// PushReliable appends data in arrival order.
//
// TCP guarantees ordering and delivery, so sequence bookkeeping is not only
// unnecessary but harmful: a TCP session legitimately starts at an arbitrary
// sequence number, and waiting for zero would stall it forever.
func (c *Channel) PushReliable(data []byte) {
	c.buf = append(c.buf, data...)
	c.extract()
}

// Poll returns the next complete message, or nil when none is ready.
func (c *Channel) Poll() []byte {
	if len(c.messages) == 0 {
		return nil
	}
	msg := c.messages[0]
	c.messages[0] = nil
	c.messages = c.messages[1:]
	if len(c.messages) == 0 {
		c.messages = nil
	}
	return msg
}

// PendingFragments reports how many out-of-order fragments are held back.
func (c *Channel) PendingFragments() int { return len(c.pending) }

// extract splits the byte stream into length-prefixed messages.
func (c *Channel) extract() {
	for len(c.buf) >= 4 {
		n := binary.BigEndian.Uint32(c.buf[:4])
		if uint64(n) > uint64(len(c.buf)-4) {
			return // wait for the rest
		}
		size := int(n)
		msg := make([]byte, size)
		copy(msg, c.buf[4:4+size])
		c.buf = c.buf[4+size:]
		c.messages = append(c.messages, msg)
	}
	if len(c.buf) == 0 {
		c.buf = nil
	}
}

// AckFrame builds the DRW acknowledgement for one fragment.
func AckFrame(channel int, seq uint16) []byte {
	payload := []byte{
		DRWMagic, byte(channel),
		0x00, 0x01, // one sequence number follows
		byte(seq >> 8), byte(seq),
	}
	return EncodeFrame(MsgDRWAck, payload)
}

// ParseDRW splits a DRW payload into channel, sequence and data.
func ParseDRW(payload []byte) (channel int, seq uint16, data []byte, ok bool) {
	if len(payload) < 4 || payload[0] != DRWMagic {
		return 0, 0, nil, false
	}
	return int(payload[1]), binary.BigEndian.Uint16(payload[2:4]), payload[4:], true
}

// seqBefore reports whether a is behind b in the wrapping sequence space.
// Anything half a sequence space behind is treated as a wrap, not a
// retransmission.
func seqBefore(a, b uint16) bool {
	return uint16(b-a) < 0x8000 && a != b
}
