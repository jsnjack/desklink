package client

import (
	"testing"
	"time"
)

func TestHostClockMeasuresDelayAcrossClocks(t *testing.T) {
	var c hostClock
	base := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	// This clock is 250 ms ahead of the host's; trips take 3 ms each way,
	// one keepalive was held up 20 ms in a queue.
	const ahead = 250 * time.Millisecond
	if _, ok := c.since(base, base); ok {
		t.Error("a delay before any keepalive")
	}
	for i, queued := range []time.Duration{0, 20 * time.Millisecond, 0} {
		sent := base.Add(time.Duration(i) * 2 * time.Second)
		c.keepalive(sent, sent.Add(ahead+3*time.Millisecond+queued))
		c.roundTrip(6*time.Millisecond + queued)
	}
	if off, ok := c.offset(); !ok || off != ahead {
		t.Fatalf("offset = %v %v, want %v", off, ok, ahead)
	}
	// A frame the host captured at 10 s is shown here 21 ms later.
	captured := base.Add(10 * time.Second)
	if d, ok := c.since(captured, captured.Add(ahead+21*time.Millisecond)); !ok || d != 21*time.Millisecond {
		t.Errorf("delay = %v %v, want 21ms", d, ok)
	}
	if _, ok := c.since(captured, captured.Add(time.Minute)); ok {
		t.Error("a minute's delay was believed")
	}
}

func TestHostClockForgetsOldKeepalives(t *testing.T) {
	var c hostClock
	base := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	c.keepalive(base, base) // before the host's clock was stepped 1 s back
	c.roundTrip(0)
	for i := range clockWindow {
		sent := base.Add(time.Duration(i) * time.Second)
		c.keepalive(sent, sent.Add(time.Second))
	}
	if off, _ := c.offset(); off != time.Second {
		t.Errorf("offset = %v, want 1s once the old keepalive left the window", off)
	}
}

func TestDelayMeterAveragesAPeriod(t *testing.T) {
	var m delayMeter
	if _, ok := m.take(); ok {
		t.Error("an average without frames")
	}
	m.add(10 * time.Millisecond)
	m.add(20 * time.Millisecond)
	if d, ok := m.take(); !ok || d != 15*time.Millisecond {
		t.Errorf("average = %v %v", d, ok)
	}
	if _, ok := m.take(); ok {
		t.Error("take did not start a new period")
	}
}
