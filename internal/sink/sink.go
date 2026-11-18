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

// AudioWriter is implemented by sinks that can also carry the camera's sound.
//
// Not every sink can: the MPEG-TS muxer declares a second elementary stream for
// it, while a format without room for Opus simply drops the frames.
type AudioWriter interface {
	WriteAudio(p miss.Packet) error
}

// Factory builds a sink for one connection.
//
// The codec is only known once the first frame arrives, which is why this is a
// factory rather than a sink: at accept time there is nothing to encode yet.
type Factory func(w io.Writer, codec uint32) Sink

// TS is the MPEG-TS factory, and the default when no other is configured.
func TS(leadMS int, withAudio bool) Factory {
	return func(w io.Writer, codec uint32) Sink {
		return &tsSink{m: ts.NewMuxerWithAudio(w, codec, leadMS, withAudio)}
	}
}

type tsSink struct {
	m *ts.Muxer
}

func (s *tsSink) Write(p miss.Packet) error { return s.m.Write(p) }

// WriteAudio is what makes the transport stream carry sound.
func (s *tsSink) WriteAudio(p miss.Packet) error { return s.m.WriteAudio(p) }

func (s *tsSink) Close() error { return s.m.Flush() }
