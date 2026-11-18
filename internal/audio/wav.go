package audio

import "encoding/binary"

// WAVHeader is a RIFF/WAVE header for a stream of unknown length.
//
// The two size fields are 0xFFFFFFFF: a stream never ends, and a player that
// waits for the length would never start. ffmpeg, VLC and every browser treat the
// all-ones size as "read until the connection closes".
func WAVHeader(sampleRate, channels int) []byte {
	const (
		pcmFormat  = 1
		bitsPerSam = 16
	)

	if channels < 1 {
		channels = 1
	}
	if sampleRate < 1 {
		sampleRate = 8000
	}

	blockAlign := channels * bitsPerSam / 8
	byteRate := sampleRate * blockAlign

	header := make([]byte, 0, 44)
	header = append(header, "RIFF"...)
	header = binary.LittleEndian.AppendUint32(header, 0xFFFFFFFF)
	header = append(header, "WAVE"...)

	header = append(header, "fmt "...)
	header = binary.LittleEndian.AppendUint32(header, 16) // PCM chunk size
	header = binary.LittleEndian.AppendUint16(header, pcmFormat)
	header = binary.LittleEndian.AppendUint16(header, uint16(channels))
	header = binary.LittleEndian.AppendUint32(header, uint32(sampleRate))
	header = binary.LittleEndian.AppendUint32(header, uint32(byteRate))
	header = binary.LittleEndian.AppendUint16(header, uint16(blockAlign))
	header = binary.LittleEndian.AppendUint16(header, bitsPerSam)

	header = append(header, "data"...)
	header = binary.LittleEndian.AppendUint32(header, 0xFFFFFFFF)

	return header
}
