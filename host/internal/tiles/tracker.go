// Package tiles finds the parts of the screen that stopped changing, so
// that their exact pixels can go to the client beside the video, and tells
// the client, for every video frame, which of the tiles it was sent that
// frame changed (docs/desktop.md, "Exact tiles").
package tiles

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	dlnet "desklink/internal/net"
)

const (
	// SettleDelay is how long a tile must stay unchanged before its pixels
	// are sent, at first. Typing changes a tile a few times a second, so it
	// settles soon after the typing stops, while one that keeps changing (a
	// video, a progress bar, scrolling) never wastes bytes on pixels about
	// to change.
	SettleDelay = 200 * time.Millisecond
	// MaxSettleDelay bounds the settle delay of a tile that keeps changing
	// soon after it was sent (maxSlowdown doublings of SettleDelay).
	MaxSettleDelay = SettleDelay << maxSlowdown
	maxSlowdown    = 4
	// briefTile is how soon after its capture a change counts against a
	// tile. The client draws the video there again, and where the video's
	// colours differ from the exact pixels (dark backgrounds on an AMOLED
	// panel) the tile flickers each time: a caret blinking every 600 ms, a
	// clock's seconds. Each such change doubles the tile's settle delay, so
	// after a few it stays video while it keeps changing.
	briefTile = 2 * time.Second
	// calmPeriod halves a slowed tile's settle delay again for each period
	// it stayed unchanged.
	calmPeriod = 10 * time.Second
	// maxPendingFrames bounds the encoded frames that wait for their scan;
	// beyond it the oldest is judged without one.
	maxPendingFrames = 16
	// maxScans bounds the scans kept for frames still being encoded.
	maxScans = 8
)

// Frame is one captured frame at the stream's size, as BGRx rows stride
// bytes apart. Pix is valid only while Tracker.Scan runs.
type Frame struct {
	PTS    time.Duration
	Pix    []byte
	Stride int
}

// Capture is a settled tile's exact pixels, taken from the scan of the
// frame numbered Seq, as BGRx rows of W*4 bytes.
type Capture struct {
	Grid dlnet.TileGrid
	Slot int
	Seq  uint32
	// Gen is the slot's generation when it was captured; the capture is
	// still worth sending while Tracker.Valid says so.
	Gen uint32
	// At is when the scan that captured it ran, so the newest go first.
	At   time.Time
	W, H int
	Pix  []byte
}

// Output takes what the tracker produces. Its methods run with the
// tracker's lock held, so they must neither block nor call the tracker
// (RequestReset excepted).
type Output interface {
	// Judged takes the invalidation of an encoded frame, with the ref
	// Encoded got for it: which of the tiles the client may have that frame
	// changed. Frames are judged once each, in order.
	Judged(f dlnet.TileFrame, ref any)
	// Send queues a tile_data message for the client; messages must reach
	// the client in the order they are queued.
	Send(msg []byte)
	// Captured takes a tile to send.
	Captured(c Capture)
}

type slotState uint8

const (
	// idle: the client has no tile here and none is on its way.
	idle slotState = iota
	// capturing: a scan captured the tile; its frame has not been encoded yet.
	capturing
	// tracked: a tile was handed to the pacer; changes are reported until
	// the client is told it is invalid.
	tracked
	// ignored: the settled pixels were not worth sending (photographs);
	// the slot waits for its next change.
	ignored
)

type slot struct {
	hash    uint64
	changed time.Time
	seen    bool
	state   slotState
	// sent is the hash of the tracked tile, captured when its scan ran.
	sent     uint64
	captured time.Time
	gen      uint32
	// slowdown doubles the settle delay this many times.
	slowdown uint8
}

// calm takes back one doubling of the settle delay for each calm period
// the slot stayed unchanged.
func (s *slot) calm(still time.Duration) {
	s.slowdown -= uint8(min(int64(s.slowdown), int64(still/calmPeriod)))
}

type scan struct {
	pts      time.Duration
	at       time.Time
	hashes   []uint64
	captures []Capture
}

type encoded struct {
	seq    uint32
	pts    time.Duration
	micros uint64
	ref    any
	// unknown marks a frame without a source timestamp, which no scan
	// matches.
	unknown bool
	// judged is set once Output.Judged had the frame's invalidation; the
	// frame may still wait for its scan, whose captures are tracked from it.
	judged bool
}

// Tracker turns scans of the captured frames and the encoder's output into
// tile captures and each frame's invalidation. Scans and encoded frames are
// matched by their source timestamps: both branches of the pipeline see the
// same buffer. Every encoded frame is judged in order; one whose scan was
// skipped reports every tile invalid, so the client never keeps a tile over
// a frame nobody checked. While no tile is tracked, a frame is judged as
// soon as it is encoded, since nothing the client may have can have
// changed; its scan's captures are tracked from it once the scan arrives.
type Tracker struct {
	out    Output
	settle time.Duration

	mu      sync.Mutex
	grid    dlnet.TileGrid
	slots   []slot
	scans   []scan
	pending []encoded
	seq     uint32
	// reset makes the next tile_frame invalidate every earlier tile.
	reset bool
	// epoch counts restarts, so a scan that spans one is dropped.
	epoch                      uint64
	lastScan, lastEnc          time.Duration
	haveScan, haveEnc          bool
	resetAsked                 atomic.Bool
	skipped, collected, slowed uint64
}

// NewTracker returns a tracker for a stream of grid's size whose first
// tile_frame invalidates every tile the client may hold.
func NewTracker(grid dlnet.TileGrid, out Output) *Tracker {
	t := &Tracker{out: out, settle: SettleDelay}
	t.restartLocked(grid)
	return t
}

// Restart starts over for a new pipeline of grid's size: encoded frames
// waiting for a scan are reported with every tile invalid, and the next
// judged frame invalidates every earlier tile.
func (t *Tracker) Restart(grid dlnet.TileGrid) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := range t.pending {
		t.judge(&t.pending[i], nil)
	}
	t.restartLocked(grid)
}

func (t *Tracker) restartLocked(grid dlnet.TileGrid) {
	t.grid = grid
	t.slots = make([]slot, grid.Count())
	t.scans = nil
	t.pending = nil
	t.reset = true
	t.epoch++
	t.haveScan, t.haveEnc = false, false
}

// RequestReset makes the next judged frame invalidate every earlier tile.
// It only sets a flag, so it may be called from Output.
func (t *Tracker) RequestReset() { t.resetAsked.Store(true) }

// Scan takes one captured frame, scanned at the time at: it hashes every
// tile, notes which changed, and captures the pixels of tiles that have
// been unchanged for the settle delay. It keeps nothing of f.Pix.
func (t *Tracker) Scan(f Frame, at time.Time) {
	t.mu.Lock()
	grid, epoch := t.grid, t.epoch
	t.mu.Unlock()
	if !grid.Valid() || f.Stride < grid.Width*4 || len(f.Pix) < f.Stride*(grid.Height-1)+grid.Width*4 {
		slog.Debug("tile scan does not match the stream size", "width", grid.Width, "height", grid.Height, "stride", f.Stride, "bytes", len(f.Pix))
		return
	}
	hashes := hashTiles(grid, f.Pix, f.Stride)

	t.mu.Lock()
	if t.epoch != epoch {
		t.mu.Unlock()
		return
	}
	if t.haveScan && f.PTS <= t.lastScan {
		if f.PTS == t.lastScan {
			t.mu.Unlock()
			return
		}
		// Timestamps going backwards mean the stream started over.
		t.restartFromSource()
		epoch = t.epoch
	}
	t.lastScan, t.haveScan = f.PTS, true
	var picked []int
	for i, h := range hashes {
		s := &t.slots[i]
		if !s.seen || s.hash != h {
			if s.seen {
				s.calm(at.Sub(s.changed))
			}
			s.hash, s.changed, s.seen = h, at, true
			if s.state == ignored {
				s.state = idle
			}
		}
		if s.state == idle && at.Sub(s.changed) >= t.settle<<s.slowdown {
			s.state = capturing
			picked = append(picked, i)
		}
	}
	t.mu.Unlock()

	// Copying runs outside the lock: on a full-screen settle it moves the
	// whole frame, and the encoder's thread reports frames meanwhile.
	captures := make([]Capture, 0, len(picked))
	for _, i := range picked {
		x, y, w, h := grid.Rect(i)
		pix := make([]byte, 0, w*h*4)
		for r := y; r < y+h; r++ {
			pix = append(pix, f.Pix[r*f.Stride+x*4:r*f.Stride+(x+w)*4]...)
		}
		captures = append(captures, Capture{Grid: grid, Slot: i, At: at, W: w, H: h, Pix: pix})
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.epoch != epoch {
		return
	}
	t.scans = append(t.scans, scan{pts: f.PTS, at: at, hashes: hashes, captures: captures})
	t.match()
}

// Encoded takes the encoder's next frame, by the source timestamp of the
// buffer it was encoded from (negative when unknown) and its capture time as
// sent to the client; Output.Judged gets ref back with its invalidation.
func (t *Tracker) Encoded(pts time.Duration, captureMicros uint64, ref any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seq++
	e := encoded{seq: t.seq, pts: pts, micros: captureMicros, ref: ref, unknown: pts < 0}
	if !e.unknown {
		if t.haveEnc && pts < t.lastEnc {
			t.restartFromSource()
		}
		t.lastEnc, t.haveEnc = pts, true
	}
	t.pending = append(t.pending, e)
	t.match()
}

// restartFromSource starts over after the source's timestamps went
// backwards, keeping the grid.
func (t *Tracker) restartFromSource() {
	for i := range t.pending {
		t.judge(&t.pending[i], nil)
	}
	t.restartLocked(t.grid)
}

// match judges the encoded frames, oldest first, as their scans arrive.
func (t *Tracker) match() {
	for len(t.pending) > 0 {
		e := &t.pending[0]
		if e.unknown {
			t.skipped++
			t.judge(e, nil)
			t.pending = t.pending[1:]
			continue
		}
		i, later := 0, false
		for ; i < len(t.scans); i++ {
			if t.scans[i].pts >= e.pts {
				later = t.scans[i].pts > e.pts
				break
			}
		}
		switch {
		case i < len(t.scans) && !later:
			t.dropScans(i)
			t.judge(e, &t.scans[0])
			t.adopt(e, &t.scans[0])
			t.scans = t.scans[1:]
		case i < len(t.scans) || len(t.pending) > maxPendingFrames:
			// The scan of this frame was skipped: a scan of a later frame
			// arrived, or too many frames wait.
			t.dropScans(i)
			t.skipped++
			t.judge(e, nil)
		default:
			// The oldest frame waits for its scan. Only frames after it can
			// see the tiles its scan captures, so it need not wait to be
			// judged while the client may have no tile at all.
			if !e.judged && !t.anyTracked() {
				t.report(e, t.resetDue(), nil)
			}
			t.trimScans()
			return
		}
		t.pending = t.pending[1:]
	}
	t.trimScans()
}

// anyTracked reports whether the client may have a tile.
func (t *Tracker) anyTracked() bool {
	for i := range t.slots {
		if t.slots[i].state == tracked {
			return true
		}
	}
	return false
}

// dropScans forgets the first n scans: frames the encoder never encoded.
// Their captures go back to waiting, to be captured again by a later scan.
func (t *Tracker) dropScans(n int) {
	for _, s := range t.scans[:n] {
		for _, c := range s.captures {
			if t.slots[c.Slot].state == capturing {
				t.slots[c.Slot].state = idle
			}
		}
	}
	t.scans = t.scans[n:]
}

func (t *Tracker) trimScans() {
	if len(t.scans) > maxScans {
		t.dropScans(len(t.scans) - maxScans)
	}
}

// judge reports which tracked tiles frame e changed, every one when sc is
// nil (no scan) or a reset is due, unless e was judged already. A tile that
// changed soon after its capture settles more slowly from then on;
// invalidating every tile slows none.
func (t *Tracker) judge(e *encoded, sc *scan) {
	if e.judged {
		return
	}
	all := t.resetDue() || sc == nil
	var changed []uint16
	for i := range t.slots {
		s := &t.slots[i]
		if s.state != tracked {
			continue
		}
		if all || sc.hashes[i] != s.sent {
			if !all {
				changed = append(changed, uint16(i))
				if sc.at.Sub(s.captured) < briefTile && s.slowdown < maxSlowdown {
					s.slowdown++
					t.slowed++
				}
			}
			s.state = idle
			s.gen++
		}
	}
	t.report(e, all, changed)
}

// resetDue reports whether the next judged frame must invalidate every
// earlier tile.
func (t *Tracker) resetDue() bool { return t.reset || t.resetAsked.Swap(false) }

// report hands frame e's invalidation to the output. The client's tiles
// from before this frame are judged before its own captures start (adopt),
// so a tile captured from this very frame survives All.
func (t *Tracker) report(e *encoded, all bool, changed []uint16) {
	t.reset = false
	e.judged = true
	t.out.Judged(dlnet.TileFrame{Grid: t.grid, Seq: e.seq, CaptureMicros: e.micros, All: all, Changed: changed}, e.ref)
}

// adopt starts tracking the tiles captured from frame e's scan.
func (t *Tracker) adopt(e *encoded, sc *scan) {
	for _, c := range sc.captures {
		s := &t.slots[c.Slot]
		if s.state != capturing {
			continue
		}
		s.state = tracked
		s.sent, s.captured = sc.hashes[c.Slot], c.At
		s.gen++
		c.Seq, c.Gen = e.seq, s.gen
		t.collected++
		t.out.Captured(c)
	}
}

// Valid reports whether a capture of slot at generation gen still shows
// what the client would be told is there: nothing has changed it since.
func (t *Tracker) Valid(grid dlnet.TileGrid, slot int, gen uint32) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.validLocked(grid, slot, gen)
}

func (t *Tracker) validLocked(grid dlnet.TileGrid, slot int, gen uint32) bool {
	if grid != t.grid || slot < 0 || slot >= len(t.slots) {
		return false
	}
	s := t.slots[slot]
	return s.state == tracked && s.gen == gen
}

// Ignore stops tracking a capture that will not be sent, until its slot
// changes again; the client never had it, so nothing is reported.
func (t *Tracker) Ignore(grid dlnet.TileGrid, slot int, gen uint32) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.validLocked(grid, slot, gen) {
		t.slots[slot].state = ignored
		t.slots[slot].gen++
	}
}

// Ref names one capture in a message, for SendIfValid.
type Ref struct {
	Slot int
	Gen  uint32
}

// SendIfValid queues msg, a tile_data message of grid's tiles refs, if
// any of them is still valid, and reports whether it did. Checking and
// queueing under the lock keeps the message ahead of any tile_frame that
// invalidates its tiles on the WebSocket; a tile already invalid when it is
// queued is discarded by the client, which has seen the newer invalidation.
// An invalidation sent over UDP may arrive before or after the message,
// and the client takes either order (docs/protocol.md).
func (t *Tracker) SendIfValid(grid dlnet.TileGrid, refs []Ref, msg []byte) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, r := range refs {
		if t.validLocked(grid, r.Slot, r.Gen) {
			t.out.Send(msg)
			return true
		}
	}
	return false
}

// Stats are counters for the session log.
type Stats struct {
	// Frames is the number of encoded frames judged, Skipped how many of
	// them had no scan, Captured the tiles captured for sending, Slowed how
	// often a tile's settle delay doubled because it changed soon after its
	// capture.
	Frames, Skipped, Captured, Slowed uint64
}

// Stats returns the counters.
func (t *Tracker) Stats() Stats {
	t.mu.Lock()
	defer t.mu.Unlock()
	return Stats{Frames: uint64(t.seq), Skipped: t.skipped, Captured: t.collected, Slowed: t.slowed}
}
