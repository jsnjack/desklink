package tiles

import (
	"context"
	"math/rand/v2"
	"testing"
	"time"

	dlnet "desklink/internal/net"
)

// fixedBacklog is a writer backlog the test sets.
type fixedBacklog struct{ bytes int }

func (b *fixedBacklog) Backlog() int { return b.bytes }

// pacerFixture is a tracker whose captures go to a pacer with a fake clock.
type pacerFixture struct {
	tr      *Tracker
	r       *recorder
	p       *Pacer
	k       *clock
	backlog *fixedBacklog
	now     time.Time
	slept   []time.Duration
}

func newPacerFixture(grid dlnet.TileGrid) *pacerFixture {
	f := &pacerFixture{r: &recorder{}, k: newClock(), backlog: &fixedBacklog{}, now: time.Unix(1_700_000_000, 0)}
	f.tr = NewTracker(grid, f.r)
	f.p = NewPacer(f.tr, f.backlog)
	f.r.pacer = f.p
	f.p.now = func() time.Time { return f.now }
	f.p.sleep = func(_ context.Context, d time.Duration) bool {
		f.slept = append(f.slept, d)
		f.now = f.now.Add(d)
		if f.backlog.bytes > 0 {
			f.backlog.bytes = 0 // the writer drained meanwhile
		}
		return true
	}
	f.p.filled = f.now
	f.p.tokens = f.p.burst
	return f
}

// sent decodes the tile_data messages the tracker queued.
func (f *pacerFixture) sent(t *testing.T) [][]dlnet.DecodedTile {
	t.Helper()
	var out [][]dlnet.DecodedTile
	for _, msg := range f.r.data {
		_, tiles, err := dlnet.ParseTileData(msg)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, tiles)
	}
	return out
}

// noise fills a tile with random colours, which QOI cannot shrink.
func noise(c *canvas, x0, y0 int) {
	rng := rand.New(rand.NewPCG(uint64(x0), uint64(y0)))
	for y := y0; y < y0+64; y++ {
		for x := x0; x < x0+64; x++ {
			c.fill(x, y, 1, 1, byte(rng.Uint32()), byte(rng.Uint32()), byte(rng.Uint32()))
		}
	}
}

// speckle scatters eight colours over a tile: about a byte a pixel in QOI,
// which keeps it below the photograph limit, and hard to compress further.
func speckle(c *canvas, x0, y0 int) {
	rng := rand.New(rand.NewPCG(uint64(x0)+7, uint64(y0)))
	for y := y0; y < y0+64; y++ {
		for x := x0; x < x0+64; x++ {
			v := byte(rng.IntN(8))
			c.fill(x, y, 1, 1, v*30, v*20, 255-v*25)
		}
	}
}

// text draws a few strokes into a tile, which makes its QOI ops longer
// than a flat tile's.
func text(c *canvas, x0, y0 int, strokes int) {
	for i := range strokes {
		c.fill(x0+4+i*3, y0+8, 1, 40, 222, 222, 222)
		c.fill(x0+4, y0+10+i*2, 30, 1, 98, 160, 234)
	}
}

func TestPacerSendsTheMostRecentlySettledTilesFirst(t *testing.T) {
	f := newPacerFixture(testGrid)
	c := newCanvas(256, 128)
	c.fill(0, 64, 256, 64, 1, 2, 3) // the bottom row changes later
	step(f.tr, f.k, c, 0)
	c.fill(0, 64, 256, 64, 200, 2, 3)
	step(f.tr, f.k, c, 100*time.Millisecond)
	step(f.tr, f.k, c, 100*time.Millisecond) // top row settles
	step(f.tr, f.k, c, 100*time.Millisecond) // bottom row settles
	f.p.sendOne()
	f.p.sendOne()
	sent := f.sent(t)
	if len(sent) != 2 {
		t.Fatalf("%d messages", len(sent))
	}
	for _, tile := range sent[0] {
		if tile.Slot < 4 {
			t.Errorf("tile %d, which settled first, went out before the newer ones", tile.Slot)
		}
	}
	if sent[1][0].Slot >= 4 || sent[1][0].Seq != 3 {
		t.Errorf("the second message holds tile %d of frame %d", sent[1][0].Slot, sent[1][0].Seq)
	}
}

func TestPacerSendsTheSmallestTilesFirst(t *testing.T) {
	f := newPacerFixture(testGrid)
	c := newCanvas(256, 128)
	text(c, 64, 0, 8)
	text(c, 128, 0, 2)
	step(f.tr, f.k, c, 0)
	step(f.tr, f.k, c, SettleDelay)
	f.p.sendOne()
	sent := f.sent(t)
	if len(sent) != 1 || len(sent[0]) != testGrid.Count() {
		t.Fatalf("got %d messages", len(sent))
	}
	n := len(sent[0])
	if sent[0][n-1].Slot != 1 || sent[0][n-2].Slot != 2 {
		t.Errorf("the busiest tiles were not last: %d, %d", sent[0][n-2].Slot, sent[0][n-1].Slot)
	}
}

func TestPacerSendsAtMostSixteenTilesAMessage(t *testing.T) {
	g := dlnet.TileGrid{Width: 64 * 5, Height: 64 * 4}
	f := newPacerFixture(g)
	c := newCanvas(g.Width, g.Height)
	step(f.tr, f.k, c, 0)
	step(f.tr, f.k, c, SettleDelay)
	f.p.sendOne()
	f.p.sendOne()
	sent := f.sent(t)
	if len(sent) != 2 || len(sent[0]) != batchTiles || len(sent[1]) != g.Count()-batchTiles {
		t.Errorf("20 tiles went out as %d messages", len(sent))
	}
}

func TestPacerDropsTilesThatChangedBeforeTheyWentOut(t *testing.T) {
	f := newPacerFixture(testGrid)
	c := newCanvas(256, 128)
	step(f.tr, f.k, c, 0)
	step(f.tr, f.k, c, SettleDelay)
	c.fill(0, 0, 256, 128, 9, 9, 9)
	step(f.tr, f.k, c, 16*time.Millisecond)
	f.p.sendOne()
	if len(f.r.data) != 0 {
		t.Error("tiles that changed were sent")
	}
	if s := f.p.Stats(); s.Stale != uint64(testGrid.Count()) {
		t.Errorf("%d tiles counted as stale, want %d", s.Stale, testGrid.Count())
	}
}

func TestPacerSkipsPhotographs(t *testing.T) {
	f := newPacerFixture(testGrid)
	c := newCanvas(256, 128)
	noise(c, 64, 64)
	step(f.tr, f.k, c, 0)
	step(f.tr, f.k, c, SettleDelay)
	photo := f.r.captures[5]
	f.p.sendOne()
	sent := f.sent(t)
	if len(sent) != 1 || len(sent[0]) != testGrid.Count()-1 {
		t.Fatalf("sent %v", sent)
	}
	for _, tile := range sent[0] {
		if tile.Slot == 5 {
			t.Error("the noise tile was sent")
		}
	}
	if f.tr.Valid(testGrid, photo.Slot, photo.Gen) || f.p.Stats().Photos != 1 {
		t.Error("the noise tile is still tracked")
	}
}

func TestPacerKeepsToItsBudget(t *testing.T) {
	g := dlnet.TileGrid{Width: 64 * 16, Height: 64 * 8}
	f := newPacerFixture(g)
	c := newCanvas(g.Width, g.Height)
	for i := range g.Count() {
		x, y, _, _ := g.Rect(i)
		speckle(c, x, y)
	}
	step(f.tr, f.k, c, 0)
	step(f.tr, f.k, c, SettleDelay)
	ctx := context.Background()
	start := f.now
	for range g.Count() {
		if f.p.Stats().Tiles == uint64(g.Count()) {
			break
		}
		if !f.p.waitForBudget(ctx) {
			t.Fatal("budget wait ended")
		}
		f.p.sendOne()
	}
	bytes := f.p.Stats().Bytes
	if f.p.Stats().Tiles != uint64(g.Count()) {
		t.Fatalf("%d of %d tiles sent", f.p.Stats().Tiles, g.Count())
	}
	if bytes < 3*budgetBurst {
		t.Fatalf("only %d bytes sent; the test needs more than the burst", bytes)
	}
	// Everything beyond the burst waits for the budget, except the last
	// message, which is sent before the wait that pays for it.
	last := f.r.data[len(f.r.data)-1]
	want := time.Duration(float64(int(bytes)-budgetBurst-len(last)) / BudgetBytesPerSecond * float64(time.Second))
	elapsed := f.now.Sub(start)
	if elapsed < want || elapsed > want+want/10 {
		t.Errorf("%d bytes went out in %v; the budget allows them in %v", bytes, elapsed, want)
	}
}

func TestPacerWaitsWhileTheConnectionIsBehind(t *testing.T) {
	f := newPacerFixture(testGrid)
	f.backlog.bytes = maxBacklog + 1
	if !f.p.waitForBudget(context.Background()) {
		t.Fatal("budget wait ended")
	}
	if len(f.slept) != 1 || f.slept[0] != backlogPoll {
		t.Errorf("slept %v while the backlog was full", f.slept)
	}
}

func TestPacerStopsWithItsContext(t *testing.T) {
	f := newPacerFixture(testGrid)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		f.p.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after its context ended")
	}
}
