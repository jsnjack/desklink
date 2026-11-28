package doctor

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"desklink/internal/desktop"
	"desklink/internal/video"
)

var (
	gpu        = video.EncoderInfo{Available: true, TenBit: true, Device: "AMD Radeon 890M Graphics"}
	eightBit   = video.EncoderInfo{Available: true, Device: "Intel UHD Graphics"}
	noEncoder  = video.EncoderInfo{}
	errDenied  = fmt.Errorf("/dev/uinput: %w", fs.ErrPermission)
	errMissing = fmt.Errorf("/dev/uinput: %w", fs.ErrNotExist)
)

// want is what a test expects of a check: its status, words in its detail,
// its fix and whether it blocks.
type want struct {
	status Status
	detail string
	fix    string
	blocks bool
}

func (w want) verify(t *testing.T, c Check) {
	t.Helper()
	if c.Status != w.status {
		t.Errorf("status %v, want %v: %+v", c.Status, w.status, c)
	}
	if !strings.Contains(c.Detail, w.detail) {
		t.Errorf("detail %q lacks %q", c.Detail, w.detail)
	}
	if c.Fix != w.fix {
		t.Errorf("fix %q, want %q", c.Fix, w.fix)
	}
	if c.Blocks != w.blocks {
		t.Errorf("blocks %v, want %v", c.Blocks, w.blocks)
	}
	if (c.Status == Problem) != (c.Missing != "") {
		t.Errorf("a problem, and only a problem, says what is missing: %+v", c)
	}
}

func TestEncoders(t *testing.T) {
	cases := []struct {
		name      string
		av1, hevc video.EncoderInfo
		want      want
	}{
		{"both encoders", gpu, gpu, want{OK, "AV1 and HEVC 10-bit", "", false}},
		{"AV1 alone points to RPM Fusion's driver", gpu, noEncoder, want{Warning, "AV1 only", fixHEVC, false}},
		{"an 8-bit HEVC encoder does not count", gpu, eightBit, want{Warning, "AV1 only", fixHEVC, false}},
		{"HEVC alone", noEncoder, gpu, want{Warning, "HEVC 10-bit only", "", false}},
		{"no encoder blocks", noEncoder, noEncoder, want{Problem, "no VA-API encoder", fixVAAPI, true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.want.verify(t, Encoders(c.av1, c.hevc)) })
	}
}

func TestInput(t *testing.T) {
	cases := []struct {
		name string
		err  error
		rule bool
		want want
	}{
		{"writable", nil, true, want{OK, "/dev/uinput is writable", "", false}},
		{"a missing node needs the module", errMissing, true, want{Problem, "does not exist", "sudo modprobe uinput", true}},
		{"an installed rule needs applying", errDenied, true, want{Problem, "not writable", fixUinputRule, true}},
		{"without the rule it is installed first", errDenied, false, want{Problem, "not writable", fixUinputInstall, true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.want.verify(t, Input("/dev/uinput", c.err, c.rule)) })
	}
}

func TestWakeOnLAN(t *testing.T) {
	cases := []struct {
		name string
		wake desktop.Wake
		err  error
		want want
	}{
		{"ready names the card", desktop.Wake{Interface: "enp7s0"}, nil, want{OK, "ready on enp7s0", "", false}},
		{"off gives the command", desktop.Wake{Interface: "enp7s0", Problem: "off on enp7s0", Fix: "sudo ethtool -s enp7s0 wol g"}, nil,
			want{Warning, "off on enp7s0", "sudo ethtool -s enp7s0 wol g", false}},
		{"Wi-Fi has no command", desktop.Wake{Interface: "wlan0", Problem: "wlan0 is Wi-Fi; waking needs a wired connection"}, nil,
			want{Warning, "Wi-Fi", "", false}},
		{"no card holds the address", desktop.Wake{}, nil, want{Warning, "cannot tell", "", false}},
		{"no address", desktop.Wake{}, errors.New("no LAN address within allowed_subnets"), want{Warning, "cannot tell: no LAN address", "", false}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.want.verify(t, WakeOnLAN(c.wake, c.err)) })
	}
}

func TestAutologin(t *testing.T) {
	cases := []struct {
		name  string
		login desktop.GDMLogin
		err   error
		want  want
	}{
		{"automatic and timed login", desktop.GDMLogin{Automatic: true, Timed: true, TimedDelay: 10}, nil, want{OK, "on", "", false}},
		{"timed login alone counts as on", desktop.GDMLogin{Timed: true, TimedDelay: 10}, nil, want{OK, "10 s after", "", false}},
		{"at boot only", desktop.GDMLogin{Automatic: true}, nil, want{Warning, "at boot only", fixAutologin, false}},
		{"off", desktop.GDMLogin{}, nil, want{Warning, "off; after a reboot", fixAutologin, false}},
		{"unreadable is not reported as off", desktop.GDMLogin{}, errors.New("permission denied"), want{Warning, "cannot tell", "", false}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.want.verify(t, Autologin(c.login, c.err)) })
	}
}

func TestSymbols(t *testing.T) {
	for st, sym := range map[Status]string{OK: "✓", Warning: "!", Problem: "✗"} {
		if got := st.Symbol(); got != sym {
			t.Errorf("%d.Symbol() = %q, want %q", st, got, sym)
		}
	}
}
