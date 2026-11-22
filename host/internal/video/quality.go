package video

import (
	"slices"
	"time"
)

// Thresholds of the adaptive quality controller. Delay is judged against the
// connection's own lowest recent RTT, so a long but steady office VPN path is
// not mistaken for congestion.
const (
	degradeLossPct  = 2.0
	degradeQueueing = 40 * time.Millisecond
	recoverLossPct  = 0.5
	recoverQueueing = 15 * time.Millisecond
	degradeStep     = 2
	recoverStep     = 1
	degradeCooldown = time.Second
	recoverAfter    = 5 * time.Second
	baselineWindow  = time.Minute

	// Level changes are rarer than quantizer changes: a resolution change
	// restarts the pipeline.
	levelDownAfter   = 3 * time.Second
	levelDownSpacing = 10 * time.Second
	levelUpAfter     = 20 * time.Second
	probeWindow      = 15 * time.Second
	probeFailAfter   = time.Second
	baseLockout      = 2 * time.Minute
	maxLockout       = 10 * time.Minute
	// probeQPMargin starts a new, higher level a little above the floor.
	probeQPMargin = 2

	ladderFPS = 60
)

// Ladder widths below the client's maximum, keeping its aspect ratio.
var ladderWidths = []int{1920, 1280}

// Level is one rung of the quality ladder.
type Level struct {
	Width  int `json:"width"`
	Height int `json:"height"`
	FPS    int `json:"fps"`
}

// Levels derives the ladder from the client's maximum: the full size at the
// full rate, the full size at 60 fps when the maximum is faster, then 1920 and
// 1280 pixels wide at 60 fps. Sizes keep the aspect ratio and stay even.
func Levels(maxW, maxH, maxFPS int) []Level {
	out := []Level{{Width: maxW, Height: maxH, FPS: maxFPS}}
	if maxFPS > ladderFPS {
		out = append(out, Level{Width: maxW, Height: maxH, FPS: ladderFPS})
	}
	for _, w := range ladderWidths {
		if w < maxW {
			h := (maxH*w/maxW + 1) &^ 1
			out = append(out, Level{Width: w, Height: h, FPS: min(maxFPS, ladderFPS)})
		}
	}
	return out
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

// QualityController adapts the stream to the connection. It raises the
// quantizer when the client reports loss or queueing delay and lowers it back
// to the requested value after a clean period. With auto quality it also
// steps down the level ladder when the quantizer is at its ceiling and the
// trouble persists, and probes one level up after a long clean period; a
// probe that fails steps straight back and blocks probing for a lockout that
// doubles with each failure.
type QualityController struct {
	floor, ceiling int
	qp             int
	levels         []Level
	level          int
	auto           bool

	lastChange      time.Time
	cleanSince      time.Time
	badSince        time.Time
	lastLevelChange time.Time
	probeUntil      time.Time
	upLockedUntil   time.Time
	lockout         time.Duration
	rtts            []rttSample
}

// NewQualityController starts at the requested quantizer, clamped to min..max,
// on level start of levels. Without auto it never leaves that level.
func NewQualityController(requested, minQP, maxQP int, levels []Level, start int, auto bool) *QualityController {
	q := clampQP(requested, minQP, maxQP)
	if len(levels) == 0 {
		levels = []Level{{}}
	}
	return &QualityController{
		floor: q, ceiling: maxQP, qp: q,
		levels: levels, level: max(0, min(start, len(levels)-1)), auto: auto,
		lockout: baseLockout,
	}
}

// QP is the current quantizer.
func (c *QualityController) QP() int { return c.qp }

// Level is the current ladder level.
func (c *QualityController) Level() int { return c.level }

// Levels is the ladder.
func (c *QualityController) Levels() []Level { return c.levels }

// Auto reports whether the controller moves between levels.
func (c *QualityController) Auto() bool { return c.auto }

// Reset sets a new requested quantizer, for example after a quality message,
// keeping the level and the RTT history.
func (c *QualityController) Reset(requested, minQP, maxQP int) {
	q := clampQP(requested, minQP, maxQP)
	c.floor, c.ceiling, c.qp = q, maxQP, q
	c.cleanSince, c.badSince = time.Time{}, time.Time{}
}

// baseline is the lowest RTT of the last minute, the connection's delay
// without queueing.
func (c *QualityController) baseline(now time.Time, rtt time.Duration) time.Duration {
	if rtt > 0 {
		c.rtts = append(c.rtts, rttSample{now, rtt})
	}
	c.rtts = slices.DeleteFunc(c.rtts, func(s rttSample) bool { return now.Sub(s.at) > baselineWindow })
	if len(c.rtts) == 0 {
		return 0
	}
	low := c.rtts[0].rtt
	for _, s := range c.rtts[1:] {
		low = min(low, s.rtt)
	}
	return low
}

// Observe takes one telemetry sample and returns what to change.
func (c *QualityController) Observe(now time.Time, lossPct float64, rtt time.Duration) Decision {
	queueing := time.Duration(0)
	if base := c.baseline(now, rtt); rtt > 0 {
		queueing = rtt - base
	}
	bad := lossPct >= degradeLossPct || queueing >= degradeQueueing
	clean := lossPct < recoverLossPct && queueing < recoverQueueing
	d := Decision{QP: c.qp, Level: c.level}
	switch {
	case bad:
		c.cleanSince = time.Time{}
		if c.badSince.IsZero() {
			c.badSince = now
		}
		if c.auto && now.Before(c.probeUntil) && now.Sub(c.badSince) >= probeFailAfter {
			c.failProbe(now)
			return c.levelDecision(now, d)
		}
		if c.qp < c.ceiling && now.Sub(c.lastChange) >= degradeCooldown {
			c.qp = min(c.qp+degradeStep, c.ceiling)
			c.lastChange = now
			d.QP, d.QPChanged = c.qp, true
			return d
		}
		if c.auto && c.qp >= c.ceiling && c.level < len(c.levels)-1 &&
			now.Sub(c.badSince) >= levelDownAfter && now.Sub(c.lastLevelChange) >= levelDownSpacing {
			c.level++
			c.qp = c.floor + (c.ceiling-c.floor)/2
			return c.levelDecision(now, d)
		}
	case clean:
		c.badSince = time.Time{}
		if c.cleanSince.IsZero() {
			c.cleanSince = now
		}
		if !c.probeUntil.IsZero() && !now.Before(c.probeUntil) {
			// The probe held for its whole window.
			c.probeUntil = time.Time{}
			c.lockout = baseLockout
		}
		if c.qp > c.floor && now.Sub(c.cleanSince) >= recoverAfter {
			c.qp = max(c.qp-recoverStep, c.floor)
			c.lastChange = now
			c.cleanSince = now
			d.QP, d.QPChanged = c.qp, true
			return d
		}
		if c.auto && c.qp == c.floor && c.level > 0 && now.Sub(c.cleanSince) >= levelUpAfter &&
			!now.Before(c.upLockedUntil) && now.Sub(c.lastLevelChange) >= levelUpAfter {
			c.level--
			c.qp = min(c.floor+probeQPMargin, c.ceiling)
			c.probeUntil = now.Add(probeWindow)
			c.cleanSince = now
			return c.levelDecision(now, d)
		}
	default:
		c.cleanSince, c.badSince = time.Time{}, time.Time{}
	}
	return d
}

// failProbe steps back down after a failed probe and blocks probing.
func (c *QualityController) failProbe(now time.Time) {
	c.level = min(c.level+1, len(c.levels)-1)
	c.qp = c.floor + (c.ceiling-c.floor)/2
	c.probeUntil = time.Time{}
	c.upLockedUntil = now.Add(c.lockout)
	c.lockout = min(c.lockout*2, maxLockout)
}

func (c *QualityController) levelDecision(now time.Time, d Decision) Decision {
	c.lastLevelChange, c.lastChange = now, now
	c.badSince, c.cleanSince = time.Time{}, time.Time{}
	return Decision{QP: c.qp, QPChanged: c.qp != d.QP, Level: c.level, LevelChanged: c.level != d.Level}
}

func clampQP(qp, lo, hi int) int { return max(lo, min(qp, hi)) }
