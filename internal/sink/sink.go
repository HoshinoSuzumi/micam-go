// Package sink is the seam between the frame fan-out and a wire format.
//
// Before this existed, stream.pump owned a ts.Muxer directly, so there was no
// place to put a second output format: the encoding was welded to the
// connection.
package sink

import (
	"io"

	"micam/internal/miss"
	"micam/internal/ts"
)

// Sink turns frames into one wire format and writes them out.
//
// Write is called from a single goroutine per sink, so implementations keep
// their own state without locking.
type Sink interface {
	Write(p miss.Packet) error
	Close() error
}

// Factory builds a sink for one connection.
//
// The codec is only known once the first frame arrives, which is why this is a
// factory rather than a sink: at accept time there is nothing to encode yet.
type Factory func(w io.Writer, codec uint32) Sink

// TS is the MPEG-TS factory, and the default when no other is configured.
func TS(leadMS int) Factory {
	return func(w io.Writer, codec uint32) Sink {
		return &tsSink{m: ts.NewMuxerWithLead(w, codec, leadMS)}
	}
}

type tsSink struct {
	m *ts.Muxer
}

func (s *tsSink) Write(p miss.Packet) error { return s.m.Write(p) }

func (s *tsSink) Close() error { return s.m.Flush() }
