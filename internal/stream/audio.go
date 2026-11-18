package stream

import (
	"net/http"
	"time"

	"micam/internal/audio"
	"micam/internal/miss"
)

const (
	// audioQueue holds roughly one to two seconds of camera audio: the frames are
	// tens of milliseconds each, and a listener that falls behind should skip
	// ahead instead of accumulating delay.
	audioQueue = 64
	// audioViewerTimeout drops a listener when the camera has gone quiet.
	audioViewerTimeout = 10 * time.Second
)

// audioViewer is one connected listener on the WAV endpoint.
type audioViewer struct {
	packets chan miss.Packet
	pcm     []byte // decode buffer, reused between frames
	warned  bool
}

// offer queues one audio frame.
//
// Audio has no keyframes, so there is nothing to resynchronise to: when the
// queue is full the oldest frame goes and the listener stays near live. A gap is
// a click; falling further and further behind is worse.
func (v *audioViewer) offer(p miss.Packet) {
	for {
		select {
		case v.packets <- p:
			return
		default:
		}

		select {
		case <-v.packets:
		default:
			return
		}
	}
}

// handleAudio streams the camera's audio as a WAV file that never ends.
func (s *Server) handleAudio(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not supported by this connection", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	// The header declares unknown sizes; a player must start immediately rather
	// than wait for a length that never arrives.
	if r.Method == http.MethodGet {
		if _, err := w.Write(audio.WAVHeader(s.cfg.AudioRate, 1)); err != nil {
			return
		}
	}
	flusher.Flush()

	if r.Method == http.MethodHead {
		return
	}

	v := &audioViewer{packets: make(chan miss.Packet, audioQueue)}
	s.addAudio(v)
	s.log.Info("audio listener connected", "from", r.RemoteAddr, "total", s.AudioListeners())
	defer func() {
		s.removeAudio(v)
		s.log.Info("audio listener disconnected", "total", s.AudioListeners())
	}()

	timeout := time.NewTimer(audioViewerTimeout)
	defer timeout.Stop()

	for {
		select {
		case p, ok := <-v.packets:
			if !ok {
				return
			}
			if p.Codec != miss.CodecPCMA {
				// Opus cannot be expanded to PCM by this package, and writing it
				// into a WAV stream would be garbage rather than silence.
				if !v.warned {
					s.log.Warn("audio codec is not G.711, the WAV endpoint cannot play it",
						"codec", p.CodecName())
					v.warned = true
				}
				continue
			}

			v.pcm = audio.DecodeALaw(v.pcm, p.Payload)
			if _, err := w.Write(v.pcm); err != nil {
				return // normal: the player disconnected
			}
			flusher.Flush()

			if !timeout.Stop() {
				select {
				case <-timeout.C:
				default:
				}
			}
			timeout.Reset(audioViewerTimeout)

		case <-timeout.C:
			return // the camera went quiet; drop the listener
		}
	}
}

func (s *Server) addAudio(v *audioViewer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		close(v.packets)
		return
	}
	s.audioViewers[v] = struct{}{}
}

func (s *Server) removeAudio(v *audioViewer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.audioViewers[v]; ok {
		delete(s.audioViewers, v)
		close(v.packets)
	}
}

// AudioListeners reports how many players are listening to the audio endpoint.
func (s *Server) AudioListeners() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.audioViewers)
}

// AudioPath is where the audio is served.
func (s *Server) AudioPath() string { return s.cfg.AudioPath }
