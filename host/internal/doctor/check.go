// Package doctor judges whether this computer can serve DeskLink. Each check
// turns facts read from the system into a line marked ✓, ! or ✗, with the
// command that fixes what it found: desklinkd pair shows some of the checks.
package doctor

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

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

// udevRules are where DeskLink's udev rule for /dev/uinput lives: installed
// by the desklinkd RPM, or copied by hand after make install-host.
var udevRules = []string{"/usr/lib/udev/rules.d/60-desklink.rules", "/etc/udev/rules.d/60-desklink.rules"}

// UdevRuleInstalled reports whether DeskLink's udev rule for /dev/uinput is
// in place.
func UdevRuleInstalled() bool {
	for _, p := range udevRules {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
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
