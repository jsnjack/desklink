package viewer

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"desklink/internal/client"
	dlnet "desklink/internal/net"
)

func TestCheckedStatus(t *testing.T) {
	current := dlnet.Health{Hostname: "workstation", Version: "0.60.0", Protocol: dlnet.ControlProtocolVersion, UDPPort: 9444}
	old := dlnet.Health{Hostname: "workstation", Version: "0.28.0"}
	newer := current
	newer.Protocol++
	paired := client.Profile{Name: "workstation", IP: "192.168.178.11", PeerID: "a", PeerKey: []byte{1}, Fingerprint: "ab"}
	wakeable := paired
	wakeable.MAC = "00:11:22:33:44:55"
	elsewhere := paired
	elsewhere.Addresses = []string{"10.8.0.2", "10.0.0.7"}
	noAnswer := errors.New("no address answered")
	cases := []struct {
		name   string
		p      client.Profile
		kind   string
		h      dlnet.Health
		udp    time.Duration
		udpErr error
		err    error
		want   hostStatus
	}{
		{
			"a computer that answers on a LAN offers Connect as the main action", paired, client.NetworkLAN, current, 12 * time.Millisecond, nil, nil,
			hostStatus{dot: dotOnline, text: "Online · LAN · 12 ms", action: actionConnect, main: true},
		},
		{
			"a VPN whose UDP is blocked says what to check", paired, client.NetworkVPN, current, 0, client.ErrNoUDPReply, nil,
			hostStatus{dot: dotWaking, text: "Online · VPN · UDP port 9444 does not answer: check the firewall or VPN", action: actionConnect, main: true},
		},
		{
			"an older desklinkd says to update it", paired, "", old, 0, nil, nil,
			hostStatus{dot: dotOnline, text: "Online · desklinkd 0.28.0 is older than this app: update it for every feature", action: actionConnect, main: true},
		},
		{
			"a newer desklinkd says to update the app", paired, client.NetworkLAN, newer, time.Millisecond, nil, nil,
			hostStatus{dot: dotOnline, text: "Online · LAN · 1 ms · desklinkd is newer than this app: update the app for every feature", action: actionConnect, main: true},
		},
		{
			"a computer Wake-on-LAN can wake is asleep", wakeable, "", dlnet.Health{}, 0, nil, noAnswer,
			hostStatus{dot: dotAsleep, text: "Asleep · Connect wakes it", action: actionWake},
		},
		{
			"a computer without a MAC is unreachable", paired, "", dlnet.Health{}, 0, nil, noAnswer,
			hostStatus{dot: dotUnreachable, text: "Unreachable · its address did not answer: check that it is on and runs desklinkd", action: actionConnect},
		},
		{
			"an unreachable computer with several addresses counts them", elsewhere, "", dlnet.Health{}, 0, nil, noAnswer,
			hostStatus{dot: dotUnreachable, text: "Unreachable · none of its 3 addresses answered: check that it is on and runs desklinkd", action: actionConnect},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := checkedStatus(c.p, c.kind, c.h, c.udp, c.udpErr, c.err); got != c.want {
				t.Errorf("checkedStatus = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestRowActionLabel(t *testing.T) {
	cases := []struct {
		action rowAction
		want   string
	}{
		{actionConnect, "Connect"},
		{actionWake, "Wake"},
		{actionWaking, "Waking…"},
		{actionPair, "Pair…"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			if got := c.action.label(); got != c.want {
				t.Errorf("label() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestLinkDotAndText(t *testing.T) {
	good := client.Stats{FPS: 120, LossPct: 0.1}
	top := dlnet.SessionInfo{Auto: true, Level: 0, Levels: make([]dlnet.Level, 4)}
	lowered := dlnet.SessionInfo{Auto: true, Level: 2, Levels: make([]dlnet.Level, 4)}
	cases := []struct {
		name           string
		mediaUp, quiet bool
		st             client.Stats
		info           dlnet.SessionInfo
		dot, text      string
	}{
		{"connecting", false, false, client.Stats{}, dlnet.SessionInfo{}, dotWaking, "Connecting…"},
		{"a good link at Auto Quality's top", true, false, good, top, dotOnline, "120 fps"},
		{"Auto Quality lowered by the host", true, false, good, lowered, dotWaking, "120 fps"},
		{"a fixed preset ignores levels", true, false, good, dlnet.SessionInfo{Level: 2}, dotOnline, "120 fps"},
		{"heavy loss", true, false, client.Stats{FPS: 90, LossPct: 3}, top, dotWaking, "90 fps"},
		{"nothing arrives", true, true, good, top, dotUnreachable, "No packets"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := linkDot(c.mediaUp, c.quiet, c.st, c.info); got != c.dot {
				t.Errorf("linkDot = %q, want %q", got, c.dot)
			}
			if got := linkText(c.mediaUp, c.quiet, c.st.FPS); got != c.text {
				t.Errorf("linkText = %q, want %q", got, c.text)
			}
		})
	}
}

func TestStatsText(t *testing.T) {
	st := client.Stats{RTT: 12 * time.Millisecond, Delay: 18 * time.Millisecond, FPS: 120, Mbps: 24.31, LossPct: 0.25}
	auto := dlnet.SessionInfo{Codec: "av1", Width: 2560, Height: 1600, FPS: 120, QP: 12, Auto: true, Level: 0, Levels: make([]dlnet.Level, 6), Display: "own screen 2560×1600 at 125 %"}
	cases := []struct {
		name string
		st   client.Stats
		info dlnet.SessionInfo
		want string
	}{
		{
			"Auto Quality with the delay known", st, auto,
			"12 ms round trip · 18 ms delay\n120 fps · 24.3 Mbps · 0.2 % loss\nAV1 2560×1600, up to 120 fps, QP 12\nAuto Quality: level 1 of 6\nown screen 2560×1600 at 125 %",
		},
		{
			"a fixed preset before the delay is known", client.Stats{RTT: 3 * time.Millisecond, FPS: 60, Mbps: 8}, dlnet.SessionInfo{Codec: "hevc", Width: 1920, Height: 1200, FPS: 60, QP: 20},
			"3 ms round trip\n60 fps · 8.0 Mbps · 0.0 % loss\nHEVC 1920×1200, up to 60 fps, QP 20",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := statsText(c.st, c.info); got != c.want {
				t.Errorf("statsText =\n%s\nwant\n%s", got, c.want)
			}
		})
	}
}

func TestWakeFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want []string
	}{
		{"no answer after the packets names the wait and what to check", client.ErrNotWoken, []string{"workstation did not wake up within 90 s", "Wake-on-LAN", "desklinkd pair on workstation"}},
		{"another failure gives its reason", fmt.Errorf("send Wake-on-LAN packet: %w", errors.New("network is unreachable")), []string{"Could not wake workstation: send Wake-on-LAN packet: network is unreachable"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := wakeFailure("workstation", c.err)
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("%q lacks %q", got, w)
				}
			}
		})
	}
}
