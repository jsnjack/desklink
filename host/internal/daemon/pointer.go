package daemon

import (
	"desklink/internal/desktop"
	"desklink/internal/input"
)

// pointerMap converts absolute positions from the client, which are relative
// to the whole video frame, into the range of the host's absolute devices,
// which spans the whole desktop. It removes the borders vapostproc adds when
// the monitor could not switch to the stream's aspect ratio, and places the
// captured monitor where it sits among the other monitors.
type pointerMap struct {
	// content is the captured picture inside the frame, as fractions.
	cx, cy, cw, ch float64
	// monitor is the captured monitor inside the desktop, as fractions.
	mx, my, mw, mh float64
}

// identityMap passes positions through unchanged.
var identityMap = pointerMap{cw: 1, ch: 1, mw: 1, mh: 1}

// newPointerMap builds the map for a stream of streamW x streamH showing the
// monitor at monitor (logical coordinates) on a desktop spanning stage.
func newPointerMap(streamW, streamH int, monitor, stage desktop.Rect) pointerMap {
	if streamW <= 0 || streamH <= 0 || monitor.W <= 0 || monitor.H <= 0 || stage.W <= 0 || stage.H <= 0 {
		return identityMap
	}
	sw, sh := float64(streamW), float64(streamH)
	scale := min(sw/float64(monitor.W), sh/float64(monitor.H))
	cw, ch := float64(monitor.W)*scale/sw, float64(monitor.H)*scale/sh
	return pointerMap{
		cx: (1 - cw) / 2, cy: (1 - ch) / 2, cw: cw, ch: ch,
		mx: float64(monitor.X-stage.X) / float64(stage.W),
		my: float64(monitor.Y-stage.Y) / float64(stage.H),
		mw: float64(monitor.W) / float64(stage.W),
		mh: float64(monitor.H) / float64(stage.H),
	}
}

func (m pointerMap) apply(x, y int32) (int32, int32) {
	conv := func(v int32, c, cs, mo, ms float64) int32 {
		u := (float64(v)/input.AbsMax - c) / cs
		u = max(0, min(u, 1))
		return int32((mo + u*ms) * input.AbsMax)
	}
	return conv(x, m.cx, m.cw, m.mx, m.mw), conv(y, m.cy, m.ch, m.my, m.mh)
}
