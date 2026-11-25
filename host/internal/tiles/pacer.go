package tiles

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"

	dlnet "desklink/internal/net"
)

const (
	// BudgetBytesPerSecond paces tile messages at 16 Mbit/s. A full screen
	// of settled text compresses to 300–600 KB, so it arrives within about
	// a quarter of a second without crowding out the video.
	BudgetBytesPerSecond = 2_000_000
	// budgetBurst is how far the pacer may run ahead of the budget.
	budgetBurst = 64 << 10
	// maxBacklog bounds the bytes waiting for the WebSocket. Tile messages
	// queued behind a slow link would delay the tile_frame messages after
	// them, and with them the client's tiles.
	maxBacklog = 128 << 10
	// backlogPoll is how often a pacer blocked by the backlog looks again.
	backlogPoll = 5 * time.Millisecond
	// batchTiles and batchBytes bound one tile_data message; tiles
	// compressed together share one zlib stream.
	batchTiles = 16
	batchBytes = 48 << 10
	// photoBytesPerPixel marks a tile not worth sending: QOI needs more than
	// this for photographs and gradients, which the video already shows well,
	// while text on a flat background takes about 0.2.
	photoBytesPerPixel = 1.5
)

// Backlog reports the bytes queued for the client but not yet written.
type Backlog interface {
	Backlog() int
}

// Pacer sends captured tiles within a byte budget: the most recently
// settled first and, among those, the smallest first, so the part of the
// screen the user just worked on turns exact first. A tile whose part of
// the screen changed before it was sent is dropped.
type Pacer struct {
	tracker *Tracker
	backlog Backlog
	rate    float64
	burst   float64
	now     func() time.Time
	sleep   func(context.Context, time.Duration) bool

	mu    sync.Mutex
	queue []*item
	wake  chan struct{}

	tokens float64
	filled time.Time
	sent   PacerStats
}

type item struct {
	c       Capture
	qoi     []byte
	encoded bool
}

// PacerStats are counters for the session log.
type PacerStats struct {
	Messages, Tiles, Bytes, Stale, Photos uint64
}

// NewPacer returns a pacer that sends tracker's captures while backlog
// stays short.
func NewPacer(tracker *Tracker, backlog Backlog) *Pacer {
	return &Pacer{
		tracker: tracker, backlog: backlog,
		rate: BudgetBytesPerSecond, burst: budgetBurst,
		now: time.Now, sleep: sleepCtx,
		wake: make(chan struct{}, 1),
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// Add queues a capture; it never blocks, so the tracker may call it.
func (p *Pacer) Add(c Capture) {
	p.mu.Lock()
	p.queue = append(p.queue, &item{c: c})
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Clear drops every queued capture.
func (p *Pacer) Clear() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.queue = nil
}

// Stats returns the counters.
func (p *Pacer) Stats() PacerStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sent
}

// Run sends queued tiles until ctx ends.
func (p *Pacer) Run(ctx context.Context) {
	p.filled = p.now()
	p.tokens = p.burst
	for {
		if !p.waitForWork(ctx) || !p.waitForBudget(ctx) {
			return
		}
		p.sendOne()
	}
}

func (p *Pacer) waitForWork(ctx context.Context) bool {
	for {
		p.mu.Lock()
		n := len(p.queue)
		p.mu.Unlock()
		if n > 0 {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-p.wake:
		}
	}
}

// waitForBudget waits until the budget is not in debt and the backlog is
// short.
func (p *Pacer) waitForBudget(ctx context.Context) bool {
	for {
		now := p.now()
		p.tokens = min(p.burst, p.tokens+now.Sub(p.filled).Seconds()*p.rate)
		p.filled = now
		var wait time.Duration
		switch {
		case p.tokens < 0:
			wait = time.Duration(-p.tokens / p.rate * float64(time.Second))
		case p.backlog.Backlog() > maxBacklog:
			wait = backlogPoll
		default:
			return true
		}
		if !p.sleep(ctx, wait) {
			return false
		}
	}
}

// sendOne sends one message of the newest settled tiles, if any is still
// valid.
func (p *Pacer) sendOne() {
	batch := p.nextBatch()
	if len(batch) == 0 {
		return
	}
	grid := batch[0].c.Grid
	tiles := make([]dlnet.EncodedTile, len(batch))
	refs := make([]Ref, len(batch))
	for i, it := range batch {
		tiles[i] = dlnet.EncodedTile{Seq: it.c.Seq, Slot: uint16(it.c.Slot), QOI: it.qoi}
		refs[i] = Ref{Slot: it.c.Slot, Gen: it.c.Gen}
	}
	msg, err := dlnet.MarshalTileData(grid, tiles)
	if err != nil {
		slog.Debug("encode tiles", "err", err)
		return
	}
	if !p.tracker.SendIfValid(grid, refs, msg) {
		p.count(func(s *PacerStats) { s.Stale += uint64(len(batch)) })
		return
	}
	p.tokens -= float64(len(msg))
	p.count(func(s *PacerStats) {
		s.Messages++
		s.Tiles += uint64(len(batch))
		s.Bytes += uint64(len(msg))
	})
}

func (p *Pacer) count(f func(*PacerStats)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f(&p.sent)
}

// nextBatch takes the captures of the most recently settled frame from the
// queue, drops those that went stale or are not worth sending, and returns
// the smallest of them that fit one message; the rest go back to the queue.
// It never holds the pacer's lock while it asks the tracker, which calls Add
// with its own lock held.
func (p *Pacer) nextBatch() []*item {
	p.mu.Lock()
	newest := -1
	for i, it := range p.queue {
		if newest < 0 || it.c.At.After(p.queue[newest].c.At) || (it.c.At.Equal(p.queue[newest].c.At) && it.c.Seq > p.queue[newest].c.Seq) {
			newest = i
		}
	}
	if newest < 0 {
		p.mu.Unlock()
		return nil
	}
	at, seq := p.queue[newest].c.At, p.queue[newest].c.Seq
	var group []*item
	rest := p.queue[:0]
	for _, it := range p.queue {
		if it.c.At.Equal(at) && it.c.Seq == seq {
			group = append(group, it)
		} else {
			rest = append(rest, it)
		}
	}
	p.queue = rest
	p.mu.Unlock()

	var stale, photos uint64
	live := group[:0]
	for _, it := range group {
		if !p.tracker.Valid(it.c.Grid, it.c.Slot, it.c.Gen) {
			stale++
			continue
		}
		if !it.encoded {
			it.qoi = dlnet.AppendQOI(nil, it.c.Pix, it.c.W*4, it.c.W, it.c.H)
			it.encoded = true
			it.c.Pix = nil
		}
		if float64(len(it.qoi)) > photoBytesPerPixel*float64(it.c.W*it.c.H) {
			p.tracker.Ignore(it.c.Grid, it.c.Slot, it.c.Gen)
			photos++
			continue
		}
		live = append(live, it)
	}
	slices.SortStableFunc(live, func(a, b *item) int { return len(a.qoi) - len(b.qoi) })
	n, size := 0, 0
	for n < len(live) && n < batchTiles && (n == 0 || size+len(live[n].qoi) <= batchBytes) {
		size += len(live[n].qoi)
		n++
	}
	p.mu.Lock()
	p.queue = append(p.queue, live[n:]...)
	p.sent.Stale += stale
	p.sent.Photos += photos
	p.mu.Unlock()
	return live[:n]
}
