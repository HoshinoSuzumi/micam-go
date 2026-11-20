package stream

import (
	"io"
	"net"
	"net/http"
	"sort"
	"sync/atomic"
	"time"

	"micam/internal/status"
)

// hlsSessionTimeout is how long an HLS player is considered present after its
// last request.
//
// HLS has no connection to hang a session on: the player fetches a playlist, then
// a segment every few seconds. Anything longer than a couple of segment intervals
// means it stopped.
const hlsSessionTimeout = 20 * time.Second

// hlsSession is one HLS player, inferred from its requests.
type hlsSession struct {
	remote   string
	since    time.Time
	last     time.Time
	bytes    atomic.Int64
	requests atomic.Int64
}

// noteHLS records one request from an HLS player.
func (s *Server) noteHLS(r *http.Request, size int) {
	now := time.Now()
	ip := clientIP(r)

	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.hls[ip]
	if !ok {
		sess = &hlsSession{remote: ip, since: now}
		s.hls[ip] = sess
		s.noteViewersLocked()
	}
	sess.last = now
	sess.bytes.Add(int64(size))
	sess.requests.Add(1)
}

// clientIP is who is asking, without the ephemeral port: HLS opens a new
// connection for every segment, so the port says nothing about identity.
func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// Viewers returns how many players are connected across every output.
//
// It counts HLS sessions too, which the plain viewer count used to miss: watching
// through the playlist is still watching.
func (s *Server) Viewers() int {
	return len(s.ViewersInfo())
}

// ViewersInfo lists who is watching, through which output, and how much they have
// received.
func (s *Server) ViewersInfo() []status.Connection {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	out := make([]status.Connection, 0, len(s.viewers)+len(s.audioViewers)+len(s.hls))

	for v := range s.viewers {
		out = append(out, status.Connection{
			Kind:   "ts",
			Remote: v.remote,
			Since:  v.since,
			Bytes:  v.bytes.Load(),
		})
	}

	for v := range s.audioViewers {
		out = append(out, status.Connection{
			Kind:   "wav",
			Remote: v.remote,
			Since:  v.since,
			Bytes:  v.bytes.Load(),
		})
	}

	pruned := false
	for ip, sess := range s.hls {
		if now.Sub(sess.last) > hlsSessionTimeout {
			delete(s.hls, ip)
			pruned = true
			continue
		}
		out = append(out, status.Connection{
			Kind:     "hls",
			Remote:   sess.remote,
			Since:    sess.since,
			Bytes:    sess.bytes.Load(),
			Requests: sess.requests.Load(),
			Last:     sess.last,
		})
	}

	if pruned {
		s.noteViewersLocked()
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Since.Before(out[j].Since) })
	return out
}

// countingWriter tallies what a viewer has received.
type countingWriter struct {
	w     io.Writer
	n     *atomic.Int64
	total *atomic.Int64
}

func (c countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if n > 0 {
		c.n.Add(int64(n))
		if c.total != nil {
			c.total.Add(int64(n))
		}
	}
	return n, err
}
