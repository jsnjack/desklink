// Package doctor judges whether this computer can serve DeskLink. Each check
// turns facts read from the system into a line marked ✓, ! or ✗, with the
// command that fixes what it found: desklinkd doctor shows them all, with a
// sentence that sums them up, and desklinkd pair shows four.
package doctor

import (
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"time"

	"desklink/internal/desktop"
	"desklink/internal/video"
)

// Status is how a check came out.
type Status int

const (
	// OK means nothing is missing.
	OK Status = iota
	// Warning means DeskLink works, but something it can use is missing.
	Warning
	// Problem means something is broken or missing.
	Problem
)

// Symbol is the mark that starts the check's line.
func (s Status) Symbol() string {
	switch s {
	case Warning:
		return "!"
	case Problem:
		return "✗"
	}
	return "✓"
}

// Check is one checked thing: what was found and, when something is
// missing, the command that fixes it.
type Check struct {
	Status Status
	// Name labels the line, such as "Wake-on-LAN".
	Name string
	// Detail is what the check found.
	Detail string
	// Fix is the command that fixes what the check found; "" when none does.
	Fix string
	// Missing names a problem in a few words for a summary, such as "HEVC
	// is not available"; "" unless Status is Problem.
	Missing string
	// Blocks marks a problem that keeps DeskLink from working at all.
	Blocks bool
}

func passed(name, detail string) Check {
	return Check{Status: OK, Name: name, Detail: detail}
}

func warning(name, detail, fix string) Check {
	return Check{Status: Warning, Name: name, Detail: detail, Fix: fix}
}

func failed(name, detail, fix, missing string, blocks bool) Check {
	return Check{Status: Problem, Name: name, Detail: detail, Fix: fix, Missing: missing, Blocks: blocks}
}

// The commands that give Fedora what the encoders need: GStreamer's VA-API
// plugin with Mesa's driver, and RPM Fusion's build of that driver, which
// keeps the HEVC encoder that Fedora's leaves out.
const (
	fixVAAPI = "sudo dnf install gstreamer1-plugins-bad-free mesa-va-drivers"
	fixHEVC  = "sudo dnf swap mesa-va-drivers mesa-va-drivers-freeworld"
)

// Encoders judges both encoders in one line, as desklinkd pair shows them.
// A session needs one of them.
func Encoders(av1, hevc video.EncoderInfo) Check {
	const name = "Encoder"
	switch {
	case av1.Usable() && hevc.Usable():
		return passed(name, "VA-API AV1 and HEVC 10-bit")
	case av1.Usable():
		return warning(name, "VA-API AV1 only; HEVC 10-bit is not available", fixHEVC)
	case hevc.Usable():
		return warning(name, "VA-API HEVC 10-bit only; AV1 is not available", "")
	}
	return failed(name, "no VA-API encoder for 10-bit AV1 or HEVC", fixVAAPI, "no video encoder is available", true)
}

// The commands that give the user at the desk the uinput node: applying the
// installed udev rule, or installing it from a source checkout first, as
// make install-host prints it.
const (
	fixUinputRule    = "sudo udevadm trigger --sysname-match=uinput"
	fixUinputInstall = "sudo install -m644 host/deploy/60-desklink.rules /etc/udev/rules.d/ && " + fixUinputRule
)

// Input judges write access to the uinput node at path, which desklinkd
// needs to start: err is what input.CheckAccess returned, and ruleInstalled
// whether DeskLink's udev rule is in place.
func Input(path string, err error, ruleInstalled bool) Check {
	const name = "Input"
	switch {
	case err == nil:
		return passed(name, path+" is writable")
	case errors.Is(err, fs.ErrNotExist):
		return failed(name, path+" does not exist", "sudo modprobe uinput", path+" is missing", true)
	case ruleInstalled:
		return failed(name, path+" is not writable", fixUinputRule, path+" is not writable", true)
	}
	return failed(name, path+" is not writable", fixUinputInstall, path+" is not writable", true)
}

// WakeOnLAN judges whether a client can wake this computer: w is what
// desktop.WakeFor found for the pairing address, and err why that address
// is unknown.
func WakeOnLAN(w desktop.Wake, err error) Check {
	const name = "Wake-on-LAN"
	switch {
	case err != nil:
		return warning(name, "cannot tell: "+err.Error(), "")
	case w.Interface == "":
		return warning(name, "cannot tell: no network card holds the pairing address", "")
	case w.Problem != "":
		return warning(name, w.Problem, w.Fix)
	}
	return passed(name, "ready on "+w.Interface)
}

// fixAutologin turns on GDM's automatic and timed login.
const fixAutologin = "desklinkd autologin on"

// Autologin judges whether GDM logs desklinkd's user in by itself, so that
// desklinkd runs after a reboot: err is why GDM's settings could not be
// read.
func Autologin(l desktop.GDMLogin, err error) Check {
	const name = "Automatic login"
	switch {
	case err != nil:
		return warning(name, "cannot tell: "+err.Error(), "")
	case l.Automatic && l.Timed:
		return passed(name, "on")
	case l.Timed:
		return passed(name, fmt.Sprintf("on, %d s after the login screen appears", l.TimedDelay))
	case l.Automatic:
		return warning(name, "at boot only; after a crash or a logout someone has to log in", fixAutologin)
	}
	return warning(name, "off; after a reboot someone has to log in", fixAutologin)
}

// GNOME judges the user's graphical session, which desklinkd runs in and
// captures: s and sessionErr are what logind said about it, and gnome and
// gnomeErr what the session bus said about GNOME's services.
func GNOME(s desktop.GraphicalSession, sessionErr error, gnome desktop.GNOMEServices, gnomeErr error) Check {
	const name = "GNOME on Wayland"
	switch {
	case errors.Is(sessionErr, desktop.ErrNoGraphicalSession):
		return failed(name, "nobody is logged in to a desktop session", "", "nobody is logged in to GNOME", true)
	case sessionErr != nil:
		return warning(name, "cannot tell: "+sessionErr.Error(), "")
	case s.Type != "wayland":
		return failed(name, "the session of "+s.User+" is "+s.Type+", not Wayland", "", "the desk's session is not Wayland", true)
	case gnomeErr != nil:
		return warning(name, "cannot tell: "+gnomeErr.Error(), "")
	case !gnome.Shell:
		return failed(name, "the session of "+s.User+" does not run GNOME Shell", "", "the desk does not run GNOME", true)
	}
	return passed(name, "session of "+s.User)
}

// ScreenCast judges Mutter's screen-cast and remote-desktop services, which
// desklinkd captures the screen and types text through: err is why the
// session bus could not say.
func ScreenCast(gnome desktop.GNOMEServices, err error) Check {
	const name = "Screen cast"
	switch {
	case err != nil:
		return warning(name, "cannot tell: "+err.Error(), "")
	case gnome.ScreenCast && gnome.RemoteDesktop:
		return passed(name, "Mutter screen cast and remote desktop")
	case gnome.RemoteDesktop:
		return failed(name, "Mutter offers no screen cast", "", "Mutter's screen cast is missing", true)
	case gnome.ScreenCast:
		return failed(name, "Mutter offers no remote desktop", "", "Mutter's remote desktop is missing", true)
	}
	return failed(name, "Mutter offers neither screen cast nor remote desktop", "", "Mutter's screen cast is missing", true)
}

// AV1Encoder judges the AV1 encoder. Without it DeskLink still works if
// the HEVC encoder does.
func AV1Encoder(av1, hevc video.EncoderInfo) Check {
	fix := ""
	if !hevc.Usable() {
		fix = fixVAAPI
	}
	return encoder("AV1 encoder", "AV1", av1, hevc.Usable(), fix)
}

// HEVCEncoder judges the 10-bit HEVC encoder. Without it DeskLink still
// works if the AV1 encoder does.
func HEVCEncoder(av1, hevc video.EncoderInfo) Check {
	return encoder("HEVC 10-bit", "HEVC", hevc, av1.Usable(), fixHEVC)
}

// encoder judges one codec's encoder e; a missing one blocks DeskLink
// unless the other codec's works.
func encoder(name, codec string, e video.EncoderInfo, otherWorks bool, fix string) Check {
	missing := codec + " is not available"
	switch {
	case e.Usable() && e.Device != "":
		return passed(name, "VA-API, "+e.Device)
	case e.Usable():
		return passed(name, "VA-API")
	case e.Available:
		return failed(name, "8-bit only; desklinkd sends 10-bit frames", "", missing, !otherWorks)
	}
	return failed(name, "not available", fix, missing, !otherWorks)
}

// Listening judges desklinkd's ports: running says whether desklinkd
// answers on its local socket, and err why the sockets could not be read.
func Listening(running bool, ports []PortState, err error) Check {
	const name = "Listening"
	if err != nil {
		return warning(name, "cannot tell: "+err.Error(), "")
	}
	var used, free []Port
	for _, p := range ports {
		if p.InUse {
			used = append(used, p.Port)
		} else {
			free = append(free, p.Port)
		}
	}
	switch {
	case running && len(free) == 0:
		return passed(name, list(used, ", "))
	case running:
		return failed(name, "nothing listens on "+listAnd(free)+"; desklinkd runs with other ports",
			"systemctl --user restart desklinkd", "desklinkd does not listen on "+listAnd(free), true)
	case len(used) == 0:
		return warning(name, listAnd(free)+" "+isAre(free)+" free, but desklinkd is not running", "")
	}
	var filters []string
	for _, p := range used {
		filters = append(filters, "sport = :"+strconv.Itoa(p.Number))
	}
	return failed(name, listAnd(used)+" "+isAre(used)+" in use by another program",
		"sudo ss -ltunp '"+strings.Join(filters, " or ")+"'", "another program uses "+listAnd(used), true)
}

// Firewall judges whether firewalld lets clients reach desklinkd's ports:
// err is why firewalld could not say.
func Firewall(fw FirewallState, err error) Check {
	const name = "Firewall"
	switch {
	case err != nil:
		return warning(name, "cannot tell: "+err.Error(), "")
	case !fw.Running:
		return passed(name, "firewalld is not running")
	case len(fw.Closed) == 0:
		return passed(name, listAnd(fw.Open)+" open in zone "+fw.Zone)
	}
	fix := "sudo firewall-cmd --permanent --zone=" + fw.Zone
	for _, p := range fw.Closed {
		fix += " --add-port=" + p.String()
	}
	fix += " && sudo firewall-cmd --reload"
	return warning(name, listAnd(fw.Closed)+" closed in zone "+fw.Zone, fix)
}

// The commands that install desklinkd's user unit from a source checkout
// and start it, and that show why it failed.
const (
	fixServiceInstall = "make install-host && systemctl --user enable --now desklinkd"
	fixServiceLog     = "journalctl --user -u desklinkd -b"
)

// Service judges desklinkd's systemd user unit, which starts it with the
// session and restarts it: err is why systemd could not say, and running
// whether desklinkd answers on its local socket all the same.
func Service(s ServiceState, err error, running bool, now time.Time) Check {
	const name = "Service"
	notRunning := "desklinkd is not running"
	switch {
	case err != nil && running:
		return warning(name, "cannot tell: "+err.Error(), "")
	case err != nil:
		return failed(name, "desklinkd is not running, and systemd did not answer: "+err.Error(), "", notRunning, true)
	case s.Load == "not-found" && running:
		return warning(name, "not installed; desklinkd runs by hand", fixServiceInstall)
	case s.Load == "not-found":
		return failed(name, "not installed", fixServiceInstall, "desklinkd is not installed as a service", true)
	case s.Active == "active" && s.UnitFile == "disabled":
		return warning(name, "active"+activeFor(s, now)+", but it does not start at login", "systemctl --user enable desklinkd")
	case s.Active == "active":
		return passed(name, "active"+activeFor(s, now))
	case s.Active == "activating" && s.Sub == "auto-restart":
		return failed(name, "keeps failing and restarting", fixServiceLog, "desklinkd keeps failing", true)
	case s.Active == "activating":
		return warning(name, "starting", "")
	case s.Active == "failed":
		return failed(name, "failed", fixServiceLog, "desklinkd failed", true)
	case running:
		return warning(name, s.Active+"; desklinkd runs by hand", "")
	case s.UnitFile == "enabled":
		return failed(name, "not running", "systemctl --user start desklinkd", notRunning, true)
	}
	return failed(name, "not running", "systemctl --user enable --now desklinkd", notRunning, true)
}

func activeFor(s ServiceState, now time.Time) string {
	if s.Since.IsZero() {
		return ""
	}
	return " for " + Span(now.Sub(s.Since))
}

// Device is a paired device as the devices check shows it.
type Device struct {
	Name string
	// LastSeen is when it last started a session; zero if never.
	LastSeen  time.Time
	Connected bool
}

// Devices lists the paired devices and when each was last seen: err is
// why they could not be read.
func Devices(devices []Device, err error, now time.Time) Check {
	switch {
	case err != nil:
		return warning("Paired", "cannot tell: "+err.Error(), "")
	case len(devices) == 0:
		return warning("0 paired", "no device can connect yet", "desklinkd pair")
	}
	var seen []string
	for _, d := range devices {
		when := "never connected"
		switch {
		case d.Connected:
			when = "connected now"
		case !d.LastSeen.IsZero():
			when = Ago(now.Sub(d.LastSeen))
		}
		seen = append(seen, d.Name+" ("+when+")")
	}
	return passed(fmt.Sprintf("%d paired", len(devices)), strings.Join(seen, ", "))
}

// Span says how long d is in its largest whole unit: "5 min", "3 h" (under
// two days) or "3 days"; "less than a minute" below that.
func Span(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "less than a minute"
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h", int(d.Hours()))
	}
	return fmt.Sprintf("%d days", int(d.Hours()/24))
}

// Ago says how long ago something happened d before now: "just now" under
// a minute, otherwise Span followed by "ago".
func Ago(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	return Span(d) + " ago"
}

func list(ports []Port, sep string) string {
	var s []string
	for _, p := range ports {
		s = append(s, p.String())
	}
	return strings.Join(s, sep)
}

// listAnd joins ports as a sentence does: "a", "a and b", "a, b and c".
func listAnd(ports []Port) string {
	var s []string
	for _, p := range ports {
		s = append(s, p.String())
	}
	return joinAnd(s)
}

func joinAnd(s []string) string {
	if len(s) < 2 {
		return strings.Join(s, "")
	}
	return strings.Join(s[:len(s)-1], ", ") + " and " + s[len(s)-1]
}

func isAre(ports []Port) string {
	if len(ports) == 1 {
		return "is"
	}
	return "are"
}
