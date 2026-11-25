package video

import "time"

// The microphone's jitter buffer: playout starts once jitterTarget is
// queued, so network jitter does not underrun it at once, and whatever is
// queued beyond jitterMax is dropped back to jitterTarget, so a burst or a
// client clock that runs fast cannot grow the delay. A frame whose turn has
// passed is dropped, never played late.
const (
	jitterTarget = 40 * time.Millisecond
	jitterMax    = 60 * time.Millisecond
	// maxConceal is how long missing frames are concealed before playout
	// stops and waits for jitterTarget again: Opus concealment fades into
	// noise after about 100 ms, and the client may have stopped sending.
	maxConceal = 100 * time.Millisecond
	// maxAheadFrames is how far from the next frame a counter may be, ahead
	// or behind; a frame further away means the client started counting
	// again, and the buffer starts again with it.
	maxAheadFrames = 50
	// defaultFrameDuration paces playout before the first frame and
	// conceals with it; the clients send 20 ms frames.
	defaultFrameDuration = 20 * time.Millisecond
)

// playout is what the buffer gives the decoder for one frame slot.
type playout int

const (
	// playIdle: nothing to play, the buffer is filling.
	playIdle playout = iota
	// playFrame: the frame due now.
	playFrame
	// playConceal: the frame due now is missing; the decoder conceals it.
	playConceal
)

// MicStats counts what the microphone's jitter buffer did with the client's
// frames.
type MicStats struct {
	// Played frames went to the decoder; Concealed slots had no frame.
	Played, Concealed uint64
	// Late frames arrived after their turn, Trimmed ones were dropped to cut
	// the delay back, Bad ones were not Opus.
	Late, Trimmed, Bad uint64
}

// jitterBuffer orders the client's frames by their 16-bit counter and hands
// one out per frame slot. It is not safe for concurrent use.
type jitterBuffer struct {
	frames map[uint16][]byte
	// next is the counter due next, once known is set; newest is the
	// highest counter queued.
	next, newest uint16
	known        bool
	playing      bool
	// played is set once a frame has played since the buffer started; until
	// then an earlier frame that arrives late moves the start back instead
	// of counting as late.
	played bool
	// missed is how long playout has concealed in a row.
	missed time.Duration
	// frameDur is the duration of the newest frame.
	frameDur time.Duration
	stats    MicStats
}

func newJitterBuffer() *jitterBuffer {
	return &jitterBuffer{frames: map[uint16][]byte{}, frameDur: defaultFrameDuration}
}

// before reports whether counter a comes before b, across the 16-bit wrap.
func before(a, b uint16) bool { return int16(a-b) < 0 }

// put queues one frame.
func (j *jitterBuffer) put(counter uint16, frame []byte) {
	d, ok := opusDuration(frame)
	if !ok {
		j.stats.Bad++
		return
	}
	if j.known {
		switch {
		case int(counter-j.next) >= maxAheadFrames && int(j.next-counter) > maxAheadFrames:
			j.restart()
		case before(counter, j.next) && (j.played || j.playing):
			j.stats.Late++
			return
		case before(counter, j.next):
			j.next = counter
		}
	}
	if !j.known {
		j.next, j.newest, j.known = counter, counter, true
	}
	if _, dup := j.frames[counter]; dup {
		return
	}
	j.frames[counter] = frame
	if before(j.newest, counter) {
		j.newest = counter
	}
	j.frameDur = d
}

func (j *jitterBuffer) restart() {
	clear(j.frames)
	j.known, j.playing, j.played, j.missed = false, false, false, 0
}

// queued is the audio waiting from the next frame to the newest one, gaps
// included: how long until the newest frame plays.
func (j *jitterBuffer) queued() time.Duration {
	if !j.known || len(j.frames) == 0 {
		return 0
	}
	return time.Duration(int(j.newest-j.next)+1) * j.frameDur
}

// oldest is the earliest counter queued.
func (j *jitterBuffer) oldest() uint16 {
	o := j.newest
	for c := range j.frames {
		if before(c, o) {
			o = c
		}
	}
	return o
}

// pop hands out what plays in the next frame slot and how long that slot
// lasts.
func (j *jitterBuffer) pop() ([]byte, time.Duration, playout) {
	if !j.playing {
		if len(j.frames) == 0 {
			return nil, j.frameDur, playIdle
		}
		// After a stall the frames due meanwhile are gone: start at the
		// first frame that came.
		j.next = j.oldest()
		if j.queued() < jitterTarget {
			return nil, j.frameDur, playIdle
		}
		j.playing = true
	}
	if j.queued() > jitterMax {
		keep := int(jitterTarget / j.frameDur)
		for int(j.newest-j.next)+1 > keep {
			if _, ok := j.frames[j.next]; ok {
				delete(j.frames, j.next)
				j.stats.Trimmed++
			}
			j.next++
		}
	}
	if f, ok := j.frames[j.next]; ok {
		delete(j.frames, j.next)
		j.next++
		j.missed = 0
		j.played = true
		j.stats.Played++
		d, _ := opusDuration(f)
		return f, d, playFrame
	}
	j.next++
	j.missed += j.frameDur
	if j.missed > maxConceal {
		j.playing, j.missed = false, 0
		return nil, j.frameDur, playIdle
	}
	j.stats.Concealed++
	return nil, j.frameDur, playConceal
}

// opusDuration is the audio in one Opus packet, from its TOC byte (RFC 6716
// section 3.1); ok is false for a packet that is not valid Opus.
func opusDuration(p []byte) (d time.Duration, ok bool) {
	if len(p) == 0 {
		return 0, false
	}
	config := p[0] >> 3
	var frame time.Duration
	switch {
	case config < 12: // SILK
		frame = [...]time.Duration{10, 20, 40, 60}[config%4] * time.Millisecond
	case config < 16: // hybrid
		frame = [...]time.Duration{10, 20}[config%2] * time.Millisecond
	default: // CELT
		frame = [...]time.Duration{2500, 5000, 10000, 20000}[config%4] * time.Microsecond
	}
	n := 1
	switch p[0] & 3 {
	case 1, 2:
		n = 2
	case 3:
		if len(p) < 2 || p[1]&0x3f == 0 {
			return 0, false
		}
		n = int(p[1] & 0x3f)
	}
	if d = frame * time.Duration(n); d > 120*time.Millisecond {
		return 0, false
	}
	return d, true
}
