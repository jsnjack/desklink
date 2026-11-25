package viewer

import (
	"testing"

	dlnet "desklink/internal/net"
)

func TestStepScale(t *testing.T) {
	cases := []struct {
		name      string
		current   float64
		direction int
		want      float64
	}{
		{"one step up from 100 %", 1, 1, 1.25},
		{"one step down from 200 %", 2, -1, 1.75},
		{"stops at 200 %", 2, 1, 2},
		{"stops at 100 %", 1, -1, 1},
		{"a scale Mutter rounded counts from the nearest step", 1.2503, 1, 1.5},
		{"no step snaps to the nearest step", 1.3333, 0, 1.25},
		{"a scale above the steps comes down to 200 %", 3, 0, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stepScale(c.current, c.direction); got != c.want {
				t.Errorf("stepScale(%v, %d) = %v, want %v", c.current, c.direction, got, c.want)
			}
		})
	}
}

func TestRequestScale(t *testing.T) {
	cases := []struct {
		name           string
		saved, monitor float64
		want           float64
	}{
		{"a saved choice wins over the monitor", 1.5, 2, 1.5},
		{"a monitor at 100 % without a saved choice", 0, 1, 1},
		{"a HiDPI monitor at 200 %", 0, 2, 2},
		{"a fractionally scaled monitor", 0, 1.25, 1.25},
		{"a fractional scale between steps is kept, so text has the laptop's size", 0, 1.6666, 1.6666},
		{"a monitor above 200 % asks for 200 %", 0, 3, 2},
		{"an unknown monitor asks for 100 %, not the host's 200 %", 0, 0, 1},
		{"a saved scale outside the steps is brought within them", 5, 1, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := requestScale(c.saved, c.monitor); got != c.want {
				t.Errorf("requestScale(%v, %v) = %v, want %v", c.saved, c.monitor, got, c.want)
			}
		})
	}
}

func TestMonitorTooltip(t *testing.T) {
	cases := []struct {
		m    dlnet.MonitorInfo
		want string
	}{
		{dlnet.MonitorInfo{Connector: "DP-5", Width: 2560, Height: 1440, Refresh: 59.951, Primary: true}, "2560×1440 at 60 Hz, primary"},
		{dlnet.MonitorInfo{Connector: "eDP-1", Width: 2560, Height: 1600, Refresh: 120}, "2560×1600 at 120 Hz"},
	}
	for _, c := range cases {
		if got := monitorTooltip(c.m); got != c.want {
			t.Errorf("monitorTooltip(%+v) = %q, want %q", c.m, got, c.want)
		}
	}
}

func TestControlsFor(t *testing.T) {
	on, off := true, false
	cases := []struct {
		name string
		info dlnet.SessionInfo
		want screenControls
	}{
		{"an older host that does not say what it shows offers no screen settings", dlnet.SessionInfo{}, screenControls{}},
		{"a mirrored monitor has no scale", dlnet.SessionInfo{Screen: dlnet.ScreenMirror}, screenControls{screen: true}},
		{"the own screen has a scale", dlnet.SessionInfo{Screen: dlnet.ScreenOwn, Scale: 1.5}, screenControls{screen: true, scale: true}},
		{
			"the switches appear only when the host includes them",
			dlnet.SessionInfo{Screen: dlnet.ScreenOwn, Scale: 1.5, Primary: &off, DeskOff: &on},
			screenControls{screen: true, scale: true, primary: true, deskOff: true},
		},
		{
			"with the desk off the host offers no primary switch",
			dlnet.SessionInfo{Screen: dlnet.ScreenOwn, Scale: 2, DeskOff: &on},
			screenControls{screen: true, scale: true, deskOff: true},
		},
		{"an unknown screen value offers no screen choice", dlnet.SessionInfo{Screen: "other"}, screenControls{}},
		{
			"two monitors offer a choice of the one to mirror",
			dlnet.SessionInfo{Screen: dlnet.ScreenMirror, Mirroring: "eDP-1", Monitors: []dlnet.MonitorInfo{{Connector: "DP-5"}, {Connector: "eDP-1"}}},
			screenControls{screen: true, monitors: true},
		},
		{
			"a single monitor offers no choice",
			dlnet.SessionInfo{Screen: dlnet.ScreenMirror, Mirroring: "eDP-1", Monitors: []dlnet.MonitorInfo{{Connector: "eDP-1"}}},
			screenControls{screen: true},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := controlsFor(c.info); got != c.want {
				t.Errorf("controlsFor(%+v) = %+v, want %+v", c.info, got, c.want)
			}
		})
	}
}
