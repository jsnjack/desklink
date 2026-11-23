package desktop

import (
	"errors"
	"fmt"
	"math"
	"slices"
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

// DisplayState is Mutter's GetCurrentState reduced to what DeskLink needs.
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

// Rect is a rectangle in the desktop's logical coordinates.
type Rect struct {
	X, Y, W, H int32
}

// placedMonitor is a logical monitor's first connector and its rectangle.
type placedMonitor struct {
	connector string
	rect      Rect
}

func (s DisplayState) placed() []placedMonitor {
	var out []placedMonitor
	for _, lm := range s.Logical {
		if len(lm.Connectors) == 0 {
			continue
		}
		mon, ok := s.monitor(lm.Connectors[0])
		if !ok {
			continue
		}
		mode, ok := mon.current()
		if !ok {
			continue
		}
		w, h := logicalSize(mode, lm.Scale, lm.Transform, s.LayoutMode)
		out = append(out, placedMonitor{lm.Connectors[0], Rect{X: lm.X, Y: lm.Y, W: w, H: h}})
	}
	return out
}

// LogicalRects returns where each logical monitor sits on the desktop.
func (s DisplayState) LogicalRects() []Rect {
	var out []Rect
	for _, p := range s.placed() {
		out = append(out, p.rect)
	}
	return out
}

// MonitorRect returns where the monitor on connector sits on the desktop.
func (s DisplayState) MonitorRect(connector string) (Rect, bool) {
	for _, p := range s.placed() {
		if p.connector == connector {
			return p.rect, true
		}
	}
	return Rect{}, false
}

// ConnectorAt names the monitor whose top-left corner is at x, y, which is
// how the screen-cast stream reports the recorded monitor.
func (s DisplayState) ConnectorAt(x, y int32) (string, bool) {
	for _, p := range s.placed() {
		if p.rect.X == x && p.rect.Y == y {
			return p.connector, true
		}
	}
	return "", false
}

// Stage is the bounding box of all monitors, the area absolute input
// devices span.
func (s DisplayState) Stage() Rect {
	rects := s.LogicalRects()
	if len(rects) == 0 {
		return Rect{}
	}
	minX, minY := rects[0].X, rects[0].Y
	maxX, maxY := rects[0].X+rects[0].W, rects[0].Y+rects[0].H
	for _, r := range rects[1:] {
		minX, minY = min(minX, r.X), min(minY, r.Y)
		maxX, maxY = max(maxX, r.X+r.W), max(maxY, r.Y+r.H)
	}
	return Rect{X: minX, Y: minY, W: maxX - minX, H: maxY - minY}
}

// ErrStaleLayout means a saved layout names a connector or mode the
// monitors no longer have, so it can never be applied.
var ErrStaleLayout = errors.New("saved monitor layout no longer matches the monitors")

// ValidFor checks that every connector and mode in the layout still exists.
func (l DisplayLayout) ValidFor(s DisplayState) error {
	if len(l.Logical) == 0 {
		return fmt.Errorf("%w: it has no monitors", ErrStaleLayout)
	}
	for _, lc := range l.Logical {
		for _, m := range lc.Monitors {
			mon, ok := s.monitor(m.Connector)
			if !ok {
				return fmt.Errorf("%w: %s is gone", ErrStaleLayout, m.Connector)
			}
			if !slices.ContainsFunc(mon.Modes, func(d DisplayMode) bool { return d.ID == m.ModeID }) {
				return fmt.Errorf("%w: %s has no mode %s", ErrStaleLayout, m.Connector, m.ModeID)
			}
		}
	}
	return nil
}

// ErrNoVirtualMonitor means the virtual monitor is not (yet) in the layout.
var ErrNoVirtualMonitor = errors.New("virtual monitor is not in the layout")

// PlanVirtual returns the desk's layout from before the virtual monitor
// existed, with the virtual monitor on connector added to the right of the
// rightmost monitor at the supported scale nearest want. Mutter lays out a
// monitor set it has no saved configuration for from scratch, moving the
// desk's monitors, so the desk part comes from desk, not from now. With
// primary the virtual monitor also becomes the primary one (GNOME's top bar,
// overview and notifications); otherwise the desk keeps its primary.
func PlanVirtual(desk, now DisplayState, connector string, want float64, primary bool) (DisplayLayout, error) {
	layout, err := desk.CurrentLayout()
	if err != nil {
		return DisplayLayout{}, err
	}
	mon, ok := now.monitor(connector)
	if !ok || !slices.ContainsFunc(now.Logical, func(lm LogicalMonitor) bool { return slices.Contains(lm.Connectors, connector) }) {
		return DisplayLayout{}, fmt.Errorf("%w: %s", ErrNoVirtualMonitor, connector)
	}
	mode, ok := mon.current()
	if !ok {
		return DisplayLayout{}, fmt.Errorf("virtual monitor %s has no current mode", connector)
	}
	if primary {
		for i := range layout.Logical {
			layout.Logical[i].Primary = false
		}
	}
	right := rightmost(desk.LogicalRects())
	layout.Logical = append(layout.Logical, LogicalConfig{
		X: right.X + right.W, Y: right.Y,
		Scale:    nearestScale(mode.SupportedScales, want),
		Primary:  primary,
		Monitors: []MonitorAssignment{{Connector: connector, ModeID: mode.ID}},
	})
	return layout, nil
}

// RescaleVirtual returns the layout now with only the virtual monitor on
// connector changed: at the supported scale nearest want, right of the
// rightmost other monitor. The desk's monitors stay as they are now, so a
// change made at the desk during the session survives. The virtual monitor
// comes last. changed is false when the monitor has that scale already, so
// nothing needs applying (Mutter rounds requests to the scales it supports).
func RescaleVirtual(now DisplayState, connector string, want float64) (layout DisplayLayout, changed bool, err error) {
	current, err := now.CurrentLayout()
	if err != nil {
		return DisplayLayout{}, false, err
	}
	i := slices.IndexFunc(current.Logical, func(c LogicalConfig) bool { return c.shows(connector) })
	if i < 0 {
		return DisplayLayout{}, false, fmt.Errorf("%w: %s", ErrNoVirtualMonitor, connector)
	}
	virtual := current.Logical[i]
	if len(virtual.Monitors) != 1 {
		return DisplayLayout{}, false, fmt.Errorf("virtual monitor %s shares its place with another monitor", connector)
	}
	// CurrentLayout found the monitor and its current mode.
	mon, _ := now.monitor(connector)
	mode, _ := mon.current()
	var desk []Rect
	for _, p := range now.placed() {
		if p.connector != connector {
			desk = append(desk, p.rect)
		}
	}
	right := rightmost(desk)
	layout = current.Without(connector)
	virtual.X, virtual.Y = right.X+right.W, right.Y
	scale := nearestScale(mode.SupportedScales, want)
	changed = !sameScale(scale, virtual.Scale)
	virtual.Scale = scale
	layout.Logical = append(layout.Logical, virtual)
	return layout, changed, nil
}

// Without returns the layout without the logical monitor showing connector:
// with a virtual monitor's connector, the desk's own monitors.
func (l DisplayLayout) Without(connector string) DisplayLayout {
	out := DisplayLayout{LayoutMode: l.LayoutMode}
	for _, c := range l.Logical {
		if !c.shows(connector) {
			out.Logical = append(out.Logical, c)
		}
	}
	return out
}

// DeskUnchanged reports whether now lays out every monitor but the virtual
// one on connector as in applied, the layout a session set. If not, someone
// changed the desk's monitors since, and the session must leave them be.
func DeskUnchanged(applied DisplayLayout, now DisplayState, connector string) bool {
	current, err := now.CurrentLayout()
	if err != nil {
		return false
	}
	return current.Without(connector).Equal(applied.Without(connector))
}

func (c LogicalConfig) shows(connector string) bool {
	return slices.ContainsFunc(c.Monitors, func(m MonitorAssignment) bool { return m.Connector == connector })
}

// rightmost is the rectangle reaching furthest right, the first on a tie,
// or the zero Rect without any.
func rightmost(rects []Rect) Rect {
	var right Rect
	for i, r := range rects {
		if i == 0 || r.X+r.W > right.X+right.W {
			right = r
		}
	}
	return right
}

// sameScale compares scales as Mutter reports them, which differ from the
// supported scales it lists only by float rounding.
func sameScale(a, b float64) bool { return math.Abs(a-b) < 1e-3 }

// Equal reports whether two layouts place the same monitors in the same modes
// at the same positions, scales and rotations with the same primary, in any
// order.
func (l DisplayLayout) Equal(o DisplayLayout) bool {
	if l.LayoutMode != o.LayoutMode || len(l.Logical) != len(o.Logical) {
		return false
	}
	key := func(c LogicalConfig) string {
		k := fmt.Sprintf("%d,%d,%.2f,%d,%v", c.X, c.Y, c.Scale, c.Transform, c.Primary)
		for _, m := range c.Monitors {
			k += "|" + m.Connector + "=" + m.ModeID
		}
		return k
	}
	a := make([]string, 0, len(l.Logical))
	b := make([]string, 0, len(o.Logical))
	for i := range l.Logical {
		a = append(a, key(l.Logical[i]))
		b = append(b, key(o.Logical[i]))
	}
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// Connectors returns the connectors of every monitor Mutter knows.
func (s DisplayState) Connectors() []string {
	out := make([]string, 0, len(s.Monitors))
	for _, m := range s.Monitors {
		out = append(out, m.Connector)
	}
	return out
}

// BestFit names the monitor to mirror for a client of width x height:
// configured when it is in the layout, otherwise the monitor whose current
// shape is closest to the client's, the primary one on a tie. It returns ""
// without monitors, which makes Mutter record the primary one.
func (s DisplayState) BestFit(configured string, width, height int) string {
	best, bestDiff, bestPrimary := "", math.Inf(1), false
	want := math.Log(float64(width) / float64(max(height, 1)))
	for _, lm := range s.Logical {
		for _, c := range lm.Connectors {
			if c == configured {
				return c
			}
			mon, ok := s.monitor(c)
			if !ok {
				continue
			}
			mode, ok := mon.current()
			if !ok || mode.Height == 0 {
				continue
			}
			diff := math.Abs(math.Log(float64(mode.Width)/float64(mode.Height)) - want)
			if diff < bestDiff-1e-9 || (math.Abs(diff-bestDiff) <= 1e-9 && lm.Primary && !bestPrimary) {
				best, bestDiff, bestPrimary = c, diff, lm.Primary
			}
		}
	}
	return best
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
