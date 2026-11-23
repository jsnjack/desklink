package desktop

import (
	"errors"
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

func TestPlanResolution(t *testing.T) {
	native := mode("2560x1440@144", 2560, 1440, 143.97, true)
	cases := []struct {
		name        string
		state       DisplayState
		w, h, fps   int
		wantMode    string
		wantScale   float64
		wantChanged bool
		wantErr     error
	}{
		{
			name:  "highest refresh not above the client rate",
			state: singleMonitor(native, mode("a", 2560, 1600, 59.97, false), mode("b", 2560, 1600, 119.98, false), mode("c", 2560, 1600, 165, false)),
			w:     2560, h: 1600, fps: 120,
			wantMode: "b", wantScale: 1, wantChanged: true,
		},
		{
			name:  "lowest refresh when all are above the client rate",
			state: singleMonitor(native, mode("a", 1920, 1200, 144, false), mode("b", 1920, 1200, 100, false)),
			w:     1920, h: 1200, fps: 60,
			wantMode: "b", wantScale: 1, wantChanged: true,
		},
		{
			name:  "no mode of that size",
			state: singleMonitor(native),
			w:     1920, h: 1200, fps: 60,
			wantErr: ErrNoMatchingMode,
		},
		{
			name:  "already at the requested mode",
			state: singleMonitor(mode("cur", 2560, 1600, 120, true)),
			w:     2560, h: 1600, fps: 120,
			wantMode: "cur", wantScale: 1, wantChanged: false,
		},
		{
			name: "unsupported scale falls back to the nearest supported one",
			state: func() DisplayState {
				s := singleMonitor(native, mode("n", 1920, 1200, 60, false, 1, 1.25, 2))
				s.Logical[0].Scale = 1.5
				return s
			}(),
			w: 1920, h: 1200, fps: 60,
			wantMode: "n", wantScale: 1.25, wantChanged: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plan, err := PlanResolution(c.state, "", c.w, c.h, c.fps)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("err %v, want %v", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if plan.Mode.ID != c.wantMode || plan.Changed != c.wantChanged {
				t.Errorf("mode %s changed %v, want %s %v", plan.Mode.ID, plan.Changed, c.wantMode, c.wantChanged)
			}
			lc := plan.Layout.Logical[0]
			if lc.Scale != c.wantScale || lc.Monitors[0].ModeID != c.wantMode {
				t.Errorf("layout %+v", lc)
			}
		})
	}
}

func TestPlanResolutionMovesNeighbours(t *testing.T) {
	st := DisplayState{
		LayoutMode: 1,
		Monitors: []Monitor{
			{Connector: "DP-1", Modes: []DisplayMode{mode("big", 2560, 1440, 60, true), mode("small", 1920, 1200, 60, false)}},
			{Connector: "HDMI-1", Modes: []DisplayMode{mode("side", 1920, 1080, 60, true)}},
		},
		Logical: []LogicalMonitor{
			{X: 0, Y: 0, Scale: 1, Primary: true, Connectors: []string{"DP-1"}},
			{X: 2560, Y: 0, Scale: 1, Connectors: []string{"HDMI-1"}},
		},
	}
	plan, err := PlanResolution(st, "", 1920, 1200, 60)
	if err != nil {
		t.Fatal(err)
	}
	if x := plan.Layout.Logical[1].X; x != 1920 {
		t.Errorf("right-hand monitor at x=%d, want 1920", x)
	}
	if id := plan.Layout.Logical[1].Monitors[0].ModeID; id != "side" {
		t.Errorf("right-hand monitor mode %s changed", id)
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

func TestPlanResolutionTargetsTheCapturedMonitor(t *testing.T) {
	st := DisplayState{
		LayoutMode: 1,
		Monitors: []Monitor{
			{Connector: "DP-1", Modes: []DisplayMode{mode("p", 2560, 1440, 60, true), mode("p2", 1920, 1200, 60, false)}},
			{Connector: "HDMI-1", Modes: []DisplayMode{mode("s", 2560, 1440, 60, true), mode("s2", 1920, 1200, 60, false)}},
		},
		Logical: []LogicalMonitor{
			{X: 0, Y: 0, Scale: 1, Primary: true, Connectors: []string{"DP-1"}},
			{X: 2560, Y: 0, Scale: 1, Connectors: []string{"HDMI-1"}},
		},
	}
	plan, err := PlanResolution(st, "HDMI-1", 1920, 1200, 60)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Connector != "HDMI-1" || plan.Layout.Logical[1].Monitors[0].ModeID != "s2" || plan.Layout.Logical[0].Monitors[0].ModeID != "p" {
		t.Errorf("plan %+v", plan)
	}
	if c, ok := st.ConnectorAt(2560, 0); !ok || c != "HDMI-1" {
		t.Errorf("ConnectorAt = %q %v", c, ok)
	}
	if r, ok := st.MonitorRect("HDMI-1"); !ok || r != (Rect{X: 2560, W: 2560, H: 1440}) {
		t.Errorf("MonitorRect = %+v %v", r, ok)
	}
}

func TestMonitorFor(t *testing.T) {
	st := DisplayState{
		Logical: []LogicalMonitor{
			{X: 0, Y: 0, Scale: 1, Connectors: []string{"DP-1"}},
			{X: 2560, Y: 0, Scale: 1, Primary: true, Connectors: []string{"HDMI-1"}},
		},
	}
	tests := []struct{ name, configured, want string }{
		{"empty means the primary monitor", "", "HDMI-1"},
		{"a connected monitor is used", "DP-1", "DP-1"},
		{"an unplugged monitor falls back to the primary", "eDP-1", "HDMI-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := st.MonitorFor(tt.configured); got != tt.want {
				t.Errorf("MonitorFor(%q) = %q, want %q", tt.configured, got, tt.want)
			}
		})
	}
	if got := (DisplayState{}).MonitorFor("DP-1"); got != "" {
		t.Errorf("MonitorFor without monitors = %q, want empty", got)
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
