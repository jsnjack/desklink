package doctor

import (
	"fmt"
	"time"

	"desklink/internal/desktop"
	"desklink/internal/video"
)

// Facts are what the checks judge. desklinkd doctor reads them from the
// system; tests make them up. Each Err field says why the fact before it
// could not be read.
type Facts struct {
	Session    desktop.GraphicalSession
	SessionErr error

	GNOME    desktop.GNOMEServices
	GNOMEErr error

	AV1, HEVC video.EncoderInfo

	// Uinput is the uinput node's path, UinputErr what checking write
	// access to it returned, and UdevRule whether DeskLink's rule is in
	// place.
	Uinput    string
	UinputErr error
	UdevRule  bool

	// Running is set when desklinkd answers on its local socket.
	Running  bool
	Ports    []PortState
	PortsErr error

	Firewall    FirewallState
	FirewallErr error

	Wake    desktop.Wake
	WakeErr error

	Autologin    desktop.GDMLogin
	AutologinErr error

	Service    ServiceState
	ServiceErr error

	Devices    []Device
	DevicesErr error

	Now time.Time
}

// Group is a titled list of checks.
type Group struct {
	Title  string
	Checks []Check
}

// Report is everything desklinkd doctor checks, grouped.
type Report struct {
	Groups []Group
}

// Build judges facts.
func Build(f Facts) Report {
	return Report{Groups: []Group{
		{"Desk", []Check{
			GNOME(f.Session, f.SessionErr, f.GNOME, f.GNOMEErr),
			ScreenCast(f.GNOME, f.GNOMEErr),
			AV1Encoder(f.AV1, f.HEVC),
			HEVCEncoder(f.AV1, f.HEVC),
			Input(f.Uinput, f.UinputErr, f.UdevRule),
		}},
		{"Network", []Check{
			Listening(f.Running, f.Ports, f.PortsErr),
			Firewall(f.Firewall, f.FirewallErr),
			WakeOnLAN(f.Wake, f.WakeErr),
		}},
		{"Staying up", []Check{
			Autologin(f.Autologin, f.AutologinErr),
			Service(f.Service, f.ServiceErr, f.Running, f.Now),
		}},
		{"Devices", []Check{
			Devices(f.Devices, f.DevicesErr, f.Now),
		}},
	}}
}

// Checks returns every check in order.
func (r Report) Checks() []Check {
	var all []Check
	for _, g := range r.Groups {
		all = append(all, g.Checks...)
	}
	return all
}

// Blocked reports whether a problem keeps DeskLink from working at all.
func (r Report) Blocked() bool {
	for _, c := range r.Checks() {
		if c.Blocks {
			return true
		}
	}
	return false
}

// Summary sums the report up in one sentence: how many problems and
// warnings, whether DeskLink works, and what is missing: the problems that
// block it, or else those that do not.
func (r Report) Summary() string {
	var problems, warnings int
	var blocking, other []string
	for _, c := range r.Checks() {
		switch {
		case c.Status == Warning:
			warnings++
		case c.Status == Problem && c.Blocks:
			problems++
			blocking = append(blocking, c.Missing)
		case c.Status == Problem:
			problems++
			other = append(other, c.Missing)
		}
	}
	var counts string
	switch {
	case problems == 0 && warnings == 0:
		counts = "No problems or warnings."
	case warnings == 0:
		counts = count(problems, "problem") + "."
	case problems == 0:
		counts = count(warnings, "warning") + "."
	default:
		counts = count(problems, "problem") + ", " + count(warnings, "warning") + "."
	}
	switch {
	case len(blocking) > 0:
		return counts + " DeskLink does not work: " + joinAnd(blocking) + "."
	case len(other) > 0:
		return counts + " DeskLink works; " + joinAnd(other) + "."
	}
	return counts + " DeskLink works."
}

func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
