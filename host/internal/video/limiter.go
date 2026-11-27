package video

import (
	"sync/atomic"
	"time"
)

// limiterSlack lets a source running at exactly the cap through despite
// timestamp jitter.
const limiterSlack = 0.9

// staticResend is how often a still screen is encoded: pipewiresrc resends
// the last frame while Mutter sends none, and the limiter keeps a resend
// once this long has passed since the last frame it kept, so a forced
// keyframe comes within this time.
const staticResend = 100 * time.Millisecond

// resendAfterMs is pipewiresrc's keepalive-time for a stream at fps: half a
// frame interval, at least 2 ms. A frame the limiter dropped because it came
// too soon is the newest picture when the screen then stops changing (the
// pointer's last position, the last key typed); the resend brings it back
// this soon instead of after staticResend.
func resendAfterMs(fps int) int {
	if fps <= 0 {
		fps = defaultFPS
	}
	return max(1000/fps/2, 2)
}

// frameLimiter keeps at most fps frames a second, judged by buffer
// timestamps so it works on variable-rate sources. A new frame that comes
// too soon is dropped, and the source's next resend of it is kept as soon as
// the interval allows. allow runs on the streaming thread; setFPS may be
// called from any goroutine.
type frameLimiter struct {
	interval atomic.Int64
	last     time.Duration
	have     bool
	// dropped says that a new frame was dropped after the last frame kept,
	// so the screen shows something the encoder has not seen.
	dropped bool
}

func newFrameLimiter(fps int) *frameLimiter {
	l := &frameLimiter{}
	l.setFPS(fps)
	return l
}

func (l *frameLimiter) setFPS(fps int) {
	l.interval.Store(int64(time.Second) / int64(max(fps, 1)))
}

// allow reports whether the frame with timestamp pts should be kept. resend
// says that the source repeats the frame before it (pipewiresrc's
// keepalive): it is kept only to deliver a dropped frame, or every
// staticResend on a still screen.
func (l *frameLimiter) allow(pts time.Duration, resend bool) bool {
	minGap := time.Duration(float64(l.interval.Load()) * limiterSlack)
	gap := pts - l.last
	// A timestamp going backwards means the stream restarted.
	keep := !l.have || pts < l.last
	switch {
	case keep:
	case resend:
		keep = (l.dropped && gap >= minGap) || gap >= staticResend
	default:
		keep = gap >= minGap
		l.dropped = l.dropped || !keep
	}
	if keep {
		l.last, l.have, l.dropped = pts, true, false
	}
	return keep
}

// resendDetector tells pipewiresrc's keepalive resends from new frames by
// the memory they carry. A resend is a copy of the last frame and shares
// its memory. Each new frame's memory is allocated while pipewiresrc still
// holds the frame before it for resending, so it never has the same address
// as the buffer before it. This holds only for buffers in the order the
// source pushes them, before any queue drops one.
type resendDetector struct{ last uintptr }

// resend reports whether the buffer with first memory mem repeats the one
// before it.
func (d *resendDetector) resend(mem uintptr) bool {
	same := mem != 0 && mem == d.last
	d.last = mem
	return same
}
