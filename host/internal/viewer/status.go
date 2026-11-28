package viewer

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"desklink/internal/client"
	dlnet "desklink/internal/net"
)

// rowAction is the one action a computer's row offers.
type rowAction int

const (
	// actionConnect starts a session, waking the computer first when it
	// can.
	actionConnect rowAction = iota
	// actionWake wakes a computer that did not answer, without connecting.
	actionWake
	// actionWaking stands in for Wake while the computer wakes.
	actionWaking
	// actionPair pairs a computer saved without a pairing.
	actionPair
)

func (a rowAction) label() string {
	switch a {
	case actionWake:
		return "Wake"
	case actionWaking:
		return "Waking…"
	case actionPair:
		return "Pair…"
	default:
		return "Connect"
	}
}

// hostStatus is how the list shows a computer: a dot, a status line and
// one action.
type hostStatus struct {
	dot    string
	text   string
	action rowAction
	// main marks the action as the main one, in the accent colour: Connect
	// on a computer that answered.
	main bool
}

var (
	notPairedStatus = hostStatus{dot: dotAsleep, text: "Not paired · choose Pair and paste the line desklinkd pair prints", action: actionPair}
	checkingStatus  = hostStatus{text: "Checking…", action: actionConnect}
)

// wakingStatus is the status while Wake waits for the computer to answer.
func wakingStatus() hostStatus {
	return hostStatus{dot: dotWaking, text: fmt.Sprintf("Waking · this can take up to %d s", int(client.WakeTimeout.Seconds())), action: actionWaking}
}

// checkedStatus is the status of the paired computer p once it was asked
// for /health: err is nil when it answered with h over a network of kind
// (client.NetworkKind), and udp and udpErr are its media port's answer to a
// probe, made only when h names the port. A computer that did not answer is
// asleep when Wake-on-LAN can wake it, and unreachable otherwise.
func checkedStatus(p client.Profile, kind string, h dlnet.Health, udp time.Duration, udpErr, err error) hostStatus {
	if err != nil {
		if p.MAC != "" {
			return hostStatus{dot: dotAsleep, text: "Asleep · Connect wakes it", action: actionWake}
		}
		return hostStatus{dot: dotUnreachable, text: unreachableText(len(p.Candidates())), action: actionConnect}
	}
	s := hostStatus{dot: dotOnline, action: actionConnect, main: true}
	parts := []string{"Online"}
	if kind != "" {
		parts = append(parts, kind)
	}
	switch {
	case h.UDPPort == 0:
		// A host from before the probes; the protocol note says to update it.
	case udpErr != nil:
		// HTTPS got through but the picture would not.
		s.dot = dotWaking
		parts = append(parts, fmt.Sprintf("UDP port %d does not answer: check the firewall or VPN", h.UDPPort))
	default:
		parts = append(parts, fmt.Sprintf("%d ms", udp.Milliseconds()))
	}
	switch protocol := max(h.Protocol, 1); {
	case protocol < dlnet.ControlProtocolVersion:
		parts = append(parts, fmt.Sprintf("desklinkd %s is older than this app: update it for every feature", h.Version))
	case protocol > dlnet.ControlProtocolVersion:
		parts = append(parts, "desklinkd is newer than this app: update the app for every feature")
	}
	s.text = strings.Join(parts, " · ")
	return s
}

// unreachableText says that none of a computer's addresses answered.
func unreachableText(addresses int) string {
	if addresses == 1 {
		return "Unreachable · its address did not answer: check that it is on and runs desklinkd"
	}
	return fmt.Sprintf("Unreachable · none of its %d addresses answered: check that it is on and runs desklinkd", addresses)
}

// poorLoss is the share of lost packets, in percent, from which the link
// counts as poor.
const poorLoss = 2

// linkDot is the colour of the dot beside the session's frame rate: yellow
// until the picture arrives and while the link is poor (poorLoss or more,
// or Auto Quality below its top level), red while nothing arrives, green
// otherwise.
func linkDot(mediaUp, quiet bool, st client.Stats, info dlnet.SessionInfo) string {
	switch {
	case !mediaUp:
		return dotWaking
	case quiet:
		return dotUnreachable
	case st.LossPct >= poorLoss || (info.Auto && info.Level > 0):
		return dotWaking
	default:
		return dotOnline
	}
}

// linkText is the words beside the dot: the frame rate, or why there is
// none.
func linkText(mediaUp, quiet bool, fps int) string {
	switch {
	case !mediaUp:
		return "Connecting…"
	case quiet:
		return "No packets"
	default:
		return fmt.Sprintf("%d fps", fps)
	}
}

// statsText is the session's Stats, one topic per line: the round trip and
// delay, what arrives, the stream and, with Auto Quality, its level, then
// the host's description of the screen.
func statsText(st client.Stats, info dlnet.SessionInfo) string {
	link := fmt.Sprintf("%d ms round trip", st.RTT.Milliseconds())
	if st.Delay > 0 {
		link += fmt.Sprintf(" · %d ms delay", st.Delay.Milliseconds())
	}
	lines := []string{
		link,
		fmt.Sprintf("%d fps · %.1f Mbps · %.1f %% loss", st.FPS, st.Mbps, st.LossPct),
		fmt.Sprintf("%s %d×%d, up to %d fps, QP %d", strings.ToUpper(info.Codec), info.Width, info.Height, info.FPS, info.QP),
	}
	if info.Auto {
		lines = append(lines, fmt.Sprintf("Auto Quality: level %d of %d", info.Level+1, len(info.Levels)))
	}
	if info.Display != "" {
		lines = append(lines, info.Display)
	}
	return strings.Join(lines, "\n")
}

// wakeFailure says why the computer name did not wake up and what to check.
func wakeFailure(name string, err error) string {
	if errors.Is(err, client.ErrNotWoken) {
		return fmt.Sprintf("%s did not wake up within %d s. Check that Wake-on-LAN is on in its firmware; desklinkd pair on %s says whether it is ready.",
			name, int(client.WakeTimeout.Seconds()), name)
	}
	return fmt.Sprintf("Could not wake %s: %v", name, err)
}
