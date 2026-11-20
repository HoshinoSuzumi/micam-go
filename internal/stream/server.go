// Package stream republishes the camera stream as MPEG-TS over HTTP.
package stream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"micam/internal/hls"
	"micam/internal/miss"
	"micam/internal/sink"
)

const (
	// queueDepth is roughly a second of video, and it is also the latency ceiling
	// for a viewer that cannot keep up: beyond it the oldest frames are dropped.
	queueDepth = 25
	// viewerTimeout drops a viewer when the camera has gone quiet.
	viewerTimeout = 10 * time.Second
	// firstFrameTimeout is how long a viewer that has received nothing at all
	// waits. With on-demand streaming a player can connect while the camera
	// session is still being established, and that takes longer than 10 seconds.
	firstFrameTimeout = 45 * time.Second
)

// Config is where to listen and what path to serve.
type Config struct {
	Addr string // host:port, e.g. 127.0.0.1:8554
	Path string // e.g. /cam.ts
	// PTSLeadMS is how far presentation runs ahead of the transport clock. It is
	// the largest tunable source of latency in the pipeline.
	PTSLeadMS int
	// Sink builds the format written to each viewer. Nil means MPEG-TS.
	Sink sink.Factory
	// HLS additionally serves /cam.m3u8 next to /cam.ts. Both outputs are fed
	// from the same frames.
	HLS bool
	// HLSWindow is how many segments the playlist keeps. Zero means the default.
	HLSWindow int
	// Audio serves the camera's sound as a WAV stream on its own endpoint.
	//
	// It is separate because MPEG-TS has no way to carry G.711 and the HLS
	// container cannot either: putting it in the video stream would mean writing
	// a track no player can decode.
	Audio bool
	// AudioPath defaults to /cam.wav.
	AudioPath string
	// AudioRate is the sample rate of the camera's G.711 audio.
	AudioRate int
}

// Server fans one stream out to any number of HTTP viewers.
//
// Each viewer gets its own sink and its own bounded queue, so a slow player has
// frames dropped rather than being allowed to stall the camera reader.
type Server struct {
	cfg      Config
	log      *slog.Logger
	listener net.Listener
	http     *http.Server

	// seg is the single HLS segmenter, shared by every HLS viewer.
	seg *hls.Segmenter

	mu           sync.RWMutex
	viewers      map[*viewer]struct{}
	audioViewers map[*audioViewer]struct{}
	hls          map[string]*hlsSession
	closed       bool

	// lastFrame is the UnixNano of the last video frame handed to the fan-out.
	lastFrame atomic.Int64

	// viewerSignal wakes WaitForViewer whenever the audience changes.
	viewerSignal chan struct{}
	// zeroSince is when the last viewer left, zero while someone is watching.
	// On-demand streaming uses it to decide that the camera can be released.
	zeroSince time.Time
}

// New builds a server. It does not listen until Start.
func New(cfg Config, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	if cfg.Path == "" {
		cfg.Path = "/cam.ts"
	}
	if cfg.AudioPath == "" {
		cfg.AudioPath = "/cam.wav"
	}
	if cfg.AudioRate <= 0 {
		// G.711 is an 8 kHz codec; anything else would be an unusual camera.
		cfg.AudioRate = 8000
	}
	return &Server{
		cfg:          cfg,
		log:          log,
		viewers:      make(map[*viewer]struct{}),
		audioViewers: make(map[*audioViewer]struct{}),
		hls:          make(map[string]*hlsSession),
		viewerSignal: make(chan struct{}, 1),
		zeroSince:    time.Now(),
	}
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
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/", s.handleIndex)

	if s.cfg.HLS {
		s.seg = hls.New(s.cfg.HLSWindow, s.cfg.Audio)
		mux.HandleFunc(s.HLSPlaylistPath(), s.handlePlaylist)
		// A trailing slash is required: patterns without one match exactly in
		// Go 1.22's mux, so "/cam/seg" would never see "/cam/seg0.m4s".
		mux.HandleFunc(s.HLSBase()+"/", s.handleHLSFile)
	}

	if s.cfg.Audio {
		mux.HandleFunc(s.cfg.AudioPath, s.handleAudio)
	}

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

	// The banner used to say 127.0.0.1 even when bound to 0.0.0.0, which reads like
	// "local only" when it is not. Log what the kernel actually gave us.
	s.log.Info("streaming", "listen", s.Addr(), "url", s.URL())
	return nil
}

// Addr is the address the server actually bound: the configured port, and the
// wildcard expanded to the interface it really listens on.
func (s *Server) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.cfg.Addr
}

// ReachableURL is one address another machine can open.
type ReachableURL struct {
	URL   string
	Iface string
}

// ReachableURLs lists what someone else could actually type: one entry per
// non-loopback address of this host. Empty when the listener is loopback-only.
//
// It exists because "http://127.0.0.1:8554" answers none of the questions a person
// debugging remote access has, and the wildcard it was bound to is not a URL.
func (s *Server) ReachableURLs() []ReachableURL {
	if s.Loopback() {
		return nil
	}
	_, port, err := net.SplitHostPort(s.Addr())
	if err != nil {
		return nil
	}

	var candidates []candidateAddr
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			candidates = append(candidates, candidateAddr{Iface: iface.Name, Addr: addr})
		}
	}
	return reachableURLs(candidates, port, s.cfg.Path)
}

type candidateAddr struct {
	Iface string
	Addr  net.Addr
}

// reachableURLs keeps the addresses worth publishing: IPv4 first, no loopback, no
// link-local, no unspecified.
func reachableURLs(candidates []candidateAddr, port, path string) []ReachableURL {
	var v4, v6 []ReachableURL

	for _, c := range candidates {
		ipNet, ok := c.Addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipNet.IP
		if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			continue
		}
		// RFC 2544 benchmarking space: some VPNs hand it out as a fake-IP range, and
		// it is never an address anyone can dial from another machine.
		if v4 := ip.To4(); v4 != nil && v4[0] == 198 && (v4[1] == 18 || v4[1] == 19) {
			continue
		}

		entry := ReachableURL{
			URL:   "http://" + net.JoinHostPort(ip.String(), port) + path,
			Iface: c.Iface,
		}
		if ip.To4() != nil {
			v4 = append(v4, entry)
		} else {
			v6 = append(v6, entry)
		}
	}
	return append(v4, v6...)
}

// Loopback reports whether the listener is reachable only from this machine.
func (s *Server) Loopback() bool {
	addr, err := netip.ParseAddrPort(s.Addr())
	if err != nil {
		// Addr is either host:port or [::]:port; fall back to the cheap test.
		return strings.HasPrefix(s.Addr(), "127.") || strings.HasPrefix(s.Addr(), "[::1]")
	}
	return addr.Addr().IsLoopback()
}

// URL is the address a player on this machine should open: the port actually
// bound, with a wildcard replaced by loopback (a wildcard is not a URL).
func (s *Server) URL() string {
	host, port, err := net.SplitHostPort(s.Addr())
	if err != nil {
		return "http://" + s.cfg.Addr + s.cfg.Path
	}
	if ip, err := netip.ParseAddr(host); err != nil || ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + s.cfg.Path
}

// Resyncs reports how many times a viewer fell behind and had to be restarted at
// a keyframe. A number that keeps climbing means the player cannot keep up.
func (s *Server) Resyncs() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	total := 0
	for v := range s.viewers {
		total += v.resyncs
	}
	return total
}

// Publish offers a frame to every viewer and to the HLS segmenter. It never
// blocks.
func (s *Server) Publish(p miss.Packet) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return
	}

	// Video and audio have separate audiences and separate sinks. An audio frame
	// must never reach a video viewer: its sink is built from the first frame's
	// codec, and 1027 would produce a muxer for a stream type that does not exist.
	if p.IsVideo() {
		s.lastFrame.Store(time.Now().UnixNano())
		for v := range s.viewers {
			v.offer(p)
		}
		if s.seg != nil {
			_ = s.seg.Write(p)
		}
		return
	}

	if p.IsAudio() {
		for v := range s.audioViewers {
			v.offer(p)
		}
		// Video viewers get it too: their sink decides. The transport stream has
		// a second elementary stream for it; a viewer whose sink cannot carry
		// sound drops the frame.
		for v := range s.viewers {
			v.offer(p)
		}
		// The HLS track needs these too: an init segment that declares an audio
		// track and fragments that never carry a sample of it is a stream players
		// wait on forever, which looks exactly like a broken video.
		if s.seg != nil {
			_ = s.seg.Write(p)
		}
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
	for v := range s.audioViewers {
		delete(s.audioViewers, v)
		close(v.packets)
	}
	clear(s.hls)
	s.mu.Unlock()

	if s.http != nil {
		return s.http.Close()
	}
	return nil
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// the normal case, handled below
	case http.MethodHead:
		// Players and proxies probe with HEAD before opening a stream, and a 405
		// here reads like "the server is broken" when the stream is fine.
		w.Header().Set("Content-Type", "video/mp2t")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		return
	default:
		w.Header().Set("Allow", "GET, HEAD")
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

	factory := s.cfg.Sink
	if factory == nil {
		factory = sink.TS(s.cfg.PTSLeadMS, s.cfg.Audio)
	}

	v := &viewer{
		frames:  make(chan miss.Packet, queueDepth),
		newSink: factory,
		log:     s.log,
		remote:  clientIP(r),
		since:   time.Now(),
	}
	s.add(v)
	s.log.Info("viewer connected", "from", r.RemoteAddr, "total", s.Viewers())
	defer func() {
		s.remove(v)
		s.log.Info("viewer disconnected", "total", s.Viewers())
	}()

	v.pump(countingWriter{w: w, n: &v.bytes}, flusher)
}

// HLSBase is the URL prefix shared by the HLS endpoints: the TS path without its
// extension, so /cam.ts implies /cam.m3u8 and /cam/.
func (s *Server) HLSBase() string { return strings.TrimSuffix(s.cfg.Path, ".ts") }

// HLSPlaylistPath is where the media playlist is served.
func (s *Server) HLSPlaylistPath() string { return s.HLSBase() + ".m3u8" }

// hlsMediaBase is the segment directory as a relative URI, with a trailing
// slash: the playlist at /cam.m3u8 must name its segments "cam/seg1.m4s", since
// a bare "seg1.m4s" resolves against / rather than /cam/.
func (s *Server) hlsMediaBase() string {
	base := strings.TrimPrefix(s.HLSBase(), "/")
	if base == "" {
		return ""
	}
	return base + "/"
}

// HLSURLOf swaps the stream path of one of this server's URLs for the playlist
// path. The host and port do not change, so it works for any address the caller
// already knows is reachable.
func (s *Server) HLSURLOf(streamURL string) string {
	return strings.TrimSuffix(streamURL, s.cfg.Path) + s.HLSPlaylistPath()
}

// AudioURLOf swaps the stream path for the audio path.
func (s *Server) AudioURLOf(streamURL string) string {
	return strings.TrimSuffix(streamURL, s.cfg.Path) + s.cfg.AudioPath
}

func (s *Server) handlePlaylist(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	body := s.seg.Playlist(s.hlsMediaBase())
	s.noteHLS(r, len(body))

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(body)
	}
}

// handleHLSFile serves the init segment and the media segments, which share the
// /cam/ prefix.
func (s *Server) handleHLSFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	name := strings.TrimPrefix(r.URL.Path, s.HLSBase()+"/")

	// Names are content addresses, so the init segment is addressed like the
	// media segments rather than living at a fixed /init.mp4 that could go stale
	// when the parameter sets change.
	body, ok := s.seg.Media(name)
	if ok {
		s.noteHLS(r, len(body))
	}
	if !ok {
		// Normal: the playlist was fetched before the window slid, or the player
		// is asking for a segment that has already been recycled.
		http.Error(w, "segment is outside the window", http.StatusNotFound)
		return
	}

	writeMP4(w, r, body)
}

func writeMP4(w http.ResponseWriter, r *http.Request, body []byte) {
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(body)
	}
}

func (s *Server) add(v *viewer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		close(v.frames)
		return
	}
	s.viewers[v] = struct{}{}
	s.noteViewersLocked()
}

func (s *Server) remove(v *viewer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.viewers[v]; ok {
		delete(s.viewers, v)
		close(v.frames)
		s.noteViewersLocked()
	}
}

// noteViewersLocked restarts the idle clock when the audience empties and wakes
// anything waiting for one. The caller holds mu.
func (s *Server) noteViewersLocked() {
	if len(s.viewers)+len(s.audioViewers)+len(s.hls) > 0 {
		s.zeroSince = time.Time{}
	} else if s.zeroSince.IsZero() {
		s.zeroSince = time.Now()
	}

	select {
	case s.viewerSignal <- struct{}{}:
	default:
		// A signal is already pending; one wake-up is enough.
	}
}

// WaitForViewer blocks until somebody is watching, and reports false when ctx ends
// first.
//
// It is what makes on-demand streaming possible: the camera session is only worth
// opening when there is a client to serve.
func (s *Server) WaitForViewer(ctx context.Context) bool {
	for {
		if s.Viewers() > 0 {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-s.viewerSignal:
		}
	}
}

// IdleFor reports how long nobody has been watching, or zero while someone is.
func (s *Server) IdleFor() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.zeroSince.IsZero() {
		return 0
	}
	return time.Since(s.zeroSince)
}

// viewer is one connected player.
type viewer struct {
	frames  chan miss.Packet
	started bool
	resyncs int
	remote  string
	since   time.Time
	bytes   atomic.Int64
	log     *slog.Logger
	// newSink builds this viewer's wire format once the codec is known.
	newSink sink.Factory
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
		return
	default:
	}

	// The viewer is behind. Dropping an arbitrary frame is not an option: H.265
	// P-frames reference earlier ones, so a hole makes every following frame
	// undecodable ("Could not find ref with POC ...") until the next keyframe.
	//
	// Throw the backlog away and resynchronise at the next keyframe instead. The
	// viewer loses a fraction of a second of history and keeps a clean picture;
	// latency stays bounded at roughly one GOP rather than growing with a queue.
	v.discardBacklog()
	v.started = false
	v.resyncs++
	if v.log != nil {
		v.log.Debug("viewer behind, resynchronising at the next keyframe", "resyncs", v.resyncs)
	}

	if p.Keyframe() {
		v.started = true
		select {
		case v.frames <- p:
		default:
		}
	}
}

func (v *viewer) discardBacklog() {
	for {
		select {
		case <-v.frames:
		default:
			return
		}
	}
}

func (v *viewer) pump(w io.Writer, flusher http.Flusher) {
	var out sink.Sink
	defer func() {
		if out != nil {
			_ = out.Close()
		}
	}()

	timeout := time.NewTimer(firstFrameTimeout)
	defer timeout.Stop()

	for {
		select {
		case p, ok := <-v.frames:
			if !ok {
				return
			}
			if p.IsAudio() {
				if audio, ok := out.(sink.AudioWriter); ok && audio != nil {
					if err := audio.WriteAudio(p); err != nil {
						return
					}
					flusher.Flush()
				}
				continue
			}

			if out == nil {
				out = v.newSink(w, p.Codec)
			}
			if err := out.Write(p); err != nil {
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

// handleHealth answers /healthz for Docker healthchecks and NAS monitors.
//
// It reports whether frames are still moving rather than whether the process is
// merely alive: a wedged camera looks perfectly healthy from the outside
// otherwise. While nobody is watching there is nothing to be late for, so that
// counts as healthy too.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	last := s.lastFrame.Load()
	ageMS := int64(-1)
	if last > 0 {
		ageMS = time.Since(time.Unix(0, last)).Milliseconds()
	}

	// Healthy when nobody is waiting, when nothing has been sent yet (a camera
	// still being established is not a fault), or when frames are moving.
	healthy := s.Viewers() == 0 || last == 0 || ageMS < 15_000
	code := http.StatusOK
	if !healthy {
		code = http.StatusServiceUnavailable
	}

	body, _ := json.Marshal(map[string]any{
		"ok":            healthy,
		"viewers":       s.Viewers(),
		"last_frame_ms": ageMS,
	})

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = w.Write(append(body, '\n'))
}

// handleIndex answers / with a plain list of what this server offers. Typing the
// address into a browser is the first thing anyone does; a 404 is a poor answer.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	base := s.cfg.Path
	var b strings.Builder
	b.WriteString("micam\n=====\n\n")
	fmt.Fprintf(&b, "MPEG-TS          %s\n", base)
	if s.cfg.HLS {
		fmt.Fprintf(&b, "HLS (Safari/iOS) %s\n", s.HLSPlaylistPath())
	}
	if s.cfg.Audio {
		fmt.Fprintf(&b, "Audio (WAV)      %s\n", s.cfg.AudioPath)
	}
	fmt.Fprintf(&b, "\nviewers: %d\n", s.Viewers())
	if last := s.lastFrame.Load(); last > 0 {
		fmt.Fprintf(&b, "last frame: %s ago\n", time.Since(time.Unix(0, last)).Round(time.Second))
	} else {
		b.WriteString("last frame: never\n")
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, b.String())
}
