package pppp

import (
	"context"
	"encoding/binary"
	"log/slog"
	"net"
	"sync"
	"time"
)

const (
	commandQueueDepth = 32
	mediaQueueDepth   = 256
	readTimeout       = 500 * time.Millisecond
)

// Conn carries application data over an established session.
//
// It owns the receive loop and everything the peer expects us to keep doing:
// acknowledging DRW fragments on UDP, answering keepalives, and answering the
// relay's repeated 0x28 linking request. Neglecting any of these raises no error
// - the stream simply stops.
type Conn struct {
	link  Link
	local Addr
	log   *slog.Logger

	cmdChannel   *Channel
	mediaChannel *Channel

	commands chan []byte
	media    chan []byte

	writeMu    sync.Mutex
	commandSeq uint16
	dropped    uint64

	closed    chan struct{}
	closeOnce sync.Once
	startOnce sync.Once
}

// NewConn wraps an established session. local is what the 0x28 reply reports as
// our address; it is only used on the relay path.
func NewConn(link Link, local Addr, log *slog.Logger) *Conn {
	if log == nil {
		log = slog.Default()
	}
	return &Conn{
		link:         link,
		local:        local,
		log:          log,
		cmdChannel:   NewChannel(ChannelCommand),
		mediaChannel: NewChannel(ChannelMedia),
		commands:     make(chan []byte, commandQueueDepth),
		media:        make(chan []byte, mediaQueueDepth),
		closed:       make(chan struct{}),
	}
}

// Start begins the receive loop. Safe to call once.
func (c *Conn) Start() {
	c.startOnce.Do(func() {
		go c.readLoop()
		if c.link.Reliable() {
			// A TCP session idles out without traffic; the vendor app sends a
			// ping roughly once a second.
			go c.keepaliveLoop()
		}
	})
}

func (c *Conn) keepaliveLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	ping := EncodeFrame(MsgAlive, nil)
	for {
		select {
		case <-c.closed:
			return
		case <-ticker.C:
			if err := c.link.WriteFrame(ping); err != nil {
				c.log.Debug("keepalive failed", "err", err)
				return
			}
		}
	}
}

// Done is closed when the session ends.
func (c *Conn) Done() <-chan struct{} { return c.closed }

// SendCommand sends one channel-0 command:
//
//	<u32 BE total length><u32 BE command><body>
func (c *Conn) SendCommand(cmd uint32, body []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	message := make([]byte, 8+len(body))
	binary.BigEndian.PutUint32(message[0:4], uint32(4+len(body)))
	binary.BigEndian.PutUint32(message[4:8], cmd)
	copy(message[8:], body)

	payload := make([]byte, 4+len(message))
	payload[0] = DRWMagic
	payload[1] = ChannelCommand
	binary.BigEndian.PutUint16(payload[2:4], c.commandSeq)
	copy(payload[4:], message)
	c.commandSeq++

	return c.sendRaw(EncodeFrame(MsgDRW, payload))
}

// NextCommand returns the next channel-0 message.
func (c *Conn) NextCommand(ctx context.Context) ([]byte, bool) {
	return c.take(ctx, c.commands)
}

// TryNextCommand returns a queued command without blocking.
//
// The camera keeps talking on channel 0 while it streams - roughly one message a
// second - and nothing in the media loop needs those. Draining them keeps the
// bounded queue from overflowing, and lets the caller see what they are.
func (c *Conn) TryNextCommand() ([]byte, bool) {
	select {
	case message := <-c.commands:
		return message, true
	default:
		return nil, false
	}
}

// NextMedia returns the next channel-2 message.
func (c *Conn) NextMedia(ctx context.Context) ([]byte, bool) {
	return c.take(ctx, c.media)
}

func (c *Conn) take(ctx context.Context, queue chan []byte) ([]byte, bool) {
	select {
	case message := <-queue:
		return message, true
	case <-c.closed:
		// Drain whatever already arrived so a final response is not lost.
		select {
		case message := <-queue:
			return message, true
		default:
			return nil, false
		}
	case <-ctx.Done():
		return nil, false
	}
}

// Close tells the peer we are leaving, stops the reader and releases the link.
func (c *Conn) Close() error {
	c.markClosed()
	_ = c.sendRaw(EncodeFrame(MsgClose, nil))
	return c.link.Close()
}

func (c *Conn) markClosed() {
	c.closeOnce.Do(func() { close(c.closed) })
}

func (c *Conn) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

// -------------------------------------------------------------- receiving ---

func (c *Conn) readLoop() {
	buf := make([]byte, 4096)
	for !c.isClosed() {
		if err := c.link.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
			return
		}
		n, err := c.link.ReadFrame(buf)
		if err != nil {
			if c.isClosed() {
				return
			}
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			c.log.Debug("receive stopped", "err", err)
			c.markClosed()
			return
		}
		if n == 0 {
			continue
		}

		frame, ok := ParseFrame(buf[:n])
		if !ok {
			continue
		}
		c.handle(frame)
	}
}

func (c *Conn) handle(frame Frame) {
	switch frame.Type {
	case MsgDRW:
		c.onData(frame.Payload)
	case MsgAlive:
		if err := c.sendRaw(EncodeFrame(MsgAliveAck, nil)); err != nil {
			c.log.Debug("cannot answer a keepalive", "err", err)
		}
	case MsgSetup:
		c.onSetup(frame.Payload)
	case MsgClose:
		c.log.Debug("peer closed the session", "peer", c.link.Describe())
		c.markClosed()
	}
}

func (c *Conn) onData(payload []byte) {
	channel, seq, data, ok := ParseDRW(payload)
	if !ok {
		return
	}

	// UDP loses fragments, so every one is acknowledged - duplicates included: a
	// repeat means the peer never saw the previous ack. TCP guarantees delivery
	// and needs none of this.
	if !c.link.Reliable() {
		if err := c.sendRaw(AckFrame(channel, seq)); err != nil {
			c.log.Debug("cannot ack a fragment", "err", err)
		}
	}

	var target *Channel
	var queue chan []byte
	switch channel {
	case ChannelCommand:
		target, queue = c.cmdChannel, c.commands
	case ChannelMedia:
		target, queue = c.mediaChannel, c.media
	default:
		return
	}

	if c.link.Reliable() {
		target.PushReliable(data)
	} else {
		target.Push(seq, data)
	}
	for {
		message := target.Poll()
		if message == nil {
			break
		}
		select {
		case queue <- message:
		default:
			// The reader must never block on a slow consumer. Log sparsely: a
			// full queue repeats every second, and a wall of identical debug
			// lines hides everything else.
			c.dropped++
			if c.dropped == 1 || c.dropped%500 == 0 {
				c.log.Debug("queue full, dropping a message", "channel", channel, "total", c.dropped)
			}
		}
	}
}

func (c *Conn) onSetup(payload []byte) {
	if !IsSetupRequest(payload) {
		return
	}
	reply, err := SetupReply(payload, c.local)
	if err != nil {
		c.log.Debug("cannot answer the link request", "err", err)
		return
	}
	if err := c.sendRaw(EncodeFrame(MsgSetup, reply)); err != nil {
		c.log.Debug("cannot send the link reply", "err", err)
		return
	}
	c.log.Debug("linked", "external", SetupExternalAddress(payload).String())
}

func (c *Conn) sendRaw(datagram []byte) error {
	return c.link.WriteFrame(datagram)
}
