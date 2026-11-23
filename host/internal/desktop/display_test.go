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
