// Package stream republishes the camera stream as MPEG-TS over HTTP.
package stream

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"micam/internal/miss"
	"micam/internal/ts"
)

const (
	// queueDepth is roughly a second of video; beyond this a viewer is too far
	// behind to catch up and frames are dropped.
	queueDepth = 60
	// viewerTimeout drops a viewer when the camera has gone quiet.
	viewerTimeout = 10 * time.Second
)

// Config is where to listen and what path to serve.
type Config struct {
	Addr string // host:port, e.g. 127.0.0.1:8554
	Path string // e.g. /cam.ts
}

// Server fans one stream out to any number of HTTP viewers.
//
// Each viewer gets its own muxer and its own bounded queue, so a slow player has
// frames dropped rather than being allowed to stall the camera reader.
type Server struct {
	cfg      Config
	log      *slog.Logger
	listener net.Listener
	http     *http.Server

	mu      sync.RWMutex
	viewers map[*viewer]struct{}
	closed  bool
}

// New builds a server. It does not listen until Start.
func New(cfg Config, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	if cfg.Path == "" {
		cfg.Path = "/cam.ts"
	}
	return &Server{cfg: cfg, log: log, viewers: make(map[*viewer]struct{})}
}

// Start binds the listener and begins serving.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return err
	}
	s.listener = ln

	mux := http.NewServeMux()
	mux.HandleFunc(s.cfg.Path, s.handle)

	s.http = &http.Server{
		Handler: mux,
		// A stream never ends on its own, so the write timeout must stay disabled.
		WriteTimeout: 0,
	}
	go func() {
		if err := s.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("http server stopped", "err", err)
		}
	}()

	s.log.Info("streaming", "url", s.URL())
	return nil
}

// URL is the address a player should open.
func (s *Server) URL() string {
	host, port, err := net.SplitHostPort(s.cfg.Addr)
	if err != nil {
		return "http://" + s.cfg.Addr + s.cfg.Path
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + s.cfg.Path
}

// Viewers reports how many players are connected.
func (s *Server) Viewers() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.viewers)
}

// Publish offers a frame to every viewer. It never blocks.
func (s *Server) Publish(p miss.Packet) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return
	}
	for v := range s.viewers {
		v.offer(p)
	}
}

// Close stops serving and disconnects everyone.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	for v := range s.viewers {
		delete(s.viewers, v)
		close(v.frames)
	}
	s.mu.Unlock()

	if s.http != nil {
		return s.http.Close()
	}
	return nil
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not supported by this connection", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	v := &viewer{frames: make(chan miss.Packet, queueDepth)}
	s.add(v)
	s.log.Info("viewer connected", "from", r.RemoteAddr, "total", s.Viewers())
	defer func() {
		s.remove(v)
		s.log.Info("viewer disconnected", "total", s.Viewers())
	}()

	v.pump(w, flusher)
}

func (s *Server) add(v *viewer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		close(v.frames)
		return
	}
	s.viewers[v] = struct{}{}
}

func (s *Server) remove(v *viewer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.viewers[v]; ok {
		delete(s.viewers, v)
		close(v.frames)
	}
}

// viewer is one connected player.
type viewer struct {
	frames  chan miss.Packet
	started bool
}

// offer queues a frame, starting only at a keyframe: a viewer that joins
// mid-GOP would otherwise decode P-frames referencing parameter sets it never
// received.
func (v *viewer) offer(p miss.Packet) {
	if !v.started {
		if !p.Keyframe() {
			return
		}
		v.started = true
	}
	select {
	case v.frames <- p:
	default:
		// Too far behind: drop this frame.
	}
}

func (v *viewer) pump(w io.Writer, flusher http.Flusher) {
	var muxer *ts.Muxer

	timeout := time.NewTimer(viewerTimeout)
	defer timeout.Stop()

	for {
		select {
		case p, ok := <-v.frames:
			if !ok {
				return
			}
			if muxer == nil {
				muxer = ts.NewMuxer(w, p.Codec)
			}
			if err := muxer.Write(p); err != nil {
				return // normal: the player disconnected
			}
			flusher.Flush()

			if !timeout.Stop() {
				select {
				case <-timeout.C:
				default:
				}
			}
			timeout.Reset(viewerTimeout)
		case <-timeout.C:
			return // the camera went quiet; drop the viewer
		}
	}
}

// String describes the server for logs.
func (s *Server) String() string {
	return "stream.Server(" + s.cfg.Addr + s.cfg.Path + ", viewers=" + strconv.Itoa(s.Viewers()) + ")"
}
