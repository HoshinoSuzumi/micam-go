package ts

// Clock turns a camera timestamp into a strictly increasing millisecond offset.
//
// Camera clocks are not dependable: one model reports an uptime counter, another
// reports epoch milliseconds that repeat and occasionally run backwards. Both
// MPEG-TS and fMP4 require monotonic timestamps, so plausible deltas are trusted
// and anything else falls back to the nominal frame interval.
//
// It lives here because the two output formats must agree: if the TS and HLS
// paths repaired the clock differently, the two views of the same stream would
// drift apart.
type Clock struct {
	started   bool
	origin    int64
	previous  int64
	elapsedMS int64
}

// Advance reports how many milliseconds have elapsed since the first frame.
//
// The first call returns zero: a stream that starts at t=0 is what players
// expect, and it keeps the absolute camera time out of the output entirely.
func (c *Clock) Advance(timestampMS int64) int64 {
	if !c.started {
		// A flag rather than a negative sentinel: cameras have been seen
		// reporting negative values, and a sentinel would read as "not started"
		// forever, freezing the clock.
		c.started = true
		c.origin = timestampMS
		c.previous = timestampMS
		return c.elapsedMS
	}

	delta := timestampMS - c.previous
	c.previous = timestampMS
	if delta > 0 && delta <= maxPlausibleGapMS {
		c.elapsedMS += delta
	} else {
		c.elapsedMS += nominalFrameMS
	}
	return c.elapsedMS
}

// Origin reports the camera timestamp of the first frame, which is what a second
// track needs to place itself on the same timeline.
//
// Audio cannot use its own clock: it may start seconds after the video (or only
// when there is noise), and normalising it independently would shift the sound
// against the picture by however long that gap was.
//
// It must be the first timestamp, not the latest: returning the moving "previous"
// value made every audio packet look like it happened at the start of the stream,
// which put the audio track a hundred seconds away from the video and stalled
// playback a couple of seconds in.
func (c *Clock) Origin() (int64, bool) {
	return c.origin, c.started
}
