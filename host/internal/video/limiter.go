package video

import (
	"sync/atomic"
	"time"
)

// limiterSlack lets a source running at exactly the cap through despite
// timestamp jitter.
const limiterSlack = 0.9

// frameLimiter keeps at most fps frames a second, judged by buffer
// timestamps so it works on variable-rate sources. allow runs on the
// streaming thread; setFPS may be called from any goroutine.
type frameLimiter struct {
	interval atomic.Int64
	last     time.Duration
	have     bool
}

func newFrameLimiter(fps int) *frameLimiter {
	l := &frameLimiter{}
	l.setFPS(fps)
	return l
}

func (l *frameLimiter) setFPS(fps int) {
	l.interval.Store(int64(time.Second) / int64(max(fps, 1)))
}

// allow reports whether the frame with timestamp pts should be kept.
func (l *frameLimiter) allow(pts time.Duration) bool {
	minGap := time.Duration(float64(l.interval.Load()) * limiterSlack)
	// A timestamp going backwards means the stream restarted.
	if !l.have || pts < l.last || pts-l.last >= minGap {
		l.last, l.have = pts, true
		return true
	}
	return false
}
