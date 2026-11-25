package desktop

import (
	"errors"
	"slices"
	"testing"
)

func mode(id string, w, h int32, hz float64, current bool, scales ...float64) DisplayMode {
	if len(scales) == 0 {
		scales = []float64{1, 2}
	}
	return DisplayMode{ID: id, Width: w, Height: h, Refresh: hz, PreferredScale: 1, SupportedScales: scales, Current: current}
}

func singleMonitor(modes ...DisplayMode) DisplayState {
	return DisplayState{
		Serial:     7,
		LayoutMode: 1,
		Monitors:   []Monitor{{Connector: "DP-1", Modes: modes}},
		Logical:    []LogicalMonitor{{Scale: 1, Primary: true, Connectors: []string{"DP-1"}}},
	}
}

func TestCurrentLayout(t *testing.T) {
	st := singleMonitor(mode("a", 2560, 1440, 60, false), mode("b", 2560, 1440, 144, true))
	st.Logical[0].Scale = 1.25
	l, err := st.CurrentLayout()
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Logical) != 1 || l.Logical[0].Monitors[0].ModeID != "b" || l.Logical[0].Scale != 1.25 || !l.Logical[0].Primary {
		t.Errorf("layout %+v", l)
	}
	t.Run("unknown connector", func(t *testing.T) {
		bad := st
		bad.Logical = []LogicalMonitor{{Connectors: []string{"eDP-9"}}}
		if _, err := bad.CurrentLayout(); err == nil {
			t.Error("accepted a logical monitor without a physical one")
		}
	})
}

func TestStage(t *testing.T) {
	st := DisplayState{
		LayoutMode: 1,
		Monitors: []Monitor{
			{Connector: "DP-1", Modes: []DisplayMode{mode("a", 2560, 1440, 60, true)}},
			{Connector: "HDMI-1", Modes: []DisplayMode{mode("b", 3840, 2160, 60, true)}},
		},
		Logical: []LogicalMonitor{
			{X: 0, Y: 360, Scale: 1, Primary: true, Connectors: []string{"DP-1"}},
			{X: 2560, Y: 0, Scale: 2, Connectors: []string{"HDMI-1"}},
		},
	}
	if got, want := st.Stage(), (Rect{X: 0, Y: 0, W: 4480, H: 1800}); got != want {
		t.Errorf("stage %+v, want %+v", got, want)
	}
}

func TestMonitorPlacement(t *testing.T) {
	st := DisplayState{
		LayoutMode: 1,
		Monitors: []Monitor{
			{Connector: "DP-1", Modes: []DisplayMode{mode("p", 2560, 1440, 60, true)}},
			{Connector: "HDMI-1", Modes: []DisplayMode{mode("s", 2560, 1440, 60, true)}},
		},
		Logical: []LogicalMonitor{
			{X: 0, Y: 0, Scale: 1, Primary: true, Connectors: []string{"DP-1"}},
			{X: 2560, Y: 0, Scale: 1, Connectors: []string{"HDMI-1"}},
		},
	}
	if c, ok := st.ConnectorAt(2560, 0); !ok || c != "HDMI-1" {
		t.Errorf("ConnectorAt = %q %v", c, ok)
	}
	if r, ok := st.MonitorRect("HDMI-1"); !ok || r != (Rect{X: 2560, W: 2560, H: 1440}) {
		t.Errorf("MonitorRect = %+v %v", r, ok)
	}
}

func TestBestFit(t *testing.T) {
	// This laptop: a 27-inch 16:9 primary and the 16:10 built-in panel.
	st := DisplayState{
		LayoutMode: 1,
		Monitors: []Monitor{
			{Connector: "DP-5", Modes: []DisplayMode{mode("d", 2560, 1440, 120, true)}},
			{Connector: "eDP-1", Modes: []DisplayMode{mode("e", 2560, 1600, 120, true)}},
		},
		Logical: []LogicalMonitor{
			{X: 1920, Y: 0, Scale: 1, Primary: true, Connectors: []string{"DP-5"}},
			{X: 0, Y: 240, Scale: 1.3333333, Connectors: []string{"eDP-1"}},
		},
	}
	tests := []struct {
		name       string
		configured string
		w, h       int
		want       string
	}{
		{"a 16:10 tablet gets the 16:10 panel", "", 2560, 1600, "eDP-1"},
		{"a 16:9 client gets the 16:9 monitor", "", 1920, 1080, "DP-5"},
		{"a connected configured monitor wins", "DP-5", 2560, 1600, "DP-5"},
		{"an unplugged configured monitor is ignored", "HDMI-1", 2560, 1600, "eDP-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := st.BestFit(tt.configured, tt.w, tt.h); got != tt.want {
				t.Errorf("BestFit(%q, %dx%d) = %q, want %q", tt.configured, tt.w, tt.h, got, tt.want)
			}
		})
	}
	same := DisplayState{
		Monitors: []Monitor{
			{Connector: "A", Modes: []DisplayMode{mode("a", 1920, 1080, 60, true)}},
			{Connector: "B", Modes: []DisplayMode{mode("b", 1920, 1080, 60, true)}},
		},
		Logical: []LogicalMonitor{{X: 0, Connectors: []string{"A"}}, {X: 1920, Primary: true, Connectors: []string{"B"}}},
	}
	if got := same.BestFit("", 2560, 1600); got != "B" {
		t.Errorf("on a tie the primary wins: got %q", got)
	}
	if got := (DisplayState{}).BestFit("", 2560, 1600); got != "" {
		t.Errorf("without monitors = %q, want empty", got)
	}
}

func TestLayoutValidFor(t *testing.T) {
	st := singleMonitor(mode("a", 2560, 1440, 60, true), mode("b", 1920, 1200, 60, false))
	good, err := st.CurrentLayout()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		layout DisplayLayout
		stale  bool
	}{
		{"current layout", good, false},
		{"mode removed", DisplayLayout{Logical: []LogicalConfig{{Monitors: []MonitorAssignment{{Connector: "DP-1", ModeID: "gone"}}}}}, true},
		{"monitor unplugged", DisplayLayout{Logical: []LogicalConfig{{Monitors: []MonitorAssignment{{Connector: "HDMI-2", ModeID: "a"}}}}}, true},
		{"empty", DisplayLayout{}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.layout.ValidFor(st)
			if errors.Is(err, ErrStaleLayout) != c.stale {
				t.Errorf("err %v, stale want %v", err, c.stale)
			}
		})
	}
}

func TestLogicalSize(t *testing.T) {
	m := mode("m", 2560, 1600, 60, true)
	cases := []struct {
		name       string
		scale      float64
		transform  uint32
		layoutMode uint32
		w, h       int32
	}{
		{"logical layout divides by the scale", 2, 0, 1, 1280, 800},
		{"physical layout ignores the scale", 2, 0, layoutPhysical, 2560, 1600},
		{"rotation swaps the axes", 1, 1, 1, 1600, 2560},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, h := logicalSize(m, c.scale, c.transform, c.layoutMode)
			if w != c.w || h != c.h {
				t.Errorf("got %dx%d, want %dx%d", w, h, c.w, c.h)
			}
		})
	}
}

func TestPlanVirtual(t *testing.T) {
	deskMonitors := []Monitor{
		{Connector: "DP-5", Modes: []DisplayMode{mode("d", 2560, 1440, 120, true)}},
		{Connector: "eDP-1", Modes: []DisplayMode{mode("e", 2560, 1600, 120, true)}},
	}
	desk := DisplayState{
		LayoutMode: 1,
		Monitors:   deskMonitors,
		Logical: []LogicalMonitor{
			{X: 1920, Y: 0, Scale: 1, Primary: true, Connectors: []string{"DP-5"}},
			{X: 0, Y: 240, Scale: 1.3333333, Connectors: []string{"eDP-1"}},
		},
	}
	// Mutter's own layout once the virtual monitor appeared: desk monitors moved.
	now := DisplayState{
		LayoutMode: 1,
		Monitors:   append(slices.Clone(deskMonitors), Monitor{Connector: "Meta-0", Modes: []DisplayMode{mode("v", 2560, 1600, 120, true, 1, 1.25, 1.6666666, 2, 2.5)}}),
		Logical: []LogicalMonitor{
			{X: 0, Y: 0, Scale: 1, Primary: true, Connectors: []string{"DP-5"}},
			{X: 2560, Y: 0, Scale: 1.3333333, Connectors: []string{"eDP-1"}},
			{X: 4480, Y: 0, Scale: 1, Connectors: []string{"Meta-0"}},
		},
	}
	got, err := PlanVirtual(desk, now, "Meta-0", 2, false)
	if err != nil {
		t.Fatal(err)
	}
	deskLayout, _ := desk.CurrentLayout()
	if !(DisplayLayout{LayoutMode: 1, Logical: got.Logical[:2]}).Equal(deskLayout) {
		t.Errorf("desk monitors must be where they were before: %+v", got.Logical[:2])
	}
	v := got.Logical[2]
	if v.X != 4480 || v.Y != 0 || v.Scale != 2 || v.Primary || v.Monitors[0].ModeID != "v" {
		t.Errorf("virtual monitor = %+v, want right of DP-5 (x 4480), scale 2, not primary", v)
	}
	remote, err := PlanVirtual(desk, now, "Meta-0", 2, true)
	if err != nil {
		t.Fatal(err)
	}
	if remote.Logical[0].Primary || !remote.Logical[2].Primary {
		t.Error("with primary the virtual monitor must be the only primary")
	}
	if _, err := PlanVirtual(desk, now, "Meta-1", 2, true); !errors.Is(err, ErrNoVirtualMonitor) {
		t.Errorf("unknown connector: err = %v, want ErrNoVirtualMonitor", err)
	}
}

func TestLayoutEqual(t *testing.T) {
	a := DisplayLayout{LayoutMode: 1, Logical: []LogicalConfig{
		{X: 1920, Y: 0, Scale: 1, Primary: true, Monitors: []MonitorAssignment{{Connector: "DP-5", ModeID: "d"}}},
		{X: 0, Y: 240, Scale: 1.3333333730697632, Monitors: []MonitorAssignment{{Connector: "eDP-1", ModeID: "e"}}},
	}}
	b := DisplayLayout{LayoutMode: 1, Logical: []LogicalConfig{a.Logical[1], a.Logical[0]}}
	b.Logical[0].Scale = 1.3333333
	if !a.Equal(b) {
		t.Error("the same layout in another order or with float noise must be equal")
	}
	c := DisplayLayout{LayoutMode: 1, Logical: []LogicalConfig{a.Logical[0], a.Logical[1]}}
	c.Logical[1].X = 2560
	if a.Equal(c) {
		t.Error("a moved monitor must not be equal")
	}
}

// sessionMonitors are this laptop's two monitors and a virtual one.
func sessionMonitors() []Monitor {
	return []Monitor{
		{Connector: "DP-5", Modes: []DisplayMode{mode("d", 2560, 1440, 120, true)}},
		{Connector: "eDP-1", Modes: []DisplayMode{mode("e", 2560, 1600, 120, true)}},
		{Connector: "Meta-0", Modes: []DisplayMode{mode("v", 2560, 1600, 120, true, 1, 1.25, 1.6666666, 2, 2.5)}},
	}
}

// stateOf is the DisplayState Mutter reports once it applied layout.
func stateOf(layout DisplayLayout) DisplayState {
	st := DisplayState{Serial: 9, LayoutMode: layout.LayoutMode, Monitors: sessionMonitors()}
	for _, c := range layout.Logical {
		lm := LogicalMonitor{X: c.X, Y: c.Y, Scale: c.Scale, Transform: c.Transform, Primary: c.Primary}
		for _, m := range c.Monitors {
			lm.Connectors = append(lm.Connectors, m.Connector)
		}
		st.Logical = append(st.Logical, lm)
	}
	return st
}

func logical(connector, modeID string, x, y int32, scale float64, primary bool) LogicalConfig {
	return LogicalConfig{X: x, Y: y, Scale: scale, Primary: primary, Monitors: []MonitorAssignment{{Connector: connector, ModeID: modeID}}}
}

// arranged is the layout a session sets: the desk as before the session and
// the virtual screen at 200 % to its right.
func arranged() DisplayLayout {
	return DisplayLayout{LayoutMode: 1, Logical: []LogicalConfig{
		logical("DP-5", "d", 1920, 0, 1, true),
		logical("eDP-1", "e", 0, 240, 1.3333333, false),
		logical("Meta-0", "v", 4480, 0, 2, false),
	}}
}

func TestRescaleVirtual(t *testing.T) {
	// Someone at the desk moved the panel right of DP-5 during the session.
	moved := DisplayLayout{LayoutMode: 1, Logical: []LogicalConfig{
		logical("DP-5", "d", 0, 0, 1, true),
		logical("eDP-1", "e", 2560, 0, 1.3333333, false),
		logical("Meta-0", "v", 4480, 0, 2, false),
	}}
	cases := []struct {
		name        string
		now         DisplayLayout
		want        float64
		changed     bool
		x, y        int32
		virtualMain bool
	}{
		{"a new scale keeps the desk as arranged", arranged(), 1.25, true, 4480, 0, false},
		{"a desk changed during the session stays changed", moved, 2.5, true, 4480, 0, false},
		{"a scale that rounds to the current one changes nothing", arranged(), 2.1, false, 4480, 0, false},
		{"Mutter's float noise is the same scale", withScale(arranged(), "Meta-0", 1.6666666269302368), 1.7, false, 4480, 0, false},
		{"a primary virtual screen stays primary", primaryVirtual(), 1.25, true, 4480, 0, true},
		{"a virtual screen moved at the desk goes back right of the desk", withX(arranged(), "Meta-0", -1280), 1.25, true, 4480, 0, false},
		{"desk monitors the session turned off stay off", deskOff(), 1.25, true, 0, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			now := stateOf(c.now)
			got, changed, err := RescaleVirtual(now, "Meta-0", c.want)
			if err != nil {
				t.Fatal(err)
			}
			if changed != c.changed {
				t.Errorf("changed = %v, want %v", changed, c.changed)
			}
			if !got.Without("Meta-0").Equal(c.now.Without("Meta-0")) {
				t.Errorf("desk monitors = %+v, want them as they are now: %+v", got.Without("Meta-0").Logical, c.now.Without("Meta-0").Logical)
			}
			v := got.Logical[len(got.Logical)-1]
			if !v.shows("Meta-0") || v.X != c.x || v.Y != c.y || v.Primary != c.virtualMain || v.Monitors[0].ModeID != "v" {
				t.Errorf("virtual monitor = %+v, want at %d,%d, primary %v", v, c.x, c.y, c.virtualMain)
			}
			if wantScale := nearestScale(sessionMonitors()[2].Modes[0].SupportedScales, c.want); !sameScale(v.Scale, wantScale) {
				t.Errorf("virtual scale %v, want %v", v.Scale, wantScale)
			}
		})
	}
	t.Run("an unknown connector is an error", func(t *testing.T) {
		if _, _, err := RescaleVirtual(stateOf(arranged()), "Meta-1", 2); !errors.Is(err, ErrNoVirtualMonitor) {
			t.Errorf("err = %v, want ErrNoVirtualMonitor", err)
		}
	})
}

func withScale(l DisplayLayout, connector string, scale float64) DisplayLayout {
	l.Logical = slices.Clone(l.Logical)
	for i := range l.Logical {
		if l.Logical[i].shows(connector) {
			l.Logical[i].Scale = scale
		}
	}
	return l
}

func withX(l DisplayLayout, connector string, x int32) DisplayLayout {
	l.Logical = slices.Clone(l.Logical)
	for i := range l.Logical {
		if l.Logical[i].shows(connector) {
			l.Logical[i].X = x
		}
	}
	return l
}

func primaryVirtual() DisplayLayout {
	l := arranged()
	l.Logical[0].Primary = false
	l.Logical[2].Primary = true
	return l
}

// deskOff is the layout a session sets when nobody is at the desk: the
// virtual screen alone, which turns the desk's monitors off.
func deskOff() DisplayLayout {
	return DisplayLayout{LayoutMode: 1, Logical: []LogicalConfig{logical("Meta-0", "v", 0, 0, 2, true)}}
}

func TestDeskUnchanged(t *testing.T) {
	applied := arranged()
	movedPanel := withX(applied, "eDP-1", -2560)
	otherPrimary := arranged()
	otherPrimary.Logical[0].Primary, otherPrimary.Logical[1].Primary = false, true
	unreadable := stateOf(applied)
	unreadable.Logical = append(unreadable.Logical, LogicalMonitor{Connectors: []string{"HDMI-9"}})
	panelOn := DisplayLayout{LayoutMode: 1, Logical: []LogicalConfig{
		logical("eDP-1", "e", 0, 0, 1.3333333, false),
		logical("Meta-0", "v", 1920, 0, 2, true),
	}}
	cases := []struct {
		name    string
		applied DisplayLayout
		now     DisplayState
		want    bool
	}{
		{"the desk as the session arranged it", applied, stateOf(applied), true},
		{"only the virtual screen's scale changed", applied, stateOf(withScale(applied, "Meta-0", 1.25)), true},
		{"a desk monitor moved", applied, stateOf(movedPanel), false},
		{"another desk monitor became primary", applied, stateOf(otherPrimary), false},
		{"a layout that cannot be read", applied, unreadable, false},
		{"the desk's monitors the session turned off are still off", deskOff(), stateOf(withScale(deskOff(), "Meta-0", 1.25)), true},
		{"a desk monitor was turned on during the session", deskOff(), stateOf(panelOn), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DeskUnchanged(c.applied, c.now, "Meta-0"); got != c.want {
				t.Errorf("DeskUnchanged = %v, want %v", got, c.want)
			}
		})
	}
}

func TestOnly(t *testing.T) {
	cases := []struct {
		name   string
		layout DisplayLayout
		err    error
	}{
		{"the desk and a virtual screen right of it", arranged(), nil},
		{"a primary virtual screen", primaryVirtual(), nil},
		{"the virtual screen alone already", deskOff(), nil},
		{"a layout without the virtual screen", arranged().Without("Meta-0"), ErrNoVirtualMonitor},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.layout.Only("Meta-0")
			if !errors.Is(err, c.err) {
				t.Fatalf("err = %v, want %v", err, c.err)
			}
			if c.err != nil {
				return
			}
			// Mutter accepts a layout only if it starts at 0,0 and has one primary.
			want := DisplayLayout{LayoutMode: 1, Logical: []LogicalConfig{logical("Meta-0", "v", 0, 0, 2, true)}}
			if !got.Equal(want) {
				t.Errorf("Only = %+v, want the virtual screen alone at the origin, primary, at its scale: %+v", got.Logical, want.Logical)
			}
		})
	}
	t.Run("planned for a locked desk", func(t *testing.T) {
		desk := stateOf(arranged().Without("Meta-0"))
		now := stateOf(withX(withScale(arranged(), "Meta-0", 1), "eDP-1", 2560))
		planned, err := PlanVirtual(desk, now, "Meta-0", 1.7, true)
		if err != nil {
			t.Fatal(err)
		}
		got, err := planned.Only("Meta-0")
		if err != nil {
			t.Fatal(err)
		}
		if want := withScale(deskOff(), "Meta-0", 1.6666666); !got.Equal(want) {
			t.Errorf("layout %+v, want the virtual screen alone at the supported scale nearest 1.7: %+v", got.Logical, want.Logical)
		}
	})
}

func TestDeskOff(t *testing.T) {
	cases := []struct {
		name string
		now  DisplayState
		want bool
	}{
		{"only the virtual screen is on", stateOf(deskOff()), true},
		{"the desk and the virtual screen are on", stateOf(arranged()), false},
		{"only the desk is on", stateOf(arranged().Without("Meta-0")), false},
		{"no monitor is on", stateOf(DisplayLayout{LayoutMode: 1}), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.now.DeskOff("Meta-0"); got != c.want {
				t.Errorf("DeskOff = %v, want %v", got, c.want)
			}
		})
	}
}

func TestMovePrimary(t *testing.T) {
	// Someone at the desk unplugged DP-5, the desk's primary before the session.
	unplugged := DisplayLayout{LayoutMode: 1, Logical: []LogicalConfig{
		logical("eDP-1", "e", 0, 240, 1.3333333, false),
		logical("Meta-0", "v", 1920, 0, 2, true),
	}}
	cases := []struct {
		name    string
		now     DisplayLayout
		primary bool
		want    DisplayLayout
		changed bool
	}{
		{"the client's screen becomes primary", arranged(), true, primaryVirtual(), true},
		{"the primary goes back to the desk's primary from before", primaryVirtual(), false, arranged(), true},
		{"without that monitor, the first desk monitor gets it", unplugged, false, DisplayLayout{LayoutMode: 1, Logical: []LogicalConfig{
			logical("eDP-1", "e", 0, 240, 1.3333333, true),
			logical("Meta-0", "v", 1920, 0, 2, false),
		}}, true},
		{"a primary client screen stays as it is", primaryVirtual(), true, primaryVirtual(), false},
		{"a desk that has the primary keeps it", arranged(), false, arranged(), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, changed, err := MovePrimary(stateOf(c.now), "Meta-0", c.primary, []string{"DP-5"})
			if err != nil {
				t.Fatal(err)
			}
			if changed != c.changed {
				t.Errorf("changed = %v, want %v", changed, c.changed)
			}
			if !got.Equal(c.want) {
				t.Errorf("layout = %+v, want %+v", got.Logical, c.want.Logical)
			}
			if !got.Logical[len(got.Logical)-1].shows("Meta-0") {
				t.Errorf("the virtual monitor is not last: %+v", got.Logical)
			}
		})
	}
	t.Run("with the desk's monitors off the client's screen stays primary", func(t *testing.T) {
		if _, _, err := MovePrimary(stateOf(deskOff()), "Meta-0", false, []string{"DP-5"}); !errors.Is(err, ErrDeskOff) {
			t.Errorf("err = %v, want ErrDeskOff", err)
		}
	})
	t.Run("an unknown connector is an error", func(t *testing.T) {
		if _, _, err := MovePrimary(stateOf(arranged()), "Meta-1", true, nil); !errors.Is(err, ErrNoVirtualMonitor) {
			t.Errorf("err = %v, want ErrNoVirtualMonitor", err)
		}
	})
}

func TestPrimaryConnectors(t *testing.T) {
	if got := stateOf(arranged()).PrimaryConnectors(); !slices.Equal(got, []string{"DP-5"}) {
		t.Errorf("PrimaryConnectors = %v, want [DP-5]", got)
	}
}

func TestActiveMonitors(t *testing.T) {
	cases := []struct {
		name   string
		layout DisplayLayout
		want   []ActiveMonitor
	}{
		{
			"every monitor in the layout, with its mode and the primary marked",
			arranged(),
			[]ActiveMonitor{
				{Connector: "DP-5", Width: 2560, Height: 1440, Refresh: 120, Primary: true},
				{Connector: "eDP-1", Width: 2560, Height: 1600, Refresh: 120},
				{Connector: "Meta-0", Width: 2560, Height: 1600, Refresh: 120},
			},
		},
		{
			"monitors that are off are left out",
			deskOff(),
			[]ActiveMonitor{{Connector: "Meta-0", Width: 2560, Height: 1600, Refresh: 120, Primary: true}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stateOf(c.layout).ActiveMonitors(); !slices.Equal(got, c.want) {
				t.Errorf("ActiveMonitors = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestMonitorOn(t *testing.T) {
	st := stateOf(deskOff())
	cases := []struct {
		connector string
		want      bool
	}{
		{"Meta-0", true},
		{"DP-5", false},
		{"HDMI-1", false},
		{"", false},
	}
	for _, c := range cases {
		if got := st.MonitorOn(c.connector); got != c.want {
			t.Errorf("MonitorOn(%q) = %v, want %v", c.connector, got, c.want)
		}
	}
}
