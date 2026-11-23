package client

import (
	"slices"
	"sync"
	"time"
)

// clockWindow is how many recent keepalives and round trips the clock
// estimate uses: the host sends one every 2 s, so about half a minute.
const clockWindow = 16

// maxBelievableDelay bounds a frame's delay; a larger one means the clock
// estimate is off, for example right after a clock jump.
const maxBelievableDelay = 5 * time.Second

// hostClock estimates how far this computer's clock is ahead of the host's,
// so a frame's capture time, which the host stamps with its clock, can be
// compared with the time here. Each host keepalive says when the host sent
// it: its arrival time minus that is the offset plus the trip across. The
// smallest such gap of recent keepalives is the offset plus the quickest
// trip, taken as half the quickest recent round trip. This assumes the path
// is about as fast both ways, as on a LAN or Wi-Fi.
type hostClock struct {
	mu   sync.Mutex
	gaps []time.Duration
	rtts []time.Duration
}

func pushWindow(w []time.Duration, d time.Duration) []time.Duration {
	if len(w) >= clockWindow {
		w = w[1:]
	}
	return append(w, d)
}

// keepalive records a host keepalive sent at sent (host clock) that arrived
// at arrived (this clock).
func (c *hostClock) keepalive(sent, arrived time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gaps = pushWindow(c.gaps, arrived.Sub(sent))
}

// roundTrip records the round trip of one of our keepalives.
func (c *hostClock) roundTrip(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rtts = pushWindow(c.rtts, d)
}

// offset is how far this clock is ahead of the host's; ok is false until a
// keepalive and a round trip have been seen.
func (c *hostClock) offset() (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.gaps) == 0 || len(c.rtts) == 0 {
		return 0, false
	}
	return slices.Min(c.gaps) - slices.Min(c.rtts)/2, true
}

// since is how long before now (this clock) the host captured a frame it
// stamped captured (host clock).
func (c *hostClock) since(captured, now time.Time) (time.Duration, bool) {
	off, ok := c.offset()
	if !ok {
		return 0, false
	}
	d := max(now.Sub(captured)-off, 0)
	return d, d < maxBelievableDelay
}

// delayMeter averages the delays of the frames shown in one stats period.
type delayMeter struct {
	mu  sync.Mutex
	sum time.Duration
	n   int
}

func (m *delayMeter) add(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sum += d
	m.n++
}

// take returns the average since the last take; ok is false when no frame
// was shown.
func (m *delayMeter) take() (time.Duration, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.n == 0 {
		return 0, false
	}
	avg := m.sum / time.Duration(m.n)
	m.sum, m.n = 0, 0
	return avg, true
}
