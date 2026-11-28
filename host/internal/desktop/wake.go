package desktop

import (
	"fmt"
	stdnet "net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
)

// sysNet is where the kernel describes network interfaces.
var sysNet = "/sys/class/net"

// interfaceFor returns the interface holding want.
func interfaceFor(want netip.Addr) (stdnet.Interface, bool) {
	ifaces, err := stdnet.Interfaces()
	if err != nil {
		return stdnet.Interface{}, false
	}
	for _, ifc := range ifaces {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if p, err := netip.ParsePrefix(a.String()); err == nil && p.Addr() == want {
				return ifc, true
			}
		}
	}
	return stdnet.Interface{}, false
}

// MACFor returns the hardware address of the interface holding want.
func MACFor(want netip.Addr) string {
	ifc, ok := interfaceFor(want)
	if !ok || len(ifc.HardwareAddr) == 0 {
		return ""
	}
	return ifc.HardwareAddr.String()
}

// Wake says whether a client can wake this computer through the network
// interface holding the address it wakes.
type Wake struct {
	// Interface holds the address; "" when no interface does.
	Interface string
	// Problem says why waking cannot work, in words that follow
	// "Wake-on-LAN"; "" when nothing is known to be wrong.
	Problem string
	// Fix is the command that solves Problem; "" when no command does.
	Fix string
}

// Note says in one sentence why a client may be unable to wake this
// computer and how to fix it; "" when nothing is known to be wrong.
func (w Wake) Note() string {
	switch {
	case w.Problem == "":
		return ""
	case w.Fix == "":
		return "Wake-on-LAN: " + w.Problem
	}
	return fmt.Sprintf("Wake-on-LAN: %s; turn it on now with `%s` "+
		"(the desklinkd package makes NetworkManager turn it on whenever a wired connection comes up)", w.Problem, w.Fix)
}

// WakeFor says whether a client can wake this computer through the
// interface holding ip. It reads sysfs: Wi-Fi cannot be woken by a magic
// packet, a changed MAC address is not the one the card listens for while
// asleep, and ethernet drivers enable the card's wakeup flag when
// Wake-on-LAN is armed.
func WakeFor(ip netip.Addr) Wake {
	ifc, ok := interfaceFor(ip)
	if !ok {
		return Wake{}
	}
	return wakeFor(ifc.Name)
}

func wakeFor(name string) Wake {
	w := Wake{Interface: name}
	dir := filepath.Join(sysNet, name)
	if _, err := os.Stat(filepath.Join(dir, "wireless")); err == nil {
		w.Problem = name + " is Wi-Fi; waking needs a wired connection"
		return w
	}
	if t, err := os.ReadFile(filepath.Join(dir, "addr_assign_type")); err == nil && strings.TrimSpace(string(t)) != "0" {
		w.Problem = name + " uses a changed MAC address, not the one the card listens for while asleep"
		return w
	}
	state, err := os.ReadFile(filepath.Join(dir, "device", "power", "wakeup"))
	if err != nil || strings.TrimSpace(string(state)) == "enabled" {
		return w
	}
	w.Problem = "off on " + name
	w.Fix = "sudo ethtool -s " + name + " wol g"
	return w
}
