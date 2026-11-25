package video

import (
	"slices"
	"time"
)

// Thresholds of the adaptive quality controller. Delay is judged against the
// connection's own lowest recent RTT, so a long but steady office VPN path is
// not mistaken for congestion; this applies to manual presets too.
const (
	degradeLossPct  = 2.0
	degradeQueueing = 40 * time.Millisecond
	recoverLossPct  = 0.5
	recoverQueueing = 15 * time.Millisecond
	baselineWindow  = time.Minute
	// lateSamplesForDelay: RTT is measured every 2 s but reports come every
	// 250 ms, so one late echo must not count as many bad reports.
	lateSamplesForDelay = 2
	// delayShareToAct is the share of a window's reports that must show
	// queueing delay for the window to count as congested.
	delayShareToAct = 0.75

	// How long the reports must average congestion before each kind of step
	// down. A frame-rate step costs nothing, so it comes first and soonest; a
	// quantizer step makes the encoder start a new sequence with a keyframe;
	// a size step restarts the pipeline and blurs text.
	fpsDownAfter  = time.Second
	qpUpAfter     = 2 * time.Second
	sizeDownAfter = 3 * time.Second
	// sizeDownSpacing keeps size steps apart.
	sizeDownSpacing = 10 * time.Second
	// keyframeGrace and restartGrace: right after a change that sends a
	// keyframe, the reports mostly measure that keyframe's burst. They are
	// not counted, or every step would look like congestion and probes would
	// fail on their own keyframe.
	keyframeGrace = time.Second
	restartGrace  = 2 * time.Second

	// recoverAfter is the clean time before a coarse quantizer returns to
	// the floor, which happens once the screen is still; recoverBusyAfter
	// is the clean time after which it returns although the screen keeps
	// changing.
	recoverAfter     = 5 * time.Second
	recoverBusyAfter = 20 * time.Second
	// recoverCalmAfter is the time without a congested report after which
	// any upward move may go ahead, although the reports were not clean:
	// Wi-Fi's delay often lies between the clean and the congested
	// threshold, which kept a session at the coarsest quantizer for many
	// minutes after one congested moment. A move that fails is undone and
	// locked out as any other.
	recoverCalmAfter = 30 * time.Second
	// probeAfter is the clean time before a probe one level up.
	probeAfter = 15 * time.Second
	// probeWindow is how long an upward move is watched; congestion that
	// lasts probeFailAfter within it, after the grace, undoes it.
	probeWindow    = 15 * time.Second
	probeFailAfter = time.Second
	// baseLockout and maxLockout bound how long a failed upward move blocks
	// the next; the lockout doubles with each failure in a row.
	baseLockout = time.Minute
	maxLockout  = 10 * time.Minute

	// stillAfter: the screen is still once no encoded frame showed a change
	// for this long. busyBytes is the size above which a predicted frame
	// shows a change: an unchanged 2560x1600 frame encodes to about 35
	// bytes, a typed character or the cursor blinking to 50-300.
	stillAfter = time.Second
	busyBytes  = 128

	ladderMidFPS = 60
	ladderLowFPS = 30

	// maxStartLevel is the lowest level a session starts at: one below the
	// top, which keeps the full size. A level remembered from a bad moment
	// would otherwise blur every later session on that network.
	maxStartLevel = 1
)

// FixedMinQP is the lowest quantizer a fixed-quality session may request;
// adaptive sessions stay within the configured min_qp..max_qp.
const FixedMinQP = 8

// Ladder widths below the client's maximum, keeping its aspect ratio.
var ladderWidths = []int{1920, 1280}

// Level is one rung of the quality ladder.
type Level struct {
	Width  int `json:"width"`
	Height int `json:"height"`
	FPS    int `json:"fps"`
}

func (l Level) sameSize(o Level) bool { return l.Width == o.Width && l.Height == o.Height }

// CappedFPS is fps limited to limit, the client's frame-rate cap; a limit
// of 0 means none.
func CappedFPS(fps, limit int) int {
	if limit > 0 && limit < fps {
		return limit
	}
	return fps
}

// Levels derives the ladder from the client's maximum. Text blurs when the
// client scales a smaller picture up, so the ladder keeps the full size as
// long as it can: the full size at the full rate, then at 60 and 30 fps
// where those are lower; only then 1920 and 1280 pixels wide at 30 fps (or
// the maximum rate if lower), a last resort for a link that cannot carry
// the full size even at the coarsest quantizer. Sizes keep the aspect ratio
// and stay even.
func Levels(maxW, maxH, maxFPS int) []Level {
	out := []Level{{Width: maxW, Height: maxH, FPS: maxFPS}}
	for _, fps := range []int{ladderMidFPS, ladderLowFPS} {
		if fps < maxFPS {
			out = append(out, Level{Width: maxW, Height: maxH, FPS: fps})
		}
	}
	low := min(maxFPS, ladderLowFPS)
	for _, w := range ladderWidths {
		if w < maxW {
			h := (maxH*w/maxW + 1) &^ 1
			out = append(out, Level{Width: w, Height: h, FPS: low})
		}
	}
	return out
}

// LevelFor returns the index of the best level no larger and no faster than
// want, or the last level when every level is larger. Clients remember a
// level by its size and rate, which stays meaningful when the ladder changes.
func LevelFor(levels []Level, want Level) int {
	for i, l := range levels {
		if l.Width <= want.Width && l.Height <= want.Height && l.FPS <= want.FPS {
			return i
		}
	}
	return len(levels) - 1
}

// StartLevel is the level a session starts at when the client remembers
// want from an earlier session on the same network: LevelFor, but never more
// than one level below the top, so a session starts at the full size even
// after the last one ended on a bad link. A level at the top's frame rate or
// above starts at the top whatever its size: clients ask for their view's
// size, so a session after one in a smaller window or view remembers a
// smaller size, which says nothing about the link.
func StartLevel(levels []Level, want Level) int {
	if len(levels) > 0 && want.FPS >= levels[0].FPS {
		return 0
	}
	return min(LevelFor(levels, want), maxStartLevel, max(len(levels)-1, 0))
}

// Decision is what Observe asks the session to change.
type Decision struct {
	QP           int
	QPChanged    bool
	Level        int
	LevelChanged bool
}

type rttSample struct {
	at  time.Time
	rtt time.Duration
}

type report struct {
	at    time.Time
	loss  float64
	delay bool
}

// QualityConfig sets up a QualityController.
type QualityConfig struct {
	// Requested is the client's quantizer, the floor the controller returns
	// to. It is clamped to MinQP..MaxQP, or to FixedMinQP..MaxQP with Fixed.
	Requested, MinQP, MaxQP int
	// Levels is the ladder and Start the level to begin at.
	Levels []Level
	Start  int
	// Auto lets the controller move between levels.
	Auto bool
	// Fixed keeps the quantizer and level as they start, whatever the
	// connection does.
	Fixed bool
}

// QualityController adapts the stream to the connection. When the reports
// keep showing loss or queueing delay it sheds load in a fixed order: with
// auto quality first the frame rate at the full size (free), then the
// quantizer in two large steps up to max_qp (each one keyframe), and only
// then the size. It undoes those steps in the reverse order after clean
// periods: a smaller size probes back up first, then the quantizer returns
// to the floor in one step, preferably once the screen is still, when the
// keyframe that change costs competes with nothing and the user reads,
// and last the frame rate probes back up. An upward move that meets
// congestion soon after is undone at once and blocks the next for a
// lockout that doubles with each failure. The client's frame-rate cap
// (SetFrameRateCap) lowers every level's rate without changing the ladder.
type QualityController struct {
	floor, ceiling int
	qp             int
	levels         []Level
	level          int
	auto, fixed    bool
	// fpsCap is the client's frame-rate cap (SetFrameRateCap), 0 for none.
	fpsCap int

	// calmFrom is when the reports start to count again after the last
	// change (its grace); troubleSince is the first congested report of the
	// current episode. A step needs its window of reports after both.
	calmFrom     time.Time
	troubleSince time.Time
	cleanSince   time.Time
	// calmSince is the first report after the last congested one.
	calmSince time.Time
	lastSize  time.Time
	lastBusy  time.Time

	// The running upward move: the state before it, and the end of its
	// watch window (zero when none runs).
	probeUntil    time.Time
	beforeLevel   int
	beforeQP      int
	upLockedUntil time.Time
	lockout       time.Duration

	rtts       []rttSample
	reports    []report
	lateRun    int
	delayBad   bool
	delayClean bool
}

// NewQualityController starts at the requested quantizer on the start level.
// Without Auto it never leaves that level; with Fixed it changes nothing.
func NewQualityController(cfg QualityConfig) *QualityController {
	levels := cfg.Levels
	if len(levels) == 0 {
		levels = []Level{{}}
	}
	c := &QualityController{
		levels: levels, level: max(0, min(cfg.Start, len(levels)-1)),
		auto: cfg.Auto && !cfg.Fixed, fixed: cfg.Fixed,
		lockout: baseLockout, delayClean: true,
	}
	c.setRange(cfg.Requested, cfg.MinQP, cfg.MaxQP)
	return c
}

// Follow returns a controller for a new ladder, as when the client's own
// screen changes size, that keeps what this one learned about the
// connection: its RTT baseline, which a fresh one would take from the
// restart's own delayed echoes, and the lockout after failed upward moves.
// Reports count only after restartGrace from now, because the new pipeline
// starts with a keyframe whose burst would look like congestion.
func (c *QualityController) Follow(cfg QualityConfig, now time.Time) *QualityController {
	n := NewQualityController(cfg)
	n.rtts = slices.Clone(c.rtts)
	n.upLockedUntil, n.lockout = c.upLockedUntil, c.lockout
	n.fpsCap = c.fpsCap
	n.level = n.firstAlike(n.level)
	n.calmFrom = now.Add(restartGrace)
	return n
}

// setRange sets the floor and ceiling from a requested quantizer and puts
// the quantizer on the floor.
func (c *QualityController) setRange(requested, minQP, maxQP int) {
	if c.fixed {
		q := clampQP(requested, min(FixedMinQP, minQP), maxQP)
		c.floor, c.ceiling, c.qp = q, q, q
		return
	}
	q := clampQP(requested, minQP, maxQP)
	c.floor, c.ceiling, c.qp = q, maxQP, q
}

// QP is the current quantizer.
func (c *QualityController) QP() int { return c.qp }

// Level is the current ladder level.
func (c *QualityController) Level() int { return c.level }

// Levels is the ladder.
func (c *QualityController) Levels() []Level { return c.levels }

// Auto reports whether the controller moves between levels.
func (c *QualityController) Auto() bool { return c.auto }

// Fixed reports whether the controller keeps the quality as it started.
func (c *QualityController) Fixed() bool { return c.fixed }

// Reset sets a new requested quantizer, for example after a quality message,
// keeping the level and the delay history.
func (c *QualityController) Reset(requested, minQP, maxQP int) {
	c.setRange(requested, minQP, maxQP)
	c.cleanSince, c.troubleSince, c.probeUntil = time.Time{}, time.Time{}, time.Time{}
}

// NoteFrame records an encoded frame. A predicted frame bigger than an
// unchanged one means the screen changed; a keyframe says nothing about that.
func (c *QualityController) NoteFrame(at time.Time, bytes int, keyframe bool) {
	if !keyframe && bytes > busyBytes {
		c.lastBusy = at
	}
}

// still reports whether no encoded frame has shown a change for stillAfter.
func (c *QualityController) still(now time.Time) bool {
	return c.lastBusy.IsZero() || now.Sub(c.lastBusy) >= stillAfter
}

// observeDelay updates the delay state from a new RTT sample; rtt 0 means no
// new sample since the last report, which keeps the previous state.
func (c *QualityController) observeDelay(now time.Time, rtt time.Duration) {
	c.rtts = slices.DeleteFunc(c.rtts, func(s rttSample) bool { return now.Sub(s.at) > baselineWindow })
	if rtt <= 0 {
		return
	}
	c.rtts = append(c.rtts, rttSample{now, rtt})
	base := rtt
	for _, s := range c.rtts {
		base = min(base, s.rtt)
	}
	queueing := rtt - base
	if queueing >= degradeQueueing {
		c.lateRun++
	} else {
		c.lateRun = 0
	}
	c.delayBad = c.lateRun >= lateSamplesForDelay
	c.delayClean = queueing < recoverQueueing
}

// congested reports whether the reports of the last window average at least
// the loss threshold or mostly show queueing delay. The window must lie
// after the last change's grace and after the start of the current episode
// of congestion, so a step is never taken on reports from before the last
// one, or on a brief burst long after the last loss.
func (c *QualityController) congested(now time.Time, window time.Duration) bool {
	from := c.calmFrom
	if c.troubleSince.After(from) {
		from = c.troubleSince
	}
	if from.IsZero() || now.Sub(from) < window {
		return false
	}
	var loss float64
	var n, delayed int
	for _, r := range c.reports {
		if now.Sub(r.at) < window && !r.at.Before(from) {
			loss += r.loss
			n++
			if r.delay {
				delayed++
			}
		}
	}
	return n > 0 && (loss/float64(n) >= degradeLossPct || float64(delayed)/float64(n) >= delayShareToAct)
}

// Observe takes one telemetry sample and returns what to change. rtt is a
// new round-trip measurement, or 0 when none arrived since the last report.
func (c *QualityController) Observe(now time.Time, lossPct float64, rtt time.Duration) Decision {
	c.observeDelay(now, rtt)
	d := Decision{QP: c.qp, Level: c.level}
	if c.fixed {
		return d
	}
	if c.calmFrom.IsZero() {
		// The first window starts with the first report.
		c.calmFrom = now
	}
	bad := lossPct >= degradeLossPct || c.delayBad
	clean := lossPct < recoverLossPct && c.delayClean
	c.reports = append(c.reports, report{now, lossPct, c.delayBad})
	c.reports = slices.DeleteFunc(c.reports, func(r report) bool { return now.Sub(r.at) >= sizeDownAfter })

	if bad {
		c.calmSince = time.Time{}
	} else if c.calmSince.IsZero() {
		c.calmSince = now
	}
	switch {
	case bad:
		c.cleanSince = time.Time{}
		if c.troubleSince.IsZero() {
			c.troubleSince = now
		}
	case clean:
		if c.cleanSince.IsZero() {
			c.cleanSince = now
		}
		if now.Sub(c.cleanSince) >= recoverAfter {
			// A clean period ends the episode, so one old lossy report does
			// not join a later burst into a window.
			c.troubleSince = time.Time{}
		}
	default:
		c.cleanSince = time.Time{}
	}

	if !c.probeUntil.IsZero() && now.Before(c.probeUntil) && c.congested(now, probeFailAfter) {
		return c.failProbe(now, d)
	}
	if !c.probeUntil.IsZero() && !now.Before(c.probeUntil) {
		// The upward move held for its whole window.
		c.probeUntil = time.Time{}
		c.lockout = baseLockout
	}
	if bad {
		return c.stepDown(now, d)
	}
	return c.stepUp(now, d, clean)
}

// stepDown sheds load once the reports have been congested long enough for
// the next step: a lower frame rate at the same size, a coarser quantizer,
// then a smaller size at the coarsest quantizer.
func (c *QualityController) stepDown(now time.Time, d Decision) Decision {
	next := c.nextDown()
	hasNext := c.auto && next < len(c.levels)
	switch {
	case hasNext && c.levels[next].sameSize(c.levels[c.level]):
		if c.congested(now, fpsDownAfter) {
			c.level = next
			return c.changed(now, d, 0)
		}
	case c.qp < c.ceiling:
		if c.congested(now, qpUpAfter) {
			c.qp = c.nextQPUp()
			return c.changed(now, d, keyframeGrace)
		}
	case hasNext:
		if now.Sub(c.lastSize) >= sizeDownSpacing && c.congested(now, sizeDownAfter) {
			c.level = next
			c.lastSize = now
			return c.changed(now, d, restartGrace)
		}
	}
	return d
}

// nextQPUp is the next of the quantizer's three steps: the floor, the middle
// of the range and the ceiling.
func (c *QualityController) nextQPUp() int {
	if mid := (c.floor + c.ceiling + 1) / 2; c.qp < mid {
		return mid
	}
	return c.ceiling
}

// stepUp undoes the last kind of step down after a clean period, or after
// recoverCalmAfter without congestion when clean is false. It waits while
// the last upward move is still watched, and while a failed one locks them
// out.
func (c *QualityController) stepUp(now time.Time, d Decision, clean bool) Decision {
	if now.Before(c.upLockedUntil) || !c.probeUntil.IsZero() {
		return d
	}
	var cleanFor time.Duration
	if clean {
		cleanFor = now.Sub(c.cleanSince)
	}
	calm := now.Sub(c.calmSince) >= recoverCalmAfter
	prev := -1
	if c.level > 0 {
		prev = c.firstAlike(c.level - 1)
	}
	switch {
	case c.auto && prev >= 0 && !c.levels[prev].sameSize(c.levels[c.level]):
		// Back to the larger size first, at the same quantizer, whose
		// keyframe is small at the coarse quantizer the step down left.
		if cleanFor >= probeAfter || calm {
			c.startProbe(now)
			c.level = prev
			c.lastSize = now
			return c.changed(now, d, restartGrace)
		}
	case c.qp > c.floor:
		if cleanFor >= recoverAfter && (c.still(now) || cleanFor >= recoverBusyAfter) || calm {
			c.startProbe(now)
			c.qp = c.floor
			return c.changed(now, d, keyframeGrace)
		}
	case c.auto && prev >= 0:
		if cleanFor >= probeAfter || calm {
			c.startProbe(now)
			c.level = prev
			return c.changed(now, d, 0)
		}
	}
	return d
}

// SetFrameRateCap limits the rate of every level to fps, the rate the
// client's screen shows (a panel held at 60 Hz by power saving, say); 0
// lifts the limit. The ladder stays as it is, so the levels clients
// remember keep their meaning. Levels of one size that the cap makes stream
// alike count as one, the first of them: a step down then always lowers the
// rate the stream runs at, and a level remembered under the cap keeps the
// best rate. When the cap rises and with it the rate the stream runs at,
// the rise is watched like an upward move and undone on congestion; while
// upward moves are locked out, the stream goes to the level of its old rate
// at once. For a cap between the ladder's rates (90 fps, say) that level is
// the next slower one. It returns the level to stream at, at
// CappedFPS(its FPS, fps).
func (c *QualityController) SetFrameRateCap(now time.Time, fps int) Decision {
	fps = max(fps, 0)
	d := Decision{QP: c.qp, Level: c.level}
	if fps == c.fpsCap {
		return d
	}
	before := c.rate(c.level)
	c.fpsCap = fps
	// A move watched under the old cap no longer measures what it changed.
	c.probeUntil = time.Time{}
	c.level = c.firstAlike(c.level)
	if c.rate(c.level) > before {
		if keep := c.slowerSameSize(before); keep >= 0 {
			if now.Before(c.upLockedUntil) {
				c.level = keep
			} else {
				c.beforeLevel, c.beforeQP = keep, c.qp
				c.probeUntil = now.Add(probeWindow)
			}
		}
	}
	if c.rate(c.level) == before {
		return Decision{QP: c.qp, Level: c.level, LevelChanged: c.level != d.Level}
	}
	// The reports so far describe the stream at its old rate.
	return c.changed(now, d, 0)
}

// rate is the frame rate level i streams at under the cap.
func (c *QualityController) rate(i int) int { return CappedFPS(c.levels[i].FPS, c.fpsCap) }

// streamsAlike reports whether levels i and j stream alike under the cap:
// the same size at the same rate.
func (c *QualityController) streamsAlike(i, j int) bool {
	return c.levels[i].sameSize(c.levels[j]) && c.rate(i) == c.rate(j)
}

// firstAlike is the first of the levels that stream like level i. Without
// a cap, neighbouring levels always stream differently.
func (c *QualityController) firstAlike(i int) int {
	for i > 0 && c.streamsAlike(i-1, i) {
		i--
	}
	return i
}

// nextDown is the first level below the current one that streams
// differently, or len(levels) when there is none.
func (c *QualityController) nextDown() int {
	next := c.level + 1
	for next < len(c.levels) && c.streamsAlike(next, c.level) {
		next++
	}
	return next
}

// slowerSameSize is the first level of the current level's size that
// streams at fps or slower, or -1 when there is none.
func (c *QualityController) slowerSameSize(fps int) int {
	for i := c.level + 1; i < len(c.levels) && c.levels[i].sameSize(c.levels[c.level]); i++ {
		if c.rate(i) <= fps {
			return c.firstAlike(i)
		}
	}
	return -1
}

func (c *QualityController) startProbe(now time.Time) {
	c.beforeLevel, c.beforeQP = c.level, c.qp
	c.probeUntil = now.Add(probeWindow)
}

// failProbe undoes an upward move that met congestion and blocks the next
// one for the lockout, which doubles.
func (c *QualityController) failProbe(now time.Time, d Decision) Decision {
	grace := keyframeGrace
	if !c.levels[c.beforeLevel].sameSize(c.levels[c.level]) {
		grace = restartGrace
		c.lastSize = now
	} else if c.beforeQP == c.qp {
		grace = 0
	}
	c.level, c.qp = c.beforeLevel, c.beforeQP
	c.probeUntil = time.Time{}
	c.upLockedUntil = now.Add(c.lockout)
	c.lockout = min(c.lockout*2, maxLockout)
	return c.changed(now, d, grace)
}

// changed starts afresh after a change: the reports so far describe the
// old stream, and those in the grace that follows describe its keyframe.
func (c *QualityController) changed(now time.Time, d Decision, grace time.Duration) Decision {
	c.calmFrom = now.Add(grace)
	c.cleanSince, c.troubleSince = time.Time{}, time.Time{}
	c.reports = nil
	return Decision{QP: c.qp, QPChanged: c.qp != d.QP, Level: c.level, LevelChanged: c.level != d.Level}
}

func clampQP(qp, lo, hi int) int { return max(lo, min(qp, hi)) }
