package desktop

import (
	"errors"
	"fmt"
	"math"
)

// layoutPhysical is Mutter's layout mode in which monitor sizes ignore the
// scale (the other mode, 1, divides them by it).
const layoutPhysical uint32 = 2

// DisplayMode is one mode a monitor supports.
type DisplayMode struct {
	ID              string
	Width, Height   int32
	Refresh         float64
	PreferredScale  float64
	SupportedScales []float64
	Current         bool
}

// Monitor is a physical monitor and its modes.
type Monitor struct {
	Connector string
	Modes     []DisplayMode
}

// LogicalMonitor is a region of the desktop shown by one or more monitors.
type LogicalMonitor struct {
	X, Y       int32
	Scale      float64
	Transform  uint32
	Primary    bool
	Connectors []string
}

// DisplayState is Mutter's GetCurrentState reduced to what a mode change needs.
type DisplayState struct {
	Serial     uint32
	Monitors   []Monitor
	Logical    []LogicalMonitor
	LayoutMode uint32
}

// MonitorAssignment shows a connector at a mode.
type MonitorAssignment struct {
	Connector string `json:"connector"`
	ModeID    string `json:"mode_id"`
}

// LogicalConfig is one logical monitor of ApplyMonitorsConfig.
type LogicalConfig struct {
	X         int32               `json:"x"`
	Y         int32               `json:"y"`
	Scale     float64             `json:"scale"`
	Transform uint32              `json:"transform"`
	Primary   bool                `json:"primary"`
	Monitors  []MonitorAssignment `json:"monitors"`
}

// DisplayLayout is a complete monitor configuration. It is stored in
// state.json while a session has changed the resolution.
type DisplayLayout struct {
	LayoutMode uint32          `json:"layout_mode"`
	Logical    []LogicalConfig `json:"logical"`
}

func (s DisplayState) monitor(connector string) (Monitor, bool) {
	for _, m := range s.Monitors {
		if m.Connector == connector {
			return m, true
		}
	}
	return Monitor{}, false
}

func (m Monitor) current() (DisplayMode, bool) {
	for _, mode := range m.Modes {
		if mode.Current {
			return mode, true
		}
	}
	return DisplayMode{}, false
}

// CurrentLayout returns the active configuration, for restoring later.
func (s DisplayState) CurrentLayout() (DisplayLayout, error) {
	out := DisplayLayout{LayoutMode: s.LayoutMode}
	for _, lm := range s.Logical {
		cfg := LogicalConfig{X: lm.X, Y: lm.Y, Scale: lm.Scale, Transform: lm.Transform, Primary: lm.Primary}
		for _, conn := range lm.Connectors {
			mon, ok := s.monitor(conn)
			if !ok {
				return DisplayLayout{}, fmt.Errorf("logical monitor references unknown connector %s", conn)
			}
			mode, ok := mon.current()
			if !ok {
				return DisplayLayout{}, fmt.Errorf("monitor %s has no current mode", conn)
			}
			cfg.Monitors = append(cfg.Monitors, MonitorAssignment{Connector: conn, ModeID: mode.ID})
		}
		out.Logical = append(out.Logical, cfg)
	}
	if len(out.Logical) == 0 {
		return DisplayLayout{}, errors.New("no active logical monitors")
	}
	return out, nil
}

// ResolutionPlan is the result of PlanResolution.
type ResolutionPlan struct {
	Layout    DisplayLayout
	Mode      DisplayMode
	Connector string
	// Changed is false when the primary monitor already runs the chosen mode.
	Changed bool
}

// ErrNoMatchingMode means the primary monitor has no mode of the requested size.
var ErrNoMatchingMode = errors.New("primary monitor has no mode of the requested size")

// PlanResolution picks a mode of exactly width x height on the primary
// monitor, with the highest refresh rate not above fps (or the lowest one if
// all are above), and returns the full layout to apply. The scale is kept if
// the new mode supports it, otherwise the nearest supported scale is used,
// and monitors right of or below the primary one move with its new edge.
func PlanResolution(s DisplayState, width, height, fps int) (ResolutionPlan, error) {
	layout, err := s.CurrentLayout()
	if err != nil {
		return ResolutionPlan{}, err
	}
	idx := primaryIndex(s.Logical)
	conn := s.Logical[idx].Connectors[0]
	mon, _ := s.monitor(conn)
	mode, ok := pickMode(mon.Modes, int32(width), int32(height), float64(fps))
	if !ok {
		return ResolutionPlan{}, fmt.Errorf("%w: %s has no %dx%d", ErrNoMatchingMode, conn, width, height)
	}
	cur, _ := mon.current()
	lc := &layout.Logical[idx]
	oldW, oldH := logicalSize(cur, lc.Scale, lc.Transform, s.LayoutMode)
	scale := nearestScale(mode.SupportedScales, lc.Scale)
	newW, newH := logicalSize(mode, scale, lc.Transform, s.LayoutMode)
	plan := ResolutionPlan{Mode: mode, Connector: conn, Changed: mode.ID != cur.ID || scale != lc.Scale}
	for i := range lc.Monitors {
		lc.Monitors[i].ModeID = mode.ID
	}
	right, bottom := lc.X+oldW, lc.Y+oldH
	lc.Scale = scale
	for i := range layout.Logical {
		if i == idx {
			continue
		}
		o := &layout.Logical[i]
		if o.X >= right {
			o.X += newW - oldW
		}
		if o.Y >= bottom {
			o.Y += newH - oldH
		}
	}
	plan.Layout = layout
	return plan, nil
}

func primaryIndex(l []LogicalMonitor) int {
	for i, lm := range l {
		if lm.Primary {
			return i
		}
	}
	return 0
}

func pickMode(modes []DisplayMode, w, h int32, fps float64) (DisplayMode, bool) {
	var best DisplayMode
	found := false
	// Refresh rates are fractional (59.951 Hz), so allow half a hertz over.
	limit := fps + 0.5
	for _, m := range modes {
		if m.Width != w || m.Height != h {
			continue
		}
		switch {
		case !found:
			best, found = m, true
		case best.Refresh > limit && m.Refresh < best.Refresh:
			best = m
		case m.Refresh <= limit && (best.Refresh > limit || m.Refresh > best.Refresh):
			best = m
		}
	}
	return best, found
}

func nearestScale(supported []float64, want float64) float64 {
	if len(supported) == 0 {
		return want
	}
	best := supported[0]
	for _, s := range supported {
		if math.Abs(s-want) < math.Abs(best-want) {
			best = s
		}
	}
	return best
}

// logicalSize is the space a monitor takes in the desktop layout.
func logicalSize(m DisplayMode, scale float64, transform uint32, layoutMode uint32) (int32, int32) {
	w, h := float64(m.Width), float64(m.Height)
	if layoutMode != layoutPhysical && scale > 0 {
		w, h = w/scale, h/scale
	}
	// Transforms 1 and 3 (and their flipped variants 5 and 7) rotate by 90 degrees.
	if transform%2 == 1 {
		w, h = h, w
	}
	return int32(math.Round(w)), int32(math.Round(h))
}
