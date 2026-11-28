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

// wakeFailure says why the computer name did not wake up and what to check.
func wakeFailure(name string, err error) string {
	if errors.Is(err, client.ErrNotWoken) {
		return fmt.Sprintf("%s did not wake up within %d s. Check that Wake-on-LAN is on in its firmware; desklinkd pair on %s says whether it is ready.",
			name, int(client.WakeTimeout.Seconds()), name)
	}
	return fmt.Sprintf("Could not wake %s: %v", name, err)
}
