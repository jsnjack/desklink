package video

import "time"

// Thresholds of the adaptive quantizer.
const (
	degradeLossPct  = 2.0
	degradeRTT      = 80 * time.Millisecond
	recoverLossPct  = 0.5
	recoverRTT      = 40 * time.Millisecond
	degradeStep     = 2
	recoverStep     = 1
	degradeCooldown = time.Second
	recoverAfter    = 5 * time.Second
)

// QualityController raises the quantizer when the client reports loss or a
// high RTT, and lowers it back towards the requested value after a sustained
// clean period. It never goes below the client's requested value.
type QualityController struct {
	floor, ceiling int
	qp             int
	lastChange     time.Time
	cleanSince     time.Time
}

// NewQualityController starts at the requested quantizer, clamped to min..max.
func NewQualityController(requested, minQP, maxQP int) *QualityController {
	q := clampQP(requested, minQP, maxQP)
	return &QualityController{floor: q, ceiling: maxQP, qp: q}
}

// QP is the current quantizer.
func (c *QualityController) QP() int { return c.qp }

// Reset sets a new requested quantizer, for example after a quality message.
func (c *QualityController) Reset(requested, minQP, maxQP int) {
	*c = *NewQualityController(requested, minQP, maxQP)
}

// Observe takes one telemetry sample and returns the quantizer to use and
// whether it changed.
func (c *QualityController) Observe(now time.Time, lossPct float64, rtt time.Duration) (int, bool) {
	bad := lossPct >= degradeLossPct || rtt >= degradeRTT
	clean := lossPct < recoverLossPct && rtt < recoverRTT
	switch {
	case bad:
		c.cleanSince = time.Time{}
		if c.qp < c.ceiling && now.Sub(c.lastChange) >= degradeCooldown {
			c.qp = min(c.qp+degradeStep, c.ceiling)
			c.lastChange = now
			return c.qp, true
		}
	case clean:
		if c.cleanSince.IsZero() {
			c.cleanSince = now
		}
		if c.qp > c.floor && now.Sub(c.cleanSince) >= recoverAfter {
			c.qp = max(c.qp-recoverStep, c.floor)
			c.lastChange = now
			c.cleanSince = now
			return c.qp, true
		}
	default:
		c.cleanSince = time.Time{}
	}
	return c.qp, false
}

func clampQP(qp, lo, hi int) int { return max(lo, min(qp, hi)) }
