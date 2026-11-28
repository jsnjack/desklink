package doctor

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

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

var (
	waylandSession = desktop.GraphicalSession{User: "jsn", Type: "wayland"}
	gnomeRunning   = desktop.GNOMEServices{Shell: true, ScreenCast: true, RemoteDesktop: true}
)

func TestGNOME(t *testing.T) {
	cases := []struct {
		name       string
		session    desktop.GraphicalSession
		sessionErr error
		gnome      desktop.GNOMEServices
		gnomeErr   error
		want       want
	}{
		{"GNOME on Wayland", waylandSession, nil, gnomeRunning, nil, want{OK, "session of jsn", "", false}},
		{"nobody logged in blocks", desktop.GraphicalSession{}, desktop.ErrNoGraphicalSession, desktop.GNOMEServices{}, nil,
			want{Problem, "nobody is logged in", "", true}},
		{"X11 blocks", desktop.GraphicalSession{User: "jsn", Type: "x11"}, nil, gnomeRunning, nil, want{Problem, "is x11, not Wayland", "", true}},
		{"another desktop blocks", waylandSession, nil, desktop.GNOMEServices{}, nil, want{Problem, "does not run GNOME Shell", "", true}},
		{"no logind is not a verdict", desktop.GraphicalSession{}, errors.New("no system bus"), desktop.GNOMEServices{}, nil,
			want{Warning, "cannot tell", "", false}},
		{"no session bus is not a verdict", waylandSession, nil, desktop.GNOMEServices{}, errors.New("no session bus"),
			want{Warning, "cannot tell", "", false}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.want.verify(t, GNOME(c.session, c.sessionErr, c.gnome, c.gnomeErr)) })
	}
}

func TestScreenCast(t *testing.T) {
	cases := []struct {
		name  string
		gnome desktop.GNOMEServices
		err   error
		want  want
	}{
		{"both services", gnomeRunning, nil, want{OK, "screen cast and remote desktop", "", false}},
		{"no screen cast blocks", desktop.GNOMEServices{Shell: true, RemoteDesktop: true}, nil, want{Problem, "no screen cast", "", true}},
		{"no remote desktop blocks", desktop.GNOMEServices{Shell: true, ScreenCast: true}, nil, want{Problem, "no remote desktop", "", true}},
		{"neither blocks", desktop.GNOMEServices{}, nil, want{Problem, "neither", "", true}},
		{"no session bus", desktop.GNOMEServices{}, errors.New("no session bus"), want{Warning, "cannot tell", "", false}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.want.verify(t, ScreenCast(c.gnome, c.err)) })
	}
}

func TestEncoderLines(t *testing.T) {
	cases := []struct {
		name      string
		av1, hevc video.EncoderInfo
		av1Want   want
		hevcWant  want
	}{
		{"both name the GPU", gpu, gpu, want{OK, "VA-API, AMD Radeon 890M Graphics", "", false}, want{OK, "VA-API, AMD Radeon 890M Graphics", "", false}},
		{"no HEVC is a problem that does not block", gpu, noEncoder, want{OK, "VA-API", "", false}, want{Problem, "not available", fixHEVC, false}},
		{"8-bit HEVC does not count", gpu, eightBit, want{OK, "VA-API", "", false}, want{Problem, "8-bit only", "", false}},
		{"no AV1 is a problem that does not block", noEncoder, gpu, want{Problem, "not available", "", false}, want{OK, "VA-API", "", false}},
		{"neither blocks, with the driver to install", noEncoder, noEncoder, want{Problem, "not available", fixVAAPI, true}, want{Problem, "not available", fixHEVC, true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.av1Want.verify(t, AV1Encoder(c.av1, c.hevc))
			c.hevcWant.verify(t, HEVCEncoder(c.av1, c.hevc))
		})
	}
	if got := HEVCEncoder(gpu, noEncoder).Missing; got != "HEVC is not available" {
		t.Errorf("Missing = %q", got)
	}
}

var (
	control = Port{Number: 9443, Proto: "tcp"}
	media   = Port{Number: 9444, Proto: "udp"}
)

func TestListening(t *testing.T) {
	both := func(controlUsed, mediaUsed bool) []PortState {
		return []PortState{{control, controlUsed}, {media, mediaUsed}}
	}
	cases := []struct {
		name    string
		running bool
		ports   []PortState
		err     error
		want    want
	}{
		{"desklinkd listens", true, both(true, true), nil, want{OK, "9443/tcp, 9444/udp", "", false}},
		{"stopped, the ports are free", false, both(false, false), nil, want{Warning, "9443/tcp and 9444/udp are free, but desklinkd is not running", "", false}},
		{"stopped, another program holds a port", false, both(true, false), nil,
			want{Problem, "9443/tcp is in use by another program", "sudo ss -ltunp 'sport = :9443'", true}},
		{"running on other ports", true, both(true, false), nil, want{Problem, "nothing listens on 9444/udp", "systemctl --user restart desklinkd", true}},
		{"unreadable", false, nil, errors.New("permission denied"), want{Warning, "cannot tell", "", false}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.want.verify(t, Listening(c.running, c.ports, c.err)) })
	}
}

func TestFirewall(t *testing.T) {
	cases := []struct {
		name string
		fw   FirewallState
		err  error
		want want
	}{
		{"no firewalld", FirewallState{}, nil, want{OK, "firewalld is not running", "", false}},
		{"open", FirewallState{Running: true, Zone: "FedoraWorkstation", Open: []Port{control, media}}, nil,
			want{OK, "9443/tcp and 9444/udp open in zone FedoraWorkstation", "", false}},
		{"closed ports get the command for their zone", FirewallState{Running: true, Zone: "public", Open: []Port{control}, Closed: []Port{media}}, nil,
			want{Warning, "9444/udp closed in zone public", "sudo firewall-cmd --permanent --zone=public --add-port=9444/udp && sudo firewall-cmd --reload", false}},
		{"firewalld did not answer", FirewallState{}, errors.New("timeout"), want{Warning, "cannot tell", "", false}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.want.verify(t, Firewall(c.fw, c.err)) })
	}
}

func TestService(t *testing.T) {
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	threeDays := now.Add(-73 * time.Hour)
	cases := []struct {
		name    string
		s       ServiceState
		err     error
		running bool
		want    want
	}{
		{"active", ServiceState{Load: "loaded", Active: "active", Sub: "running", UnitFile: "enabled", Since: threeDays}, nil, true,
			want{OK, "active for 3 days", "", false}},
		{"active but not started at login", ServiceState{Load: "loaded", Active: "active", UnitFile: "disabled", Since: now.Add(-5 * time.Minute)}, nil, true,
			want{Warning, "active for 5 min, but it does not start at login", "systemctl --user enable desklinkd", false}},
		{"enabled but stopped", ServiceState{Load: "loaded", Active: "inactive", UnitFile: "enabled"}, nil, false,
			want{Problem, "not running", "systemctl --user start desklinkd", true}},
		{"disabled and stopped", ServiceState{Load: "loaded", Active: "inactive", UnitFile: "disabled"}, nil, false,
			want{Problem, "not running", "systemctl --user enable --now desklinkd", true}},
		{"failed", ServiceState{Load: "loaded", Active: "failed"}, nil, false, want{Problem, "failed", fixServiceLog, true}},
		{"restarting", ServiceState{Load: "loaded", Active: "activating", Sub: "auto-restart"}, nil, false, want{Problem, "keeps failing", fixServiceLog, true}},
		{"not installed", ServiceState{Load: "not-found", Active: "inactive"}, nil, false, want{Problem, "not installed", fixServiceInstall, true}},
		{"not installed, run by hand", ServiceState{Load: "not-found", Active: "inactive"}, nil, true,
			want{Warning, "runs by hand", fixServiceInstall, false}},
		{"systemd did not answer, desklinkd runs", ServiceState{}, errors.New("no bus"), true, want{Warning, "cannot tell", "", false}},
		{"systemd did not answer, desklinkd does not run", ServiceState{}, errors.New("no bus"), false, want{Problem, "not running", "", true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.want.verify(t, Service(c.s, c.err, c.running, now)) })
	}
}

func TestDevices(t *testing.T) {
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	devices := []Device{
		{Name: "Galaxy Tab S9", Connected: true},
		{Name: "ThinkPad", LastSeen: now.Add(-73 * time.Hour)},
		{Name: "Old phone"},
	}
	c := Devices(devices, nil, now)
	want{OK, "Galaxy Tab S9 (connected now), ThinkPad (3 days ago), Old phone (never connected)", "", false}.verify(t, c)
	if c.Name != "3 paired" {
		t.Errorf("name %q", c.Name)
	}
	want{Warning, "no device can connect yet", "desklinkd pair", false}.verify(t, Devices(nil, nil, now))
	want{Warning, "cannot tell", "", false}.verify(t, Devices(nil, errors.New("bad peers.json"), now))
}

func TestSpanAndAgo(t *testing.T) {
	cases := []struct {
		d         time.Duration
		span, ago string
	}{
		{30 * time.Second, "less than a minute", "just now"},
		{5 * time.Minute, "5 min", "5 min ago"},
		{3 * time.Hour, "3 h", "3 h ago"},
		{47 * time.Hour, "47 h", "47 h ago"},
		{73 * time.Hour, "3 days", "3 days ago"},
	}
	for _, c := range cases {
		if got := Span(c.d); got != c.span {
			t.Errorf("Span(%v) = %q, want %q", c.d, got, c.span)
		}
		if got := Ago(c.d); got != c.ago {
			t.Errorf("Ago(%v) = %q, want %q", c.d, got, c.ago)
		}
	}
}

func TestSymbols(t *testing.T) {
	for st, sym := range map[Status]string{OK: "✓", Warning: "!", Problem: "✗"} {
		if got := st.Symbol(); got != sym {
			t.Errorf("%d.Symbol() = %q, want %q", st, got, sym)
		}
	}
}
