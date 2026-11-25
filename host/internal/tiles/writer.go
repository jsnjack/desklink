package tiles

import (
	"context"
	"log/slog"
	"sync"

	dlnet "desklink/internal/net"
)

const (
	// maxQueuedMessages and maxQueuedBytes bound the messages waiting for a
	// connection that stopped draining: about four seconds of tile_frame
	// messages at 120 frames a second.
	maxQueuedMessages = 512
	maxQueuedBytes    = 4 << 20
)

// Writer sends tile messages to the client in the order they were queued,
// on its own goroutine, so neither GStreamer's threads nor the pacer ever
// wait for the network.
//
// When the queue overflows, its messages are dropped. A client that missed
// a tile_frame could keep a tile the dropped message invalidated, and the
// host would count dropped tiles as sent, so after an overflow the writer
// drops everything until a tile_frame that invalidates every earlier tile,
// which it asks the tracker for. When invalidations go over UDP instead,
// AllInvalidated ends the dropping.
type Writer struct {
	send    func(context.Context, []byte) error
	onReset func()

	mu       sync.Mutex
	queue    [][]byte
	bytes    int
	sending  int
	dropping bool
	wake     chan struct{}
	dropped  uint64
}

// NewWriter returns a writer that sends with send and calls onReset, which
// must not block, when it needs the next tile_frame to invalidate every
// earlier tile.
func NewWriter(send func(context.Context, []byte) error, onReset func()) *Writer {
	return &Writer{send: send, onReset: onReset, wake: make(chan struct{}, 1)}
}

// Send queues msg; it never blocks.
func (w *Writer) Send(msg []byte) {
	w.mu.Lock()
	if w.dropping {
		if !dlnet.TileFrameAll(msg) {
			w.dropped++
			w.mu.Unlock()
			return
		}
		w.dropping = false
	}
	if len(w.queue) >= maxQueuedMessages || w.bytes+len(msg) > maxQueuedBytes {
		w.dropped += uint64(len(w.queue)) + 1
		w.queue, w.bytes, w.dropping = nil, 0, true
		w.mu.Unlock()
		slog.Debug("tile messages overflowed the queue; starting the tiles over")
		w.onReset()
		return
	}
	w.queue = append(w.queue, msg)
	w.bytes += len(msg)
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// AllInvalidated tells the writer that a frame judged after everything
// queued so far invalidated every earlier tile (sent over UDP), so tiles
// queued from now on are worth sending again after an overflow.
func (w *Writer) AllInvalidated() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.dropping = false
}

// Backlog is the number of bytes queued or being written.
func (w *Writer) Backlog() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.bytes + w.sending
}

// Dropped is the number of messages dropped by overflows.
func (w *Writer) Dropped() uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dropped
}

// Run sends queued messages until ctx ends or sending fails.
func (w *Writer) Run(ctx context.Context) error {
	for {
		w.mu.Lock()
		var msg []byte
		if len(w.queue) > 0 {
			msg = w.queue[0]
			w.queue = w.queue[1:]
			w.bytes -= len(msg)
			w.sending = len(msg)
		}
		w.mu.Unlock()
		if msg == nil {
			select {
			case <-ctx.Done():
				return nil
			case <-w.wake:
			}
			continue
		}
		err := w.send(ctx, msg)
		w.mu.Lock()
		w.sending = 0
		w.mu.Unlock()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
}
