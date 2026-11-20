// Package hls republishes the camera stream as HLS.
//
// H.265 rules out MPEG-TS segments (Apple's profile does not allow it), so the
// segments are fragmented MP4 and the init segment carries an hvcC built from the
// parameter sets the camera sends before every keyframe.
//
// Unlike the TS path there is exactly one segmenter per stream: every viewer
// reads the same segment numbering, and cutting the same picture twice would
// only waste CPU.
package hls

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"micam/internal/miss"
	"micam/internal/ts"
)

const (
	// DefaultWindow is how many segments the playlist keeps: roughly twelve
	// seconds at this camera's two-second GOP.
	DefaultWindow = 6

	// nominalFrameMS fills in a duration when two frames carry the same
	// timestamp, which happens when a camera clock stalls.
	nominalFrameMS = 50
	// nominalAudioFrameMS is the same fallback for an Opus packet: 20ms frames
	// are the usual case.
	nominalAudioFrameMS = 20
)

// Segment is one encoded fragment and how long it plays.
type Segment struct {
	Seq      uint64
	Name     string
	Data     []byte
	Duration time.Duration
}

// segmentName is the content address of a fragment.
//
// Names have to be unique per payload rather than per position: segment numbers
// restart at zero on every reconnect, so "seg3.m4s" means different bytes in
// different sessions, and anything between us and the player that caches by name
// would happily hand over the wrong one.
func segmentName(data []byte) string {
	sum := sha256.Sum256(data)
	return "seg" + hex.EncodeToString(sum[:8]) + ".m4s"
}

// initFileName is the content address of the initialisation segment, for the same
// reason: it is rebuilt whenever the parameter sets change.
func initFileName(data []byte) string {
	sum := sha256.Sum256(data)
	return "init-" + hex.EncodeToString(sum[:6]) + ".mp4"
}

// Segmenter turns frames into a sliding window of fMP4 segments.
//
// Write is called from the media loop; Playlist, Init and Segment are called
// from HTTP handlers, hence the lock.
type Segmenter struct {
	mu     sync.RWMutex
	window int

	clock    ts.Clock
	params   paramSets
	info     spsInfo
	hasSPS   bool
	init     []byte
	initName string

	seq      uint64
	ring     []*Segment
	pending  []fragmentSample
	arrivals []int64
	mediaT   uint64
	started  bool
	dropped  int

	// audio enables an Opus track beside the video. audioChannels comes from the
	// first packet's table of contents; until then the track is declared mono,
	// and rebuilt once if it turns out to be stereo.
	audio         bool
	audioChannels int
	pendingAudio  []fragmentSample
	audioArrivals []int64
}

// paramSets are the VPS/SPS/PPS triple carried in-band before each keyframe.
type paramSets struct {
	vps, sps, pps []byte
}

// update stores any parameter sets in nals and reports whether they changed.
func (p *paramSets) update(nals [][]byte) bool {
	changed := false
	for _, nal := range nals {
		var dst *[]byte
		switch nalType(nal) {
		case nalTypeVPS:
			dst = &p.vps
		case nalTypeSPS:
			dst = &p.sps
		case nalTypePPS:
			dst = &p.pps
		default:
			continue
		}
		if !bytes.Equal(*dst, nal) {
			*dst = append([]byte(nil), nal...)
			changed = true
		}
	}
	return changed
}

// New builds a segmenter. A window below one is replaced by the default.
//
// withAudio declares the Opus track from the start: adding it later would change
// the init segment, and with it every segment name, forcing connected players to
// re-initialise mid-stream.
func New(window int, withAudio bool) *Segmenter {
	if window < 1 {
		window = DefaultWindow
	}
	return &Segmenter{window: window, audio: withAudio}
}

// Write offers one access unit to the segmenter. It never blocks on viewers:
// segments go into a ring buffer and the handlers read from there.
func (s *Segmenter) Write(p miss.Packet) error {
	if p.IsAudio() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.writeAudio(p)
		return nil
	}

	if !p.IsVideo() || p.Codec != miss.CodecH265 {
		// H.264 would need an avcC record and slightly different NAL rules.
		return nil
	}

	nals := splitNALs(p.Payload)
	if len(nals) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.params.update(nals) {
		if info, err := parseSPS(s.params.sps); err == nil {
			s.info = info
			s.hasSPS = true
			// Rebuild now, not later. The init used to be dropped here and
			// rebuilt only in the "stream has not started" branch below, so a
			// camera that re-sent its parameter sets mid-stream left the playlist
			// with no EXT-X-MAP for the rest of the session: players then had no
			// hvcC and could not build a decoder, hours after everything had been
			// working.
			if s.ready() {
				s.init = buildInit(s.info, s.params, s.audioTrackChannels())
				s.initName = initFileName(s.init)
			}
		}
	}

	// A parameter-set-only packet carries no picture; it has already been
	// absorbed into the init segment.
	sample, hasPicture := buildSample(nals)
	if !hasPicture {
		return nil
	}

	elapsed := s.clock.Advance(p.TimestampMS)
	key := p.Keyframe() || hasIRAP(nals)

	if key && len(s.pending) > 0 {
		s.flush(elapsed)
	}

	if !s.started {
		if !key || !s.ready() {
			s.dropped++
			return nil
		}
		s.started = true
		s.init = buildInit(s.info, s.params, s.audioTrackChannels())
		s.initName = initFileName(s.init)
	}

	s.pending = append(s.pending, fragmentSample{data: sample, key: key})
	s.arrivals = append(s.arrivals, elapsed)
	return nil
}

// ready reports whether an init segment can be built at all.
func (s *Segmenter) ready() bool {
	return s.hasSPS && len(s.params.vps) > 0 && len(s.params.sps) > 0 && len(s.params.pps) > 0
}

// audioTrackChannels is the channel count to declare for the Opus track, or zero
// when there is no audio track at all.
func (s *Segmenter) audioTrackChannels() int {
	if !s.audio {
		return 0
	}
	if s.audioChannels == 0 {
		// Nothing has arrived yet; mono is the safe guess and it is rebuilt once
		// the first packet says otherwise.
		return 1
	}
	return s.audioChannels
}

// writeAudio queues one Opus packet. The caller holds the lock.
func (s *Segmenter) writeAudio(p miss.Packet) {
	if !s.audio || !s.started || len(p.Payload) == 0 {
		return
	}

	origin, ok := s.clock.Origin()
	if !ok {
		return
	}

	// The first packet decides the channel count. Rebuilding the init changes the
	// segment names, which is why it happens once, at the very start.
	if s.audioChannels == 0 {
		channels := opusChannels(p.Payload[0])
		if channels != 1 || s.init != nil {
			s.audioChannels = channels
			if s.init != nil {
				s.init = buildInit(s.info, s.params, s.audioTrackChannels())
				s.initName = initFileName(s.init)
			}
		} else {
			s.audioChannels = channels
		}
	}

	elapsed := p.TimestampMS - origin
	if elapsed < 0 {
		elapsed = 0
	}

	s.pendingAudio = append(s.pendingAudio, fragmentSample{
		data: append([]byte(nil), p.Payload...),
		key:  true, // every Opus packet decodes on its own
	})
	s.audioArrivals = append(s.audioArrivals, elapsed)
}

// buildSample turns the picture NALs of one access unit into a length-prefixed
// sample. fMP4 stores NALs length-prefixed, not with Annex-B start codes.
//
// The parameter sets stay in the sample even though the init segment carries
// them in hvcC as well: the sample entry is hev1, which allows in-band parameter
// sets, and the Windows decoders behind Edge and Chrome are happier when every
// keyframe can be decoded on its own.
func buildSample(nals [][]byte) ([]byte, bool) {
	var out []byte
	hasPicture := false

	for _, nal := range nals {
		if isVCL(nalType(nal)) {
			hasPicture = true
		}
		out = binary.BigEndian.AppendUint32(out, uint32(len(nal)))
		out = append(out, nal...)
	}

	return out, hasPicture
}

// hasIRAP reports whether the access unit carries a picture a decoder can start
// from, independently of the camera's keyframe flag.
func hasIRAP(nals [][]byte) bool {
	for _, nal := range nals {
		if t := nalType(nal); t >= nalTypeBlaWLp && t <= nalTypeRsvIRAP {
			return true
		}
	}
	return false
}

// flush closes the open segment at endMS, which is the arrival time of the frame
// that starts the next one.
func (s *Segmenter) flush(endMS int64) {
	if len(s.pending) == 0 {
		return
	}

	// A playlist must never advertise media without the init segment that
	// describes it: a player that fetches those segments has no hvcC and cannot
	// decode a single frame. Dropping the fragments is the lesser evil, and it
	// cannot happen silently for long because the next keyframe rebuilds the init.
	if s.init == nil {
		s.pending = s.pending[:0]
		s.arrivals = s.arrivals[:0]
		s.started = false
		s.dropped++
		return
	}

	total := uint32(0)
	for i := range s.pending {
		end := endMS
		if i+1 < len(s.pending) {
			end = s.arrivals[i+1]
		}
		ms := end - s.arrivals[i]
		if ms <= 0 {
			ms = nominalFrameMS
		}
		s.pending[i].duration = uint32(ms * mp4Timescale / 1000)
		if s.pending[i].duration == 0 {
			s.pending[i].duration = 1
		}
		total += s.pending[i].duration
	}

	// Audio spans the same interval, so it is trimmed and measured the same way.
	// Its timeline starts from the video clock's origin, which keeps the two
	// tracks aligned even when sound only begins seconds after the picture.
	audioBase := uint64(0)
	if len(s.pendingAudio) > 0 {
		audioBase = uint64(s.audioArrivals[0]) * mp4Timescale / 1000
		for i := range s.pendingAudio {
			end := endMS
			if i+1 < len(s.pendingAudio) {
				end = s.audioArrivals[i+1]
			}
			ms := end - s.audioArrivals[i]
			if ms <= 0 {
				ms = nominalAudioFrameMS
			}
			s.pendingAudio[i].duration = uint32(ms * mp4Timescale / 1000)
			if s.pendingAudio[i].duration == 0 {
				s.pendingAudio[i].duration = 1
			}
		}
	}

	data := buildFragment(uint32(s.seq+1), s.mediaT, audioBase, s.pending, s.pendingAudio)

	seg := &Segment{
		Seq:      s.seq,
		Name:     segmentName(data),
		Data:     data,
		Duration: time.Duration(int64(total) * int64(time.Second) / mp4Timescale),
	}

	s.mediaT += uint64(total)
	s.ring = append(s.ring, seg)
	if len(s.ring) > s.window {
		s.ring = s.ring[len(s.ring)-s.window:]
	}

	s.seq++
	s.pending = s.pending[:0]
	s.arrivals = s.arrivals[:0]
	s.pendingAudio = s.pendingAudio[:0]
	s.audioArrivals = s.audioArrivals[:0]
}

// Flush closes the open segment early.
//
// A live stream never needs this: segments end when the next keyframe arrives.
// It exists for a stopping stream, and for the tests, which otherwise lose the
// last partial segment.
func (s *Segmenter) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) == 0 {
		return
	}
	s.flush(s.arrivals[len(s.arrivals)-1] + nominalFrameMS)
}

// Init returns the initialisation segment, or false before the first keyframe
// that carried parameter sets.
func (s *Segmenter) Init() ([]byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.init == nil {
		return nil, false
	}
	return s.init, true
}

// Segment returns the segment with this number, or false once it has slid out of
// the window.
func (s *Segmenter) Segment(seq uint64) ([]byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, seg := range s.ring {
		if seg.Seq == seq {
			return seg.Data, true
		}
	}
	return nil, false
}

// Media returns whatever a playlist URI names: the init segment or a fragment.
//
// One lookup for both because the names are content addresses either way, and a
// caller serving a URI should not have to know which kind it is holding.
func (s *Segmenter) Media(name string) ([]byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if name != "" && name == s.initName {
		return s.init, true
	}
	for _, seg := range s.ring {
		if seg.Name == name {
			return seg.Data, true
		}
	}
	return nil, false
}

// InitName is the file name of the current initialisation segment.
func (s *Segmenter) InitName() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.init == nil {
		return ""
	}
	return s.initName
}

// Playlist renders the media playlist for the current window.
//
// base is the directory the segments live in, relative to the playlist itself,
// and it must not be empty-looking: a playlist at /cam.m3u8 whose entries are
// "seg3.m4s" tells the player to fetch /seg3.m4s, because relative URIs resolve
// against the playlist's directory. Passing "cam/" makes them /cam/seg3.m4s.
func (s *Segmenter) Playlist(base string) []byte {
	return s.PlaylistWith(base, "")
}

// PlaylistWith appends a query string to every URI it emits.
//
// This is how access control reaches an Apple player: the playlist is the only
// thing that can carry a credential into the segment requests, because <video>
// cannot set a header and Safari resolves the relative URIs itself. query must
// include the leading "?", or be empty.
func (s *Segmenter) PlaylistWith(base, query string) []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()

	target := 1
	for _, seg := range s.ring {
		if d := int(seg.Duration.Seconds() + 0.999); d > target {
			target = d
		}
	}

	var b bytes.Buffer
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n")
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", target)
	if len(s.ring) > 0 {
		fmt.Fprintf(&b, "#EXT-X-MEDIA-SEQUENCE:%d\n", s.ring[0].Seq)
	}
	if s.initName != "" {
		fmt.Fprintf(&b, "#EXT-X-MAP:URI=\"%s%s%s\"\n", base, s.initName, query)
	}
	for _, seg := range s.ring {
		fmt.Fprintf(&b, "#EXTINF:%.3f,\n%s%s%s\n", seg.Duration.Seconds(), base, seg.Name, query)
	}

	return b.Bytes()
}

// Segments reports how many segments the window currently holds.
func (s *Segmenter) Segments() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.ring)
}

// Dropped reports how many frames arrived before the stream was decodable.
func (s *Segmenter) Dropped() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dropped
}
