package client

import (
	"sync"
	"time"
)

// replayWindowSize is how many sequence numbers behind the newest one a
// datagram may arrive and still be accepted, like the host's window.
const replayWindowSize = 1024

// replayWindow accepts each sequence number once and rejects numbers more
// than replayWindowSize behind the newest one seen. It follows the host's
// window in internal/net, so a repeated host datagram is processed once.
type replayWindow struct {
	top  uint32
	seen [replayWindowSize / 64]uint64
}

func (w *replayWindow) bit(seq uint32) (word int, mask uint64) {
	i := seq % replayWindowSize
	return int(i / 64), 1 << (i % 64)
}

func (w *replayWindow) accept(seq uint32) bool {
	if seq == 0 {
		return false
	}
	if seq > w.top {
		if seq-w.top >= replayWindowSize {
			w.seen = [replayWindowSize / 64]uint64{}
		} else {
			for s := w.top + 1; s < seq; s++ {
				word, mask := w.bit(s)
				w.seen[word] &^= mask
			}
		}
		w.top = seq
		word, mask := w.bit(seq)
		w.seen[word] |= mask
		return true
	}
	if w.top-seq >= replayWindowSize {
		return false
	}
	word, mask := w.bit(seq)
	if w.seen[word]&mask != 0 {
		return false
	}
	w.seen[word] |= mask
	return true
}

// mediaTiming sets when a silent UDP path is reported. The host sends a
// keepalive every 2 s and echoes the client's, so a working path delivers
// about one datagram a second even when the screen does not change.
type mediaTiming struct {
	// firstWait is how long the first datagram may take before the path
	// is reported quiet.
	firstWait time.Duration
	// quietAfter is how long datagrams may pause later on before the path
	// is reported quiet.
	quietAfter time.Duration
	// timeout is how long without a datagram ends the session.
	timeout time.Duration
	// check is how often the transport compares the time with these limits.
	check time.Duration
}

var defaultMediaTiming = mediaTiming{
	firstWait:  3 * time.Second,
	quietAfter: 5 * time.Second,
	timeout:    10 * time.Second,
	check:      250 * time.Millisecond,
}

// mediaEvent is a change of the UDP path's state.
type mediaEvent int

const (
	mediaNone mediaEvent = iota
	// mediaReady: the first authenticated host datagram arrived, or one
	// arrived after mediaQuiet.
	mediaReady
	// mediaQuiet: no authenticated host datagram for firstWait (none yet)
	// or quietAfter (since the last one).
	mediaQuiet
	// mediaLost: no authenticated host datagram for timeout.
	mediaLost
)

// mediaWatch follows the host's authenticated, fresh datagrams. The first
// one proves the UDP path works both ways, because the host sends nothing
// until an authenticated datagram from this client has told it where to.
type mediaWatch struct {
	timing mediaTiming

	mu    sync.Mutex
	start time.Time
	last  time.Time // zero until the first datagram
	quiet bool      // mediaQuiet was reported and no datagram came since
}

func newMediaWatch(timing mediaTiming, start time.Time) *mediaWatch {
	return &mediaWatch{timing: timing, start: start}
}

// seen records a datagram at now. It returns mediaReady for the first one
// and for the first one after a quiet report.
func (w *mediaWatch) seen(now time.Time) mediaEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	report := w.last.IsZero() || w.quiet
	w.last, w.quiet = now, false
	if report {
		return mediaReady
	}
	return mediaNone
}

// check reports mediaQuiet once per silent spell and mediaLost when the
// silence has lasted timeout.
func (w *mediaWatch) check(now time.Time) mediaEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	since, wait := w.last, w.timing.quietAfter
	if since.IsZero() {
		since, wait = w.start, w.timing.firstWait
	}
	switch idle := now.Sub(since); {
	case idle >= w.timing.timeout:
		return mediaLost
	case idle >= wait && !w.quiet:
		w.quiet = true
		return mediaQuiet
	}
	return mediaNone
}

// everUp reports whether any datagram has arrived.
func (w *mediaWatch) everUp() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return !w.last.IsZero()
}
