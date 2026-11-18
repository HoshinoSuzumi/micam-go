package hls

// Opus in the fMP4 track, so the camera's sound travels in the same HLS stream
// as the picture.
//
// This is the one audio path that needs no encoder: the camera already sends Opus
// packets, and they are copied into the fragments as they arrive. Apple only
// documents AAC for HLS, but iOS 17 and later play Opus in fMP4, and Chromium
// supports it through MSE - so it is worth having when the alternative is
// bundling an encoder.
const (
	// opusSampleRate is what an Opus decoder always runs at, whatever the
	// encoder's input rate was.
	opusSampleRate = 48000
	// opusPreSkip is the decoder delay in 48 kHz samples that the encoder's
	// output must discard. 312 is the value libopus recommends when the encoder
	// does not say otherwise.
	opusPreSkip = 312
)

// opusChannels reads the stereo flag out of an Opus table-of-contents byte.
//
// The TOC is the first byte of every Opus packet: config(5) | stereo(1) | code(2).
func opusChannels(toc byte) int {
	if toc&0x04 != 0 {
		return 2
	}
	return 1
}

// buildOpusSampleEntry is the audio sample entry for the second track.
func buildOpusSampleEntry(channels int) []byte {
	if channels < 1 {
		channels = 1
	}

	entry := make([]byte, 0, 28+len(buildDOps(channels)))
	entry = append(entry, 0, 0, 0, 0, 0, 0) // reserved
	entry = append(entry, u16(1)...)        // data_reference_index
	entry = append(entry, u32(0)...)        // reserved (version, revision)
	entry = append(entry, u32(0)...)        // reserved (vendor)
	entry = append(entry, u16(uint16(channels))...)
	entry = append(entry, u16(16)...)                 // sample size
	entry = append(entry, u16(0)...)                  // pre_defined
	entry = append(entry, u16(0)...)                  // reserved
	entry = append(entry, u32(opusSampleRate<<16)...) // sample rate, 16.16

	return box("Opus", append(entry, buildDOps(channels)...))
}

// buildDOps is the Opus decoder configuration record ("Opus in ISOBMFF", §3).
func buildDOps(channels int) []byte {
	if channels < 1 {
		channels = 1
	}

	b := []byte{0} // Version
	b = append(b, byte(channels))
	b = append(b, u16(opusPreSkip)...)
	b = append(b, u32(opusSampleRate)...)
	b = append(b, 0, 0) // OutputGain
	b = append(b, 0)    // ChannelMappingFamily: 0, mono or stereo

	return box("dOps", b)
}
