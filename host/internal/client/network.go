package client

import (
	stdnet "net"
	"net/netip"
	"strconv"
)

// defaultNetwork names the network when the route to the host is unknown.
const defaultNetwork = "default"

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
