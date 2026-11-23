package daemon

import (
	"errors"
	"log/slog"
	stdnet "net"
	"net/netip"
	"strings"
)

// routeProbe is a documentation address (RFC 5737). Connecting a UDP socket
// to it sends nothing; it only makes the kernel pick the default route's
// source address.
const routeProbe = "192.0.2.1:9"

// bridgePrefixes name container and VM bridges, whose addresses the tablet
// cannot reach even though they lie in private ranges.
var bridgePrefixes = []string{"lxdbr", "docker", "virbr", "podman", "br-", "veth", "cni"}

var errNoAddress = errors.New("no LAN address within allowed_subnets; set advertise_ip in config.json")

// advertiseIP returns the address put in the pairing QR code and used to find
// the Wake-on-LAN MAC: advertise_ip from the config when set, otherwise the
// source address of the default route, otherwise the first address of an
// interface that is up and not a container bridge. A detected address must
// lie within allowed_subnets and not be loopback.
func (d *Daemon) advertiseIP() (netip.Addr, error) {
	if s := d.opts.Config.AdvertiseIP; s != "" {
		return netip.ParseAddr(s)
	}
	usable := func(a netip.Addr) bool { return a.Is4() && !a.IsLoopback() && d.allow(a) }
	if a, ok := defaultRouteSource(); ok && usable(a) {
		return a, nil
	}
	ifaces, err := stdnet.Interfaces()
	if err != nil {
		return netip.Addr{}, errNoAddress
	}
	for _, ifc := range ifaces {
		if ifc.Flags&stdnet.FlagUp == 0 || isBridge(ifc.Name) {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if p, err := netip.ParsePrefix(a.String()); err == nil && usable(p.Addr()) {
				return p.Addr(), nil
			}
		}
	}
	return netip.Addr{}, errNoAddress
}

func defaultRouteSource() (netip.Addr, bool) {
	conn, err := stdnet.Dial("udp4", routeProbe)
	if err != nil {
		return netip.Addr{}, false
	}
	ap, err := netip.ParseAddrPort(conn.LocalAddr().String())
	if cerr := conn.Close(); cerr != nil {
		slog.Debug("close route probe socket", "err", cerr)
	}
	if err != nil {
		return netip.Addr{}, false
	}
	return ap.Addr().Unmap(), true
}

func isBridge(name string) bool {
	for _, p := range bridgePrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
