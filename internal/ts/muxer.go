// Package ts wraps H.264/H.265 access units in an MPEG-TS stream.
//
// Muxing only: the elementary stream is passed through untouched, so nothing on
// this path decodes or re-encodes video. One Muxer serves one viewer, because
// continuity counters and the timestamp origin are per-stream state.
package ts

import (
	"fmt"
	"io"

	"micam/internal/miss"
)

const (
	packetSize = 188
	syncByte   = 0x47
	// maxPayload is the payload of a packet with no adaptation field.
	maxPayload = 184
	// pcrFieldLen is the adaptation field content when carrying a PCR: a flags
	// byte plus six PCR bytes.
	pcrFieldLen = 7

	pidPAT   uint16 = 0x0000
	pidPMT   uint16 = 0x1000
	pidVideo uint16 = 0x0100
	pidAudio uint16 = 0x0101

	streamTypeH264 = 0x1B
	streamTypeH265 = 0x24
	// streamTypePrivate is what a private stream carries; the codec inside is named
	// by a registration descriptor. Opus in MPEG-TS is not given its own
	// stream_type, so this is the shape ffmpeg writes and reads.
	streamTypePrivate = 0x06
	streamIDVideo     = 0xE0
	streamIDPrivate   = 0xBD

	// clockHz is the MPEG-TS timestamp clock.
	clockHz = 90_000

	// DefaultPTSLeadMS is how far presentation runs ahead of the transport clock.
	// PCR is when a packet is transmitted, PTS when its frame is shown; equal
	// values leave no margin and strict players (mpv, VLC) drop every frame.
	//
	// Upstream uses 300ms. This fork defaults to 100: it is a third of the added
	// latency and still leaves a decoder room to work. Raise it if a software
	// decoder starts dropping frames.
	DefaultPTSLeadMS = 100
	// maxPTSLeadMS bounds the tunable lead.
	maxPTSLeadMS = 2_000

	// tableIntervalFrames re-announces the tables so a decoder can resynchronise.
	tableIntervalFrames = 20
	// nominalFrameMS is the assumed spacing when a camera timestamp cannot be
	// trusted: 20 fps.
	nominalFrameMS = 50
	// maxPlausibleGapMS is the largest delta treated as real rather than a clock
	// glitch.
	maxPlausibleGapMS = 1_000
)

// Muxer writes one transport stream.
type Muxer struct {
	w          io.Writer
	streamType byte

	patCounter   byte
	pmtCounter   byte
	videoCounter byte
	audioCounter byte

	// withAudio declares a second elementary stream for the camera's Opus.
	withAudio bool

	clock Clock

	ptsOffset int64

	framesSinceTables int
}

// NewMuxer builds a muxer for the given codec id with the default PTS lead.
func NewMuxer(w io.Writer, codec uint32) *Muxer {
	return NewMuxerWithLead(w, codec, DefaultPTSLeadMS)
}

// NewMuxerWithLead builds a muxer with an explicit presentation lead, in
// milliseconds. Zero means presentation and transmission coincide, which is the
// lowest latency a player can honour and the least forgiving.
func NewMuxerWithLead(w io.Writer, codec uint32, leadMS int) *Muxer {
	streamType := byte(streamTypeH265)
	if codec == miss.CodecH264 {
		streamType = streamTypeH264
	}
	if leadMS < 0 {
		leadMS = 0
	}
	if leadMS > maxPTSLeadMS {
		leadMS = maxPTSLeadMS
	}
	return &Muxer{
		w:                 w,
		streamType:        streamType,
		ptsOffset:         int64(leadMS) * clockHz / 1000,
		framesSinceTables: 1 << 30, // force the tables out on the first frame
	}
}

// NewMuxerWithAudio is NewMuxerWithLead plus the private Opus stream. The PMT
// declares it from the first table, so a player never sees an audio PID appear
// mid-stream.
func NewMuxerWithAudio(w io.Writer, codec uint32, leadMS int, withAudio bool) *Muxer {
	m := NewMuxerWithLead(w, codec, leadMS)
	m.withAudio = withAudio
	return m
}

// Write emits one access unit, with the PAT/PMT when they are due.
func (m *Muxer) Write(p miss.Packet) error {
	if m.framesSinceTables >= tableIntervalFrames || p.Keyframe() {
		if err := m.writePAT(); err != nil {
			return err
		}
		if err := m.writePMT(); err != nil {
			return err
		}
		m.framesSinceTables = 0
	}
	m.framesSinceTables++

	elapsed := m.advanceClock(p.TimestampMS) * clockHz / 1000
	return m.writeVideo(p.Payload, m.ptsOffset+elapsed, elapsed, p.Keyframe())
}

// Flush pushes the underlying writer.
func (m *Muxer) Flush() error {
	if f, ok := m.w.(interface{ Flush() error }); ok {
		return f.Flush()
	}
	return nil
}

// advanceClock converts a camera timestamp into a strictly increasing
// millisecond offset.
//
// Camera clocks are not dependable: one model reports an uptime counter, another
// reports epoch milliseconds that repeat and occasionally run backwards. MPEG-TS
// requires monotonic timestamps, so plausible deltas are trusted and anything
// else falls back to the nominal frame interval.
func (m *Muxer) advanceClock(timestampMS int64) int64 {
	return m.clock.Advance(timestampMS)
}

// ---------------------------------------------------------------- tables ---

func (m *Muxer) writePAT() error {
	section := make([]byte, 12)
	section[0] = 0x00 // table_id
	section[1] = 0xB0 // syntax indicator + length high bits
	section[2] = 0x0D // section_length = 13
	section[3] = 0x00
	section[4] = 0x01 // transport_stream_id
	section[5] = 0xC1 // version 0, current
	section[6] = 0x00 // section_number
	section[7] = 0x00 // last_section_number
	section[8] = 0x00
	section[9] = 0x01 // program_number
	section[10] = 0xE0 | byte(pidPMT>>8)
	section[11] = byte(pidPMT & 0xFF)

	if err := m.writeSection(pidPAT, section, m.patCounter); err != nil {
		return err
	}
	m.patCounter = (m.patCounter + 1) & 0x0F
	return nil
}

func (m *Muxer) writePMT() error {
	// The PMT grows by one stream entry (with its descriptors) when audio is on.
	// Section length is everything after the length field: 5 bytes of header
	// before the streams, then each entry, then the 4-byte CRC.
	extra := 0
	if m.withAudio {
		extra = 5 + opusDescriptorsLen
	}

	section := make([]byte, 17+extra)
	section[0] = 0x02 // table_id
	section[1] = 0xB0
	section[2] = byte(0x12 + extra) // section_length
	section[3] = 0x00
	section[4] = 0x01 // program_number
	section[5] = 0xC1
	section[6] = 0x00
	section[7] = 0x00
	section[8] = 0xE0 | byte(pidVideo>>8) // PCR pid
	section[9] = byte(pidVideo & 0xFF)
	section[10] = 0xF0
	section[11] = 0x00 // program_info_length
	section[12] = m.streamType
	section[13] = 0xE0 | byte(pidVideo>>8)
	section[14] = byte(pidVideo & 0xFF)
	section[15] = 0xF0
	section[16] = 0x00 // ES_info_length

	at := 17
	if m.withAudio {
		section[at] = streamTypePrivate
		section[at+1] = 0xE0 | byte(pidAudio>>8)
		section[at+2] = byte(pidAudio & 0xFF)
		section[at+3] = 0xF0 | byte(opusDescriptorsLen>>8)
		section[at+4] = byte(opusDescriptorsLen)
		copy(section[at+5:], opusDescriptors[:])
	}

	if err := m.writeSection(pidPMT, section, m.pmtCounter); err != nil {
		return err
	}
	m.pmtCounter = (m.pmtCounter + 1) & 0x0F
	return nil
}

// opusDescriptors are the ES descriptors that tell a demuxer the private stream is
// Opus. A registration descriptor naming the format, plus the extension
// descriptor ffmpeg writes alongside it.
var opusDescriptors = [10]byte{
	0x05, 0x04, 'O', 'p', 'u', 's', // registration_descriptor
	0x7F, 0x02, 0x80, 0x01, // extension_descriptor, as ffmpeg emits it
}

// opusDescriptorsLen is a constant so the PMT section length can be computed at
// compile time; len() of a slice is not.
const opusDescriptorsLen = len(opusDescriptors)

// writeSection emits a short table: pointer field, section, CRC, stuffing.
func (m *Muxer) writeSection(pid uint16, section []byte, counter byte) error {
	packet := make([]byte, packetSize)
	for i := range packet {
		packet[i] = 0xFF
	}
	packet[0] = syncByte
	packet[1] = 0x40 | byte(pid>>8) // payload_unit_start_indicator
	packet[2] = byte(pid)
	packet[3] = 0x10 | counter // payload only
	packet[4] = 0x00           // pointer_field
	copy(packet[5:], section)

	crc := crc32Mpeg(section)
	at := 5 + len(section)
	packet[at] = byte(crc >> 24)
	packet[at+1] = byte(crc >> 16)
	packet[at+2] = byte(crc >> 8)
	packet[at+3] = byte(crc)

	_, err := m.w.Write(packet)
	return err
}

// ----------------------------------------------------------------- video ---

func (m *Muxer) writeVideo(accessUnit []byte, pts, pcr int64, keyframe bool) error {
	return m.writeElementary(pidVideo, &m.videoCounter, buildPESHeader(pts), accessUnit, true, pcr, keyframe)
}

// WriteAudio emits one Opus frame on the private audio stream.
//
// The timestamp is measured from the video clock's origin, the same rule the HLS
// segmenter follows: normalising audio on its own would shift the sound against
// the picture whenever the camera started sending audio later than video.
func (m *Muxer) WriteAudio(p miss.Packet) error {
	if !m.withAudio || len(p.Payload) == 0 {
		return nil
	}

	origin, ok := m.clock.Origin()
	if !ok {
		// No picture yet, so there is no timeline to align to and no PMT either.
		return nil
	}

	elapsed := p.TimestampMS - origin
	if elapsed < 0 {
		elapsed = 0
	}

	pts := m.ptsOffset + elapsed*clockHz/1000
	return m.writeElementary(pidAudio, &m.audioCounter, buildAudioPESHeader(pts, len(p.Payload)), p.Payload, false, 0, false)
}

// writeElementary splits one PES packet across TS packets.
func (m *Muxer) writeElementary(pid uint16, counter *byte, pes, unit []byte, withPCR bool, pcr int64, keyframe bool) error {
	payloadLength := len(pes) + len(unit)

	written := 0
	first := true
	for written < payloadLength {
		packet := make([]byte, packetSize)
		packet[0] = syncByte
		if first {
			packet[1] = 0x40 | byte(pid>>8)
		} else {
			packet[1] = byte(pid >> 8)
		}
		packet[2] = byte(pid & 0xFF)

		remaining := payloadLength - written

		// PCR rides on the first packet of every access unit, and a short final
		// packet is padded with adaptation-field stuffing, because TS packets are
		// always 188 bytes.
		useAdaptation := (first && withPCR) || remaining < maxPayload
		fieldLength := 0
		if first && withPCR {
			fieldLength = pcrFieldLen
		}

		capacity := maxPayload
		if useAdaptation {
			capacity = maxPayload - 1 - fieldLength
			if remaining < capacity {
				fieldLength += capacity - remaining
				capacity = remaining
			}
		}

		var offset int
		if useAdaptation {
			packet[3] = 0x30 | *counter
			offset = writeAdaptationField(packet, fieldLength, first && withPCR, keyframe, pcr)
		} else {
			packet[3] = 0x10 | *counter
			offset = 4
		}
		*counter = (*counter + 1) & 0x0F

		chunk := capacity
		if remaining < chunk {
			chunk = remaining
		}
		copyPayload(packet, offset, pes, unit, written, chunk)
		written += chunk
		first = false

		if _, err := m.w.Write(packet); err != nil {
			return err
		}
	}
	return nil
}

// copyPayload copies from the logical concatenation of PES header and access
// unit into one packet.
func copyPayload(packet []byte, offset int, pes, unit []byte, from, length int) {
	done := 0
	for done < length {
		position := from + done
		if position < len(pes) {
			take := len(pes) - position
			if length-done < take {
				take = length - done
			}
			copy(packet[offset+done:], pes[position:position+take])
			done += take
			continue
		}
		take := length - done
		start := position - len(pes)
		copy(packet[offset+done:], unit[start:start+take])
		done += take
	}
}

// writeAdaptationField writes the adaptation field and returns the offset where
// the payload starts.
func writeAdaptationField(packet []byte, fieldLength int, withPCR, keyframe bool, pcr int64) int {
	packet[4] = byte(fieldLength)
	index := 5
	if fieldLength > 0 {
		var flags byte
		if keyframe {
			flags |= 0x40 // random_access_indicator
		}
		if withPCR {
			flags |= 0x10 // PCR_flag
		}
		packet[index] = flags
		index++
		if withPCR {
			packet[index] = byte(pcr >> 25)
			packet[index+1] = byte(pcr >> 17)
			packet[index+2] = byte(pcr >> 9)
			packet[index+3] = byte(pcr >> 1)
			packet[index+4] = byte((pcr&1)<<7 | 0x7E)
			packet[index+5] = 0x00 // PCR extension
			index += 6
		}
		for index < 5+fieldLength {
			packet[index] = 0xFF // stuffing
			index++
		}
	}
	return 5 + fieldLength
}

// buildPESHeader builds a PES header carrying only a PTS. Video packets may
// declare length zero, which means "until the next start", avoiding the 64KB
// limit a keyframe would exceed.
func buildPESHeader(pts int64) []byte {
	header := make([]byte, 14)
	header[0] = 0x00
	header[1] = 0x00
	header[2] = 0x01
	header[3] = streamIDVideo
	header[4] = 0x00 // unbounded length
	header[5] = 0x00
	header[6] = 0x80 // marker bits
	header[7] = 0x80 // PTS present
	header[8] = 0x05 // header data length
	writePTS(header, 9, pts, 0x02)
	return header
}

// buildAudioPESHeader is the private-stream PES header for Opus: stream_id 0xBD,
// a real packet length (audio frames are small enough to state it), and a PTS
// with no DTS.
func buildAudioPESHeader(pts int64, payloadLen int) []byte {
	header := make([]byte, 14)
	header[0] = 0x00
	header[1] = 0x00
	header[2] = 0x01
	header[3] = streamIDPrivate
	length := 3 + 5 + payloadLen // the three flag bytes, the PTS, the payload
	header[4] = byte(length >> 8)
	header[5] = byte(length)
	header[6] = 0x80 // marker bits
	header[7] = 0x80 // PTS present, no DTS
	header[8] = 0x05 // header data length
	writePTS(header, 9, pts, 0x02)
	return header
}

// writePTS spreads a 33-bit timestamp over five bytes with marker bits.
func writePTS(out []byte, offset int, value int64, prefix byte) {
	out[offset] = prefix<<4 | byte((value>>30)&0x07)<<1 | 0x01
	out[offset+1] = byte(value >> 22)
	out[offset+2] = byte((value>>15)&0x7F)<<1 | 0x01
	out[offset+3] = byte(value >> 7)
	out[offset+4] = byte(value&0x7F)<<1 | 0x01
}

// crc32Mpeg is CRC-32/MPEG-2: polynomial 0x04C11DB7, all-ones seed, no final
// inversion.
func crc32Mpeg(data []byte) uint32 {
	crc := uint32(0xFFFFFFFF)
	for _, b := range data {
		crc ^= uint32(b) << 24
		for i := 0; i < 8; i++ {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04C11DB7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// String describes the muxer for logs.
func (m *Muxer) String() string {
	name := "H265"
	if m.streamType == streamTypeH264 {
		name = "H264"
	}
	return fmt.Sprintf("ts.Muxer(%s)", name)
}
