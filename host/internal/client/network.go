package client

import (
	stdnet "net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// defaultNetwork names the network when the route to the host is unknown.
const defaultNetwork = "default"

// Network kinds, as the host list shows them.
const (
	NetworkLAN = "LAN"
	NetworkVPN = "VPN"
)

// sysNet holds a directory per network interface; its type file is the
// interface's ARPHRD link type.
const sysNet = "/sys/class/net"

// linkKinds maps ARPHRD link types to network kinds: Ethernet and Wi-Fi are
// 1, WireGuard and tun devices have no link layer (65534), and PPP is 512.
var linkKinds = map[string]string{"1": NetworkLAN, "65534": NetworkVPN, "512": NetworkVPN}

// NetworkKey names the local interface that reaches ip, such as a WireGuard
// interface in the office or wlp1s0 at home, so the client can remember a
// quality level per network. Connecting a UDP socket sends nothing.
func NetworkKey(ip string) string {
	conn, err := stdnet.Dial("udp", stdnet.JoinHostPort(ip, strconv.Itoa(DefaultPort)))
	if err != nil {
		return defaultNetwork
	}
	local, ok := conn.LocalAddr().(*stdnet.UDPAddr)
	if cerr := conn.Close(); cerr != nil || !ok {
		return defaultNetwork
	}
	ifaces, err := stdnet.Interfaces()
	if err != nil {
		return defaultNetwork
	}
	want, ok := netip.AddrFromSlice(local.IP)
	if !ok {
		return defaultNetwork
	}
	for _, ifc := range ifaces {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if p, err := netip.ParsePrefix(a.String()); err == nil && p.Addr() == want.Unmap() {
				return ifc.Name
			}
		}
	}
	return defaultNetwork
}

// NetworkKind says whether the local interface iface (a NetworkKey) is a LAN
// or a VPN, by its link type, or "" when that is unknown.
func NetworkKind(iface string) string { return networkKind(sysNet, iface) }

func networkKind(sysfs, iface string) string {
	if iface == "" || iface == defaultNetwork || strings.ContainsRune(iface, '/') {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(sysfs, iface, "type"))
	if err != nil {
		return ""
	}
	return linkKinds[strings.TrimSpace(string(b))]
}
