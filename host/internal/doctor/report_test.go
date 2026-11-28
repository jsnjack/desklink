package doctor

import (
	"errors"
	"slices"
	"testing"
	"time"

	"desklink/internal/desktop"
)

// healthy are the facts of a desk where everything works: desklinkd runs as
// its service and a tablet is paired.
func healthy() Facts {
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	return Facts{
		Session: waylandSession, GNOME: gnomeRunning,
		AV1: gpu, HEVC: gpu,
		Uinput: "/dev/uinput", UdevRule: true,
		Running:   true,
		Ports:     []PortState{{control, true}, {media, true}},
		Firewall:  FirewallState{Running: true, Zone: "FedoraWorkstation", Open: []Port{control, media}},
		Wake:      desktop.Wake{Interface: "enp7s0"},
		Autologin: desktop.GDMLogin{Automatic: true, Timed: true, TimedDelay: 10},
		Service:   ServiceState{Load: "loaded", Active: "active", UnitFile: "enabled", Since: now.Add(-73 * time.Hour)},
		Devices:   []Device{{Name: "Galaxy Tab S9", Connected: true}},
		Now:       now,
	}
}

func TestBuildGroupsTheChecks(t *testing.T) {
	r := Build(healthy())
	var titles, names []string
	for _, g := range r.Groups {
		titles = append(titles, g.Title)
		for _, c := range g.Checks {
			names = append(names, c.Name)
		}
	}
	if want := []string{"Desk", "Network", "Staying up", "Devices"}; !slices.Equal(titles, want) {
		t.Errorf("groups %q, want %q", titles, want)
	}
	want := []string{"GNOME on Wayland", "Screen cast", "AV1 encoder", "HEVC 10-bit", "Input",
		"Listening", "Firewall", "Wake-on-LAN", "Automatic login", "Service", "1 paired"}
	if !slices.Equal(names, want) {
		t.Errorf("checks %q, want %q", names, want)
	}
	for _, c := range r.Checks() {
		if c.Status != OK {
			t.Errorf("a healthy desk has %v %s: %s", c.Status, c.Name, c.Detail)
		}
	}
}

func TestSummary(t *testing.T) {
	cases := []struct {
		name    string
		change  func(f *Facts)
		want    string
		blocked bool
	}{
		{"everything works", func(*Facts) {}, "No problems or warnings. DeskLink works.", false},
		{"the design guide's example", func(f *Facts) {
			f.HEVC = noEncoder
			f.Firewall = FirewallState{Running: true, Zone: "public", Open: []Port{control}, Closed: []Port{media}}
			f.Autologin = desktop.GDMLogin{}
		}, "1 problem, 2 warnings. DeskLink works; HEVC is not available.", false},
		{"warnings alone", func(f *Facts) {
			f.Autologin = desktop.GDMLogin{}
			f.Devices = nil
		}, "2 warnings. DeskLink works.", false},
		{"first run, before desklinkd is installed", func(f *Facts) {
			f.Running = false
			f.Ports = []PortState{{control, false}, {media, false}}
			f.Service = ServiceState{Load: "not-found", Active: "inactive"}
		}, "1 problem, 1 warning. DeskLink does not work: desklinkd is not installed as a service.", true},
		{"blocking problems come before the others", func(f *Facts) {
			f.HEVC = noEncoder
			f.UinputErr = errors.New("/dev/uinput: permission denied")
		}, "2 problems. DeskLink does not work: /dev/uinput is not writable.", true},
		{"several blocking problems", func(f *Facts) {
			f.Session, f.SessionErr = desktop.GraphicalSession{}, desktop.ErrNoGraphicalSession
			f.GNOME = desktop.GNOMEServices{}
			f.AV1, f.HEVC = noEncoder, noEncoder
		}, "4 problems. DeskLink does not work: nobody is logged in to GNOME, Mutter's screen cast is missing, " +
			"AV1 is not available and HEVC is not available.", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := healthy()
			c.change(&f)
			r := Build(f)
			if got := r.Summary(); got != c.want {
				t.Errorf("Summary() = %q\nwant        %q", got, c.want)
			}
			if r.Blocked() != c.blocked {
				t.Errorf("Blocked() = %v, want %v", r.Blocked(), c.blocked)
			}
		})
	}
}
