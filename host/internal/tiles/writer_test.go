package tiles

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	dlnet "desklink/internal/net"
)

// collector is a send function that keeps the messages it gets.
type collector struct {
	mu   sync.Mutex
	msgs [][]byte
	got  chan struct{}
}

func newCollector() *collector { return &collector{got: make(chan struct{}, 1024)} }

func (c *collector) send(_ context.Context, msg []byte) error {
	c.mu.Lock()
	c.msgs = append(c.msgs, msg)
	c.mu.Unlock()
	c.got <- struct{}{}
	return nil
}

func (c *collector) wait(t *testing.T, n int) [][]byte {
	t.Helper()
	for range n {
		select {
		case <-c.got:
		case <-time.After(time.Second):
			t.Fatalf("fewer than %d messages were sent", n)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.msgs
}

func frameMsg(seq uint32, all bool) []byte {
	return dlnet.TileFrame{Grid: testGrid, Seq: seq, All: all}.Marshal()
}

func TestWriterSendsInTheOrderQueued(t *testing.T) {
	c := newCollector()
	w := NewWriter(c.send, func() {})
	want := [][]byte{frameMsg(1, true), {dlnet.TileKindData, 1}, frameMsg(2, false)}
	for _, m := range want {
		w.Send(m)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		if err := w.Run(ctx); err != nil {
			t.Error(err)
		}
	}()
	got := c.wait(t, len(want))
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Errorf("message %d is %x, want %x", i, got[i], want[i])
		}
	}
}

func TestWriterOverflowDropsEverythingUntilAFrameInvalidatesAllTiles(t *testing.T) {
	resets := 0
	c := newCollector()
	w := NewWriter(c.send, func() { resets++ })
	for i := range maxQueuedMessages + 1 {
		w.Send(frameMsg(uint32(i+1), false))
	}
	if resets != 1 || w.Backlog() != 0 {
		t.Fatalf("after an overflow: %d resets, %d bytes queued", resets, w.Backlog())
	}
	w.Send(frameMsg(1000, false))
	w.Send([]byte{dlnet.TileKindData, 1})
	w.Send(frameMsg(1001, true))
	w.Send(frameMsg(1002, false))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		if err := w.Run(ctx); err != nil {
			t.Error(err)
		}
	}()
	got := c.wait(t, 2)
	if !bytes.Equal(got[0], frameMsg(1001, true)) || !bytes.Equal(got[1], frameMsg(1002, false)) {
		t.Errorf("after the overflow the client got %x", got)
	}
	if w.Dropped() != maxQueuedMessages+3 {
		t.Errorf("%d messages counted as dropped", w.Dropped())
	}
}

func TestWriterOverflowEndsWhenAnInvalidationOverUDPInvalidatedAllTiles(t *testing.T) {
	c := newCollector()
	w := NewWriter(c.send, func() {})
	data := func(b byte) []byte { return []byte{dlnet.TileKindData, b} }
	for range maxQueuedMessages + 1 {
		w.Send(data(0))
	}
	w.Send(data(1))
	w.AllInvalidated()
	w.Send(data(2))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		if err := w.Run(ctx); err != nil {
			t.Error(err)
		}
	}()
	if got := c.wait(t, 1); !bytes.Equal(got[0], data(2)) {
		t.Errorf("after the overflow the client got %x", got)
	}
}

func TestWriterCountsTheBytesNotYetWritten(t *testing.T) {
	w := NewWriter(func(context.Context, []byte) error { return nil }, func() {})
	w.Send(make([]byte, 100))
	w.Send(make([]byte, 50))
	if w.Backlog() != 150 {
		t.Errorf("backlog %d, want 150", w.Backlog())
	}
}

func TestWriterStopsWhenSendingFails(t *testing.T) {
	broken := errors.New("broken pipe")
	w := NewWriter(func(context.Context, []byte) error { return broken }, func() {})
	w.Send(frameMsg(1, true))
	if err := w.Run(context.Background()); !errors.Is(err, broken) {
		t.Errorf("Run returned %v, want the send error", err)
	}
}
