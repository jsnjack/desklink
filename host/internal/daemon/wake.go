package daemon

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

// macFor returns the hardware address of the interface holding want.
func macFor(want netip.Addr) string {
	ifc, ok := interfaceFor(want)
	if !ok || len(ifc.HardwareAddr) == 0 {
		return ""
	}
	return ifc.HardwareAddr.String()
}

// wakeNote says why a client may be unable to wake this computer through the
// interface holding ip, and how to fix it; "" when nothing is known to be
// wrong. It reads sysfs: Wi-Fi cannot be woken by a magic packet, a changed
// MAC address is not the one the card listens for while asleep, and ethernet
// drivers enable the card's wakeup flag when Wake-on-LAN is armed.
func wakeNote(ip netip.Addr) string {
	ifc, ok := interfaceFor(ip)
	if !ok {
		return ""
	}
	return wakeNoteFor(ifc.Name)
}

func wakeNoteFor(name string) string {
	dir := filepath.Join(sysNet, name)
	if _, err := os.Stat(filepath.Join(dir, "wireless")); err == nil {
		return name + " is Wi-Fi; Wake-on-LAN needs a wired connection"
	}
	if t, err := os.ReadFile(filepath.Join(dir, "addr_assign_type")); err == nil && strings.TrimSpace(string(t)) != "0" {
		return name + " uses a changed MAC address, not the one the card listens for while asleep"
	}
	w, err := os.ReadFile(filepath.Join(dir, "device", "power", "wakeup"))
	if err != nil || strings.TrimSpace(string(w)) == "enabled" {
		return ""
	}
	return fmt.Sprintf("Wake-on-LAN is off on %s; turn it on now with `sudo ethtool -s %s wol g` "+
		"(the desklinkd package makes NetworkManager turn it on whenever a wired connection comes up)", name, name)
}
