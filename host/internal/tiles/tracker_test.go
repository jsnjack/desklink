package tiles

import (
	"slices"
	"testing"
	"time"

	dlnet "desklink/internal/net"
)

// recorder is a tracker Output that keeps what it gets.
type recorder struct {
	frames   []dlnet.TileFrame
	data     [][]byte
	captures []Capture
	// pacer, when set, also gets the captures.
	pacer *Pacer
}

func (r *recorder) Judged(f dlnet.TileFrame, _ any) { r.frames = append(r.frames, f) }

func (r *recorder) Send(msg []byte) { r.data = append(r.data, msg) }

func (r *recorder) Captured(c Capture) {
	r.captures = append(r.captures, c)
	if r.pacer != nil {
		r.pacer.Add(c)
	}
}

func (r *recorder) last() dlnet.TileFrame { return r.frames[len(r.frames)-1] }

// canvas is a fake captured frame: BGRx rows with a few bytes of padding at
// the end of each, as GStreamer may give.
type canvas struct {
	w, h, stride int
	pix          []byte
}

func newCanvas(w, h int) *canvas {
	c := &canvas{w: w, h: h, stride: w*4 + 16}
	c.pix = make([]byte, c.stride*h)
	c.fill(0, 0, w, h, 36, 31, 49)
	return c
}

func (c *canvas) fill(x0, y0, w, h int, r, g, b byte) {
	for y := y0; y < y0+h; y++ {
		for x := x0; x < x0+w; x++ {
			i := y*c.stride + x*4
			c.pix[i], c.pix[i+1], c.pix[i+2], c.pix[i+3] = b, g, r, 0xff
		}
	}
}

func (c *canvas) frame(pts time.Duration) Frame {
	return Frame{PTS: pts, Pix: c.pix, Stride: c.stride}
}

// clock hands out scan times and timestamps a frame interval apart.
type clock struct {
	start time.Time
	pts   time.Duration
}

func newClock() *clock { return &clock{start: time.Unix(1_700_000_000, 0), pts: time.Second} }

func (k *clock) now() time.Time { return k.start.Add(k.pts) }

// step scans c as the next frame, d after the last one, and has it encoded.
func step(tr *Tracker, k *clock, c *canvas, d time.Duration) time.Duration {
	k.pts += d
	tr.Scan(c.frame(k.pts), k.now())
	tr.Encoded(k.pts, uint64(k.pts/time.Microsecond), nil)
	return k.pts
}

var testGrid = dlnet.TileGrid{Width: 256, Height: 128}

// settled returns a tracker whose eight tiles of c settled and were
// captured.
func settled(t *testing.T, c *canvas) (*Tracker, *recorder, *clock) {
	t.Helper()
	r := &recorder{}
	tr := NewTracker(testGrid, r)
	k := newClock()
	step(tr, k, c, 0)
	step(tr, k, c, SettleDelay)
	if len(r.captures) != testGrid.Count() {
		t.Fatalf("%d of %d tiles captured after the settle delay", len(r.captures), testGrid.Count())
	}
	return tr, r, k
}

func TestTilesSettleAfterTheDelay(t *testing.T) {
	r := &recorder{}
	tr := NewTracker(testGrid, r)
	k := newClock()
	c := newCanvas(256, 128)
	step(tr, k, c, 0)
	step(tr, k, c, 100*time.Millisecond)
	step(tr, k, c, 99*time.Millisecond)
	if len(r.captures) != 0 {
		t.Fatalf("%d tiles captured %v after they last changed", len(r.captures), 199*time.Millisecond)
	}
	step(tr, k, c, time.Millisecond)
	if len(r.captures) != testGrid.Count() {
		t.Fatalf("%d tiles captured once settled, want %d", len(r.captures), testGrid.Count())
	}
	for _, cp := range r.captures {
		if cp.Seq != 4 || cp.W != 64 || cp.H != 64 || len(cp.Pix) != 64*64*4 {
			t.Errorf("capture of tile %d: frame %d, %dx%d, %d bytes", cp.Slot, cp.Seq, cp.W, cp.H, len(cp.Pix))
		}
		if !tr.Valid(testGrid, cp.Slot, cp.Gen) {
			t.Errorf("tile %d is not valid right after its capture", cp.Slot)
		}
	}
	if b, g, red := r.captures[0].Pix[0], r.captures[0].Pix[1], r.captures[0].Pix[2]; red != 36 || g != 31 || b != 49 {
		t.Errorf("captured pixel is %d,%d,%d, want 36,31,49", red, g, b)
	}
}

func TestATileThatKeepsChangingNeverSettles(t *testing.T) {
	r := &recorder{}
	tr := NewTracker(testGrid, r)
	k := newClock()
	c := newCanvas(256, 128)
	for i := range 20 {
		c.fill(70, 10, 4, 4, byte(i), 0, 0) // inside tile 1
		step(tr, k, c, 50*time.Millisecond)
	}
	for _, cp := range r.captures {
		if cp.Slot == 1 {
			t.Fatal("a tile that changed every 50 ms was captured")
		}
	}
	if len(r.captures) != testGrid.Count()-1 {
		t.Errorf("%d tiles captured, want every one but the changing one", len(r.captures))
	}
}

func TestEveryEncodedFrameGetsOneTileFrameInOrder(t *testing.T) {
	c := newCanvas(256, 128)
	tr, r, k := settled(t, c)
	pts := step(tr, k, c, 16*time.Millisecond)
	if len(r.frames) != 3 {
		t.Fatalf("%d tile_frame messages for 3 frames", len(r.frames))
	}
	for i, f := range r.frames {
		if f.Seq != uint32(i+1) || f.Grid != testGrid {
			t.Errorf("message %d is for frame %d of a %+v grid", i, f.Seq, f.Grid)
		}
	}
	if !r.frames[0].All {
		t.Error("the first tile_frame does not invalidate the tiles the client may hold")
	}
	if f := r.last(); f.All || len(f.Changed) != 0 || f.CaptureMicros != uint64(pts/time.Microsecond) {
		t.Errorf("an unchanged frame's message is %+v", f)
	}
}

func TestAFrameThatChangesATrackedTileReportsIt(t *testing.T) {
	c := newCanvas(256, 128)
	tr, r, k := settled(t, c)
	c.fill(64*2+5, 64+7, 1, 1, 255, 255, 255) // one pixel in tile 6
	step(tr, k, c, 16*time.Millisecond)
	if f := r.last(); f.All || !slices.Equal(f.Changed, []uint16{6}) {
		t.Fatalf("the frame that changed tile 6 reports %+v", f)
	}
	for _, cp := range r.captures {
		if got := tr.Valid(testGrid, cp.Slot, cp.Gen); got != (cp.Slot != 6) {
			t.Errorf("tile %d valid = %v after tile 6 changed", cp.Slot, got)
		}
	}
	step(tr, k, c, 16*time.Millisecond)
	if f := r.last(); len(f.Changed) != 0 {
		t.Errorf("a tile reported once is reported again: %v", f.Changed)
	}
}

func TestTheFourthByteIsNotAChange(t *testing.T) {
	c := newCanvas(256, 128)
	tr, r, k := settled(t, c)
	for i := 3; i < len(c.pix); i += 4 {
		c.pix[i] = byte(i)
	}
	step(tr, k, c, 16*time.Millisecond)
	if f := r.last(); f.All || len(f.Changed) != 0 {
		t.Errorf("changing only the padding bytes reported %+v", f)
	}
}

func TestAFrameWithoutAScanInvalidatesEveryTile(t *testing.T) {
	c := newCanvas(256, 128)
	tr, r, k := settled(t, c)
	k.pts += 16 * time.Millisecond
	tr.Encoded(k.pts, 1, nil) // its scan was skipped
	if len(r.frames) != 2 {
		t.Fatal("the frame was judged before a later scan showed that its own was skipped")
	}
	step(tr, k, c, 16*time.Millisecond)
	if !r.frames[2].All {
		t.Errorf("the frame without a scan reports %+v, want every tile invalid", r.frames[2])
	}
	for _, cp := range r.captures {
		if tr.Valid(testGrid, cp.Slot, cp.Gen) {
			t.Errorf("tile %d is still valid after a frame nobody checked", cp.Slot)
		}
	}
}

func TestTooManyFramesWaitingForScansAreJudgedWithout(t *testing.T) {
	c := newCanvas(256, 128)
	tr, r, k := settled(t, c)
	for range maxPendingFrames + 1 {
		k.pts += time.Millisecond
		tr.Encoded(k.pts, uint64(k.pts/time.Microsecond), nil)
	}
	// The oldest goes without one, invalidating every tile; with none
	// left, the next need not wait either.
	if len(r.frames) != 4 || !r.frames[2].All || r.frames[3].All {
		t.Errorf("after %d frames without scans: %+v", maxPendingFrames+1, r.frames[2:])
	}
}

func TestAFrameIsJudgedAtOnceWhileTheClientHasNoTile(t *testing.T) {
	r := &recorder{}
	tr := NewTracker(testGrid, r)
	k := newClock()
	c := newCanvas(256, 128)
	tr.Encoded(k.pts, 1, nil)
	if len(r.frames) != 1 || !r.frames[0].All {
		t.Fatalf("the first frame, before its scan: %+v", r.frames)
	}
	tr.Scan(c.frame(k.pts), k.now())
	k.pts += SettleDelay
	tr.Encoded(k.pts, 2, nil)
	if len(r.frames) != 2 || r.frames[1].All || len(r.frames[1].Changed) != 0 {
		t.Fatalf("the second frame, before its scan: %+v", r.frames)
	}
	if len(r.captures) != 0 {
		t.Fatal("tiles were captured before the frame's scan arrived")
	}
	tr.Scan(c.frame(k.pts), k.now())
	if len(r.frames) != 2 || len(r.captures) != testGrid.Count() || r.captures[0].Seq != 2 {
		t.Fatalf("the scan of a frame judged early: %d invalidations, %d captures", len(r.frames), len(r.captures))
	}
}

func TestAFrameWaitsForItsScanWhileTheClientMayHaveTiles(t *testing.T) {
	c := newCanvas(256, 128)
	tr, r, k := settled(t, c)
	k.pts += 16 * time.Millisecond
	tr.Encoded(k.pts, 3, nil)
	if len(r.frames) != 2 {
		t.Fatal("a frame was judged before the scan that shows what it changed")
	}
	c.fill(70, 10, 4, 4, 255, 0, 0) // inside tile 1
	tr.Scan(c.frame(k.pts), k.now())
	if len(r.frames) != 3 || !slices.Equal(r.last().Changed, []uint16{1}) {
		t.Fatalf("the frame after its scan: %+v", r.frames)
	}
}

func TestScansMayArriveAfterTheirFrame(t *testing.T) {
	r := &recorder{}
	tr := NewTracker(testGrid, r)
	k := newClock()
	c := newCanvas(256, 128)
	for range 3 {
		k.pts += 100 * time.Millisecond
		tr.Encoded(k.pts, uint64(k.pts/time.Microsecond), nil)
		tr.Scan(c.frame(k.pts), k.now())
	}
	if len(r.frames) != 3 || r.frames[1].All || r.frames[2].All {
		t.Fatalf("frames whose scans came late: %+v", r.frames)
	}
	if len(r.captures) != testGrid.Count() || r.captures[0].Seq != 3 {
		t.Errorf("%d captures, the first from frame %d; want %d from frame 3", len(r.captures), r.captures[0].Seq, testGrid.Count())
	}
}

func TestATileCapturedFromAFrameTheEncoderSkippedIsCapturedAgain(t *testing.T) {
	r := &recorder{}
	tr := NewTracker(testGrid, r)
	k := newClock()
	c := newCanvas(256, 128)
	step(tr, k, c, 0)
	k.pts += SettleDelay
	tr.Scan(c.frame(k.pts), k.now()) // settles, but the encoder drops this frame
	step(tr, k, c, 16*time.Millisecond)
	if len(r.captures) != 0 {
		t.Fatalf("%d tiles captured from a frame that was never encoded", len(r.captures))
	}
	step(tr, k, c, 16*time.Millisecond)
	if len(r.captures) != testGrid.Count() || r.captures[0].Seq != 3 {
		t.Errorf("%d captures from frame %d; want every tile from frame 3", len(r.captures), r.captures[0].Seq)
	}
}

func TestAChangeBeforeTheCapturedFrameIsEncodedIsReported(t *testing.T) {
	r := &recorder{}
	tr := NewTracker(testGrid, r)
	k := newClock()
	c := newCanvas(256, 128)
	step(tr, k, c, 0)
	k.pts += SettleDelay
	captured := k.pts
	tr.Scan(c.frame(captured), k.now())
	c.fill(0, 0, 1, 1, 1, 2, 3) // tile 0 changes in the next frame
	k.pts += 16 * time.Millisecond
	tr.Scan(c.frame(k.pts), k.now())
	tr.Encoded(captured, 1, nil)
	tr.Encoded(k.pts, 2, nil)
	if len(r.captures) != testGrid.Count() {
		t.Fatalf("%d tiles captured", len(r.captures))
	}
	if f := r.last(); !slices.Equal(f.Changed, []uint16{0}) {
		t.Errorf("the frame after the capture reports %v, want tile 0", f.Changed)
	}
}

func TestRestartInvalidatesEveryTile(t *testing.T) {
	c := newCanvas(256, 128)
	tr, r, k := settled(t, c)
	k.pts += 16 * time.Millisecond
	tr.Encoded(k.pts, 1, nil) // waiting for its scan
	small := dlnet.TileGrid{Width: 128, Height: 64}
	tr.Restart(small)
	if f := r.last(); !f.All || f.Grid != testGrid {
		t.Errorf("a frame of the old pipeline reports %+v, want every tile invalid", f)
	}
	for _, cp := range r.captures {
		if tr.Valid(cp.Grid, cp.Slot, cp.Gen) {
			t.Errorf("tile %d of the old grid is still valid", cp.Slot)
		}
	}
	sc := newCanvas(128, 64)
	step(tr, k, sc, 16*time.Millisecond)
	if f := r.last(); !f.All || f.Grid != small {
		t.Errorf("the new pipeline's first frame reports %+v, want every tile invalid", f)
	}
}

func TestTimestampsGoingBackwardsStartOver(t *testing.T) {
	c := newCanvas(256, 128)
	tr, r, k := settled(t, c)
	k.pts = time.Millisecond
	step(tr, k, c, 0)
	if f := r.last(); !f.All {
		t.Errorf("the first frame after the timestamps went back reports %+v", f)
	}
	step(tr, k, c, 100*time.Millisecond)
	n := len(r.captures)
	step(tr, k, c, SettleDelay)
	if len(r.captures) == n {
		t.Error("tiles did not settle again after the restart")
	}
}

func TestRequestResetInvalidatesEveryTileOnce(t *testing.T) {
	c := newCanvas(256, 128)
	tr, r, k := settled(t, c)
	tr.RequestReset()
	step(tr, k, c, 16*time.Millisecond)
	step(tr, k, c, 16*time.Millisecond)
	if n := len(r.frames); !r.frames[n-2].All || r.frames[n-1].All {
		t.Errorf("after a reset request: %+v then %+v", r.frames[n-2], r.frames[n-1])
	}
}

func TestAnIgnoredTileWaitsForItsNextChange(t *testing.T) {
	c := newCanvas(256, 128)
	tr, r, k := settled(t, c)
	cp := r.captures[3]
	tr.Ignore(testGrid, cp.Slot, cp.Gen)
	if tr.Valid(testGrid, cp.Slot, cp.Gen) {
		t.Fatal("an ignored tile is still valid")
	}
	r.captures = nil
	step(tr, k, c, SettleDelay)
	if len(r.captures) != 0 {
		t.Fatalf("an ignored tile was captured again without a change")
	}
	c.fill(3*64, 0, 64, 64, 9, 9, 9)
	step(tr, k, c, 16*time.Millisecond)
	if f := r.last(); len(f.Changed) != 0 {
		t.Errorf("a change to a tile the client never got was reported: %v", f.Changed)
	}
	step(tr, k, c, SettleDelay)
	if len(r.captures) != 1 || r.captures[0].Slot != 3 {
		t.Errorf("after it changed and settled, captures are %+v", r.captures)
	}
}

func TestSendIfValidSendsOnlyWhileATileIsValid(t *testing.T) {
	c := newCanvas(256, 128)
	tr, r, k := settled(t, c)
	refs := []Ref{{Slot: r.captures[0].Slot, Gen: r.captures[0].Gen}, {Slot: r.captures[1].Slot, Gen: r.captures[1].Gen}}
	c.fill(0, 0, 1, 1, 1, 1, 1) // tile 0 changes
	step(tr, k, c, 16*time.Millisecond)
	if !tr.SendIfValid(testGrid, refs, []byte{dlnet.TileKindData}) || len(r.data) != 1 {
		t.Error("a message with one valid tile was not sent")
	}
	c.fill(64, 0, 1, 1, 1, 1, 1) // tile 1 changes
	step(tr, k, c, 16*time.Millisecond)
	if tr.SendIfValid(testGrid, refs, []byte{dlnet.TileKindData}) || len(r.data) != 1 {
		t.Error("a message whose tiles all went stale was sent")
	}
}

func TestTilesAtTheStreamsEdgeAreCut(t *testing.T) {
	g := dlnet.TileGrid{Width: 200, Height: 100}
	r := &recorder{}
	tr := NewTracker(g, r)
	k := newClock()
	c := newCanvas(200, 100)
	step(tr, k, c, 0)
	step(tr, k, c, SettleDelay)
	if len(r.captures) != g.Count() {
		t.Fatalf("%d of %d tiles captured", len(r.captures), g.Count())
	}
	last := r.captures[len(r.captures)-1]
	if last.W != 200-192 || last.H != 100-64 || len(last.Pix) != last.W*last.H*4 {
		t.Errorf("the corner tile is %dx%d with %d bytes", last.W, last.H, len(last.Pix))
	}
	c.fill(199, 99, 1, 1, 0, 0, 0)
	step(tr, k, c, 16*time.Millisecond)
	if f := r.last(); !slices.Equal(f.Changed, []uint16{uint16(g.Count() - 1)}) {
		t.Errorf("a change in the corner reports %v", f.Changed)
	}
}

func TestAScanOfAnotherSizeIsIgnored(t *testing.T) {
	tr, r, k := settled(t, newCanvas(256, 128))
	small := newCanvas(128, 64)
	k.pts += 16 * time.Millisecond
	tr.Scan(small.frame(k.pts), k.now())
	tr.Encoded(k.pts, 3, nil)
	if len(r.frames) != 2 {
		t.Errorf("a frame was judged by a scan of another size: %+v", r.frames[2:])
	}
}

// frameGap is the frame interval of the settle-delay tests: a still
// screen's keepalive frames come 100 ms apart, so a tile's changes are seen
// at least that often.
const frameGap = 50 * time.Millisecond

// watch runs frames frameGap apart for d, changing tile 1 at each frame
// change asks for (given the time since the start), and returns the times
// at which tile 1 was captured, and whether a tile_frame listed it at or
// after quietFrom.
func watch(tr *Tracker, r *recorder, k *clock, c *canvas, d time.Duration, change func(time.Duration) bool, quietFrom time.Duration) (captured []time.Duration, listedLate bool) {
	for at := frameGap; at <= d; at += frameGap {
		if change(at) {
			i := 10*c.stride + 70*4 // a pixel inside tile 1
			c.pix[i] ^= 0xff
		}
		n := len(r.captures)
		step(tr, k, c, frameGap)
		for _, cp := range r.captures[n:] {
			if cp.Slot == 1 {
				captured = append(captured, at)
			}
		}
		if at >= quietFrom && slices.Contains(r.last().Changed, 1) {
			listedLate = true
		}
	}
	return captured, listedLate
}

// every changes the tile once each period.
func every(period time.Duration) func(time.Duration) bool {
	return func(at time.Duration) bool { return at%period == 0 }
}

// once changes the tile at the first frame.
func once(at time.Duration) bool { return at == frameGap }

func never(time.Duration) bool { return false }

func TestATileThatKeepsChangingSoonAfterItWasSentStopsBeingSent(t *testing.T) {
	cases := []struct {
		name   string
		period time.Duration
		// quiet is when the tile must have stopped being sent.
		quiet time.Duration
	}{
		{"a caret blinking every 600 ms", 600 * time.Millisecond, 2 * time.Second},
		{"a clock's seconds", time.Second, 4 * time.Second},
		{"a status that changes every two seconds", 2 * time.Second, 12 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newCanvas(256, 128)
			tr, r, k := settled(t, c)
			captured, listed := watch(tr, r, k, c, 30*time.Second, every(tc.period), tc.quiet+tc.period)
			for _, at := range captured {
				if at >= tc.quiet {
					t.Errorf("tile captured again %v after the changes began (captures at %v)", at, captured)
				}
			}
			if listed {
				t.Error("tile_frames still list the tile: the client still switches it between exact and video")
			}
			if s := tr.Stats(); s.Slowed == 0 || s.Slowed > maxSlowdown {
				t.Errorf("the settle delay doubled %d times", s.Slowed)
			}
		})
	}
}

func TestATileThatChangesRarelyKeepsTheShortDelay(t *testing.T) {
	c := newCanvas(256, 128)
	tr, r, k := settled(t, c)
	period := 5 * time.Second
	captured, _ := watch(tr, r, k, c, 28*time.Second, every(period), time.Hour)
	if len(captured) != 5 {
		t.Fatalf("captured at %v, want once after each of 5 changes", captured)
	}
	for i, at := range captured {
		if since := at - time.Duration(i+1)*period; since != SettleDelay {
			t.Errorf("capture %d came %v after its change, want %v", i, since, SettleDelay)
		}
	}
	if s := tr.Stats(); s.Slowed != 0 {
		t.Errorf("a tile changing every %v was slowed %d times", period, s.Slowed)
	}
}

func TestASlowedTileIsSentOnceStillForItsLongerDelay(t *testing.T) {
	c := newCanvas(256, 128)
	tr, r, k := settled(t, c)
	// A caret blinks for 6 s, its last change at 6 s, then stays.
	watch(tr, r, k, c, 6*time.Second, every(600*time.Millisecond), time.Hour)
	captured, _ := watch(tr, r, k, c, 3*time.Second, never, time.Hour)
	if want := SettleDelay << 2; len(captured) != 1 || captured[0] != want {
		t.Errorf("once still, captured at %v; want once, %v after the last change", captured, want)
	}
}

func TestASlowedTileSettlesFasterAgainAfterBeingStill(t *testing.T) {
	cases := []struct {
		name  string
		still time.Duration
		want  time.Duration
	}{
		{"still for one calm period, half the delay", calmPeriod, SettleDelay << 1},
		{"still for two, the normal delay", 2 * calmPeriod, SettleDelay},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newCanvas(256, 128)
			tr, r, k := settled(t, c)
			watch(tr, r, k, c, 6*time.Second, every(600*time.Millisecond), time.Hour)
			watch(tr, r, k, c, tc.still, never, time.Hour)
			captured, _ := watch(tr, r, k, c, 2*time.Second, once, time.Hour)
			if len(captured) != 1 || captured[0]-frameGap != tc.want {
				t.Errorf("after one change, captured at %v; want once, %v after the change", captured, tc.want)
			}
		})
	}
}

func TestTheSettleDelayStopsGrowingAtTheMaximum(t *testing.T) {
	c := newCanvas(256, 128)
	tr, r, k := settled(t, c)
	seen := len(r.captures)
	// The tile changes at the start, 50 ms after settled captured it, and
	// at the frame after each capture.
	afterCapture := func(at time.Duration) bool {
		if at == frameGap || len(r.captures) > seen {
			seen = len(r.captures)
			return true
		}
		return false
	}
	captured, _ := watch(tr, r, k, c, 30*time.Second, afterCapture, time.Hour)
	if len(captured) < maxSlowdown+3 {
		t.Fatalf("captured at %v", captured)
	}
	for i, want := range []time.Duration{SettleDelay << 1, SettleDelay << 2, SettleDelay << 3, MaxSettleDelay, MaxSettleDelay, MaxSettleDelay} {
		changed := frameGap
		if i > 0 {
			changed = captured[i-1] + frameGap
		}
		if wait := captured[i] - changed; wait != want {
			t.Errorf("capture %d came %v after its change, want %v", i, wait, want)
		}
	}
	if s := tr.Stats(); s.Slowed != maxSlowdown {
		t.Errorf("the settle delay doubled %d times, want %d", s.Slowed, maxSlowdown)
	}
}

func TestInvalidatingEveryTileSlowsNone(t *testing.T) {
	c := newCanvas(256, 128)
	tr, r, k := settled(t, c)
	for i := range 5 {
		tr.RequestReset()
		step(tr, k, c, frameGap)
		n := len(r.captures)
		step(tr, k, c, frameGap)
		if len(r.captures)-n != testGrid.Count() {
			t.Fatalf("round %d: %d tiles captured again after a reset, want all %d at once", i, len(r.captures)-n, testGrid.Count())
		}
	}
	if s := tr.Stats(); s.Slowed != 0 {
		t.Errorf("resets slowed tiles %d times", s.Slowed)
	}
}
