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
	// are sent. A caret blinks every 600 ms in GNOME and typing changes a
	// tile a few times a second, so in either case the tile settles soon
	// after the change stops, while one that keeps changing (a video, a
	// progress bar, scrolling) never wastes bytes on pixels about to change.
	SettleDelay = 200 * time.Millisecond
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

// Output takes what the tracker produces. Both methods run with the
// tracker's lock held, so they must neither block nor call the tracker.
type Output interface {
	// Send queues a binary message for the client; messages must reach the
	// client in the order they are queued.
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
	// sent is the hash of the tracked tile.
	sent uint64
	gen  uint32
}

type scan struct {
	pts      time.Duration
	hashes   []uint64
	captures []Capture
}

type encoded struct {
	seq    uint32
	pts    time.Duration
	micros uint64
	// unknown marks a frame without a source timestamp, which no scan
	// matches.
	unknown bool
}

// Tracker turns scans of the captured frames and the encoder's output into
// tile captures and tile_frame messages. Scans and encoded frames are
// matched by their source timestamps: both branches of the pipeline see the
// same buffer. Every encoded frame gets a tile_frame message in order;
// one whose scan was skipped reports every tile invalid, so the client never
// keeps a tile over a frame nobody checked.
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
	epoch              uint64
	lastScan, lastEnc  time.Duration
	haveScan, haveEnc  bool
	resetAsked         atomic.Bool
	skipped, collected uint64
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
// tile_frame invalidates every earlier tile.
func (t *Tracker) Restart(grid dlnet.TileGrid) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, e := range t.pending {
		t.decide(e, nil)
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

// RequestReset makes the next tile_frame invalidate every earlier tile. It
// only sets a flag, so it may be called from Output.
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
	hashes := make([]uint64, grid.Count())
	for i := range hashes {
		x, y, w, h := grid.Rect(i)
		hashes[i] = hashTile(f.Pix, f.Stride, x, y, w, h)
	}

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
			s.hash, s.changed, s.seen = h, at, true
			if s.state == ignored {
				s.state = idle
			}
		}
		if s.state == idle && at.Sub(s.changed) >= t.settle {
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
	t.scans = append(t.scans, scan{pts: f.PTS, hashes: hashes, captures: captures})
	t.match()
}

// Encoded takes the encoder's next frame, by the source timestamp of the
// buffer it was encoded from (negative when unknown) and its capture time as
// sent to the client.
func (t *Tracker) Encoded(pts time.Duration, captureMicros uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seq++
	e := encoded{seq: t.seq, pts: pts, micros: captureMicros, unknown: pts < 0}
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
	for _, e := range t.pending {
		t.decide(e, nil)
	}
	t.restartLocked(t.grid)
}

// match judges the encoded frames, oldest first, as their scans arrive.
func (t *Tracker) match() {
	for len(t.pending) > 0 {
		e := t.pending[0]
		if e.unknown {
			t.skipped++
			t.decide(e, nil)
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
			t.decide(e, &t.scans[0])
			t.scans = t.scans[1:]
		case i < len(t.scans) || len(t.pending) > maxPendingFrames:
			// The scan of this frame was skipped: a scan of a later frame
			// arrived, or too many frames wait.
			t.dropScans(i)
			t.skipped++
			t.decide(e, nil)
		default:
			t.trimScans()
			return
		}
		t.pending = t.pending[1:]
	}
	t.trimScans()
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

// decide reports which tracked tiles frame e changed, every one when sc is
// nil (no scan), and starts tracking the tiles captured from its scan.
func (t *Tracker) decide(e encoded, sc *scan) {
	all := t.reset || t.resetAsked.Swap(false) || sc == nil
	var changed []uint16
	for i := range t.slots {
		s := &t.slots[i]
		if s.state != tracked {
			continue
		}
		if all || sc.hashes[i] != s.sent {
			if !all {
				changed = append(changed, uint16(i))
			}
			s.state = idle
			s.gen++
		}
	}
	t.reset = false
	// The client's tiles from before this frame are judged before the new
	// ones start, so a tile captured from this very frame survives All.
	t.out.Send(dlnet.TileFrame{Grid: t.grid, Seq: e.seq, CaptureMicros: e.micros, All: all, Changed: changed}.Marshal())
	if sc == nil {
		return
	}
	for _, c := range sc.captures {
		s := &t.slots[c.Slot]
		if s.state != capturing {
			continue
		}
		s.state = tracked
		s.sent = sc.hashes[c.Slot]
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
// invalidates its tiles; a tile already invalid when it is queued is
// discarded by the client, which has seen the newer tile_frame.
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
	// them had no scan, Captured the tiles captured for sending.
	Frames, Skipped, Captured uint64
}

// Stats returns the counters.
func (t *Tracker) Stats() Stats {
	t.mu.Lock()
	defer t.mu.Unlock()
	return Stats{Frames: uint64(t.seq), Skipped: t.skipped, Captured: t.collected}
}
