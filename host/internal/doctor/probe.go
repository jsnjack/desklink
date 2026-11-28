package doctor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Runner runs a command and returns its standard output and exit status;
// err says that it could not run at all.
type Runner func(ctx context.Context, name string, args ...string) (out string, status int, err error)

// Exec runs commands for real.
func Exec(ctx context.Context, name string, args ...string) (string, int, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return string(out), exit.ExitCode(), nil
	}
	return string(out), 0, err
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

// Port is one of desklinkd's ports.
type Port struct {
	Number int
	// Proto is "tcp" or "udp".
	Proto string
}

func (p Port) String() string { return strconv.Itoa(p.Number) + "/" + p.Proto }

// PortState says whether a socket on this computer holds a port.
type PortState struct {
	Port
	InUse bool
}

// procNet is where the kernel lists the sockets of every user.
var procNet = "/proc/net"

// tcpListen is the state of a listening socket in /proc/net/tcp.
const tcpListen = "0A"

// PortInUse reports whether a socket listens on p (TCP) or is bound to it
// (UDP), whoever owns it. It reads /proc/net and opens no socket.
func PortInUse(p Port) (bool, error) {
	for _, table := range []string{p.Proto, p.Proto + "6"} {
		data, err := os.ReadFile(filepath.Join(procNet, table))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("read the socket table: %w", err)
		}
		if holds(string(data), p) {
			return true, nil
		}
	}
	return false, nil
}

// holds reports whether a /proc/net/tcp or udp table lists a socket on p:
// for TCP one that listens, for UDP any.
func holds(table string, p Port) bool {
	for i, line := range strings.Split(table, "\n") {
		f := strings.Fields(line)
		if i == 0 || len(f) < 4 {
			continue
		}
		hexPort := f[1][strings.LastIndexByte(f[1], ':')+1:]
		port, err := strconv.ParseUint(hexPort, 16, 16)
		if err != nil || int(port) != p.Number || p.Proto == "tcp" && f[3] != tcpListen {
			continue
		}
		return true
	}
	return false
}

// FirewallState is what firewalld allows for desklinkd's ports.
type FirewallState struct {
	// Running is set when firewalld runs; the rest is empty otherwise.
	Running bool
	// Zone is the zone of the network card clients reach.
	Zone         string
	Open, Closed []Port
}

// firewalld's exit statuses: --state when it is not running, and a query
// whose answer is no.
const (
	firewallNotRunning = 252
	firewallNo         = 1
)

// ReadFirewall asks firewalld, with firewall-cmd's queries alone, which of
// ports the zone of the network card iface opens; "" means the default
// zone. Without firewall-cmd there is no firewalld.
func ReadFirewall(ctx context.Context, run Runner, iface string, ports []Port) (FirewallState, error) {
	_, status, err := run(ctx, "firewall-cmd", "--state")
	switch {
	case errors.Is(err, exec.ErrNotFound), err == nil && status == firewallNotRunning:
		return FirewallState{}, nil
	case err != nil:
		return FirewallState{}, fmt.Errorf("firewall-cmd --state: %w", err)
	case status != 0:
		return FirewallState{}, fmt.Errorf("firewall-cmd --state exited with status %d", status)
	}
	fw := FirewallState{Running: true}
	if iface != "" {
		// Exit status 2 says "no zone": the default zone applies.
		if out, status, err := run(ctx, "firewall-cmd", "--get-zone-of-interface="+iface); err == nil && status == 0 {
			fw.Zone = strings.TrimSpace(out)
		}
	}
	if fw.Zone == "" {
		out, status, err := run(ctx, "firewall-cmd", "--get-default-zone")
		if err != nil || status != 0 {
			return FirewallState{}, fmt.Errorf("firewall-cmd --get-default-zone: status %d: %v", status, err)
		}
		fw.Zone = strings.TrimSpace(out)
	}
	// A zone whose target is ACCEPT, such as trusted, lets everything in.
	if out, status, err := run(ctx, "firewall-cmd", "--permanent", "--zone="+fw.Zone, "--get-target"); err == nil && status == 0 && strings.TrimSpace(out) == "ACCEPT" {
		fw.Open = ports
		return fw, nil
	}
	for _, p := range ports {
		// A port inside an opened range, such as 1025-65535/udp, answers yes.
		out, status, err := run(ctx, "firewall-cmd", "--zone="+fw.Zone, "--query-port="+p.String())
		switch {
		case err != nil || status != 0 && status != firewallNo:
			return FirewallState{}, fmt.Errorf("firewall-cmd --query-port=%s: status %d: %v", p, status, err)
		case strings.TrimSpace(out) == "yes":
			fw.Open = append(fw.Open, p)
		default:
			fw.Closed = append(fw.Closed, p)
		}
	}
	return fw, nil
}

// ServiceState is desklinkd's systemd user unit as systemctl reports it.
type ServiceState struct {
	// Load is LoadState: "loaded", or "not-found" for no unit.
	Load string
	// Active and Sub are ActiveState and SubState, such as "active" and
	// "running", or "activating" and "auto-restart".
	Active, Sub string
	// UnitFile is UnitFileState: "enabled" starts it with the session.
	UnitFile string
	// Since is when it last became active; zero when it never did.
	Since time.Time
}

// unitName is desklinkd's systemd user unit.
const unitName = "desklinkd.service"

// ReadService asks systemd for the state of desklinkd's user unit.
func ReadService(ctx context.Context, run Runner) (ServiceState, error) {
	out, status, err := run(ctx, "systemctl", "--user", "show", unitName, "--timestamp=unix",
		"--property=LoadState,ActiveState,SubState,UnitFileState,ActiveEnterTimestamp")
	if err != nil {
		return ServiceState{}, fmt.Errorf("systemctl --user show: %w", err)
	}
	if status != 0 {
		return ServiceState{}, fmt.Errorf("systemctl --user show exited with status %d", status)
	}
	return parseService(out), nil
}

// parseService reads systemctl show's KEY=VALUE lines.
func parseService(out string) ServiceState {
	var s ServiceState
	for _, line := range strings.Split(out, "\n") {
		key, value, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch key {
		case "LoadState":
			s.Load = value
		case "ActiveState":
			s.Active = value
		case "SubState":
			s.Sub = value
		case "UnitFileState":
			s.UnitFile = value
		case "ActiveEnterTimestamp":
			if sec, err := strconv.ParseInt(strings.TrimPrefix(value, "@"), 10, 64); err == nil && sec > 0 {
				s.Since = time.Unix(sec, 0)
			}
		}
	}
	return s
}
