package client

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"

	"github.com/godbus/dbus/v5"
)

const (
	avahiBus      = "org.freedesktop.Avahi"
	avahiServer   = "org.freedesktop.Avahi.Server"
	avahiBrowser  = "org.freedesktop.Avahi.ServiceBrowser"
	avahiIfUnspec = int32(-1)
	avahiInet     = int32(0)
	// ServiceType is the DNS-SD type desklinkd announces.
	ServiceType = "_desklink._tcp"
)

// Nearby is a desklinkd host announcing itself on the local network.
type Nearby struct {
	// Name is the announced name, "DeskLink on <hostname>".
	Name        string
	IP          string
	Port        int
	Fingerprint string
}

// Host is the announced name without the "DeskLink on " prefix.
func (n Nearby) Host() string { return strings.TrimPrefix(n.Name, "DeskLink on ") }

// Discover browses mDNS for desklinkd hosts through Avahi until ctx ends,
// calling found for every resolved IPv4 announcement (one per interface the
// host announces on) and lost with the name of one that went away. An
// announcement is only a hint: connecting still pins the certificate.
func Discover(ctx context.Context, found func(Nearby), lost func(name string)) error {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("connect to the system bus: %w", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			slog.Debug("close system bus", "err", err)
		}
	}()
	// Subscribe before the browser exists: Avahi may report items at once.
	match := []dbus.MatchOption{dbus.WithMatchInterface(avahiBrowser)}
	if err := conn.AddMatchSignalContext(ctx, match...); err != nil {
		return fmt.Errorf("subscribe to Avahi: %w", err)
	}
	signals := make(chan *dbus.Signal, 32)
	conn.Signal(signals)
	var browser dbus.ObjectPath
	err = conn.Object(avahiBus, "/").CallWithContext(ctx, avahiServer+".ServiceBrowserNew", 0,
		avahiIfUnspec, avahiInet, ServiceType, "", uint32(0)).Store(&browser)
	if err != nil {
		return fmt.Errorf("browse mDNS (is avahi-daemon running?): %w", err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case sig := <-signals:
			if sig.Path != browser || len(sig.Body) < 5 {
				continue
			}
			name, _ := sig.Body[2].(string)
			switch sig.Name {
			case avahiBrowser + ".ItemNew":
				if n, ok := resolve(ctx, conn, sig.Body); ok {
					found(n)
				}
			case avahiBrowser + ".ItemRemove":
				lost(name)
			}
		}
	}
}

// resolve turns a browser item (interface, protocol, name, type, domain)
// into the host's address, port and fingerprint.
func resolve(ctx context.Context, conn *dbus.Conn, item []any) (Nearby, bool) {
	iface, _ := item[0].(int32)
	proto, _ := item[1].(int32)
	name, _ := item[2].(string)
	typ, _ := item[3].(string)
	domain, _ := item[4].(string)
	var (
		rIface, rProto, aProto int32
		rName, rType, rDomain  string
		host, address          string
		port                   uint16
		txt                    [][]byte
		flags                  uint32
	)
	err := conn.Object(avahiBus, "/").CallWithContext(ctx, avahiServer+".ResolveService", 0,
		iface, proto, name, typ, domain, avahiInet, uint32(0)).
		Store(&rIface, &rProto, &rName, &rType, &rDomain, &host, &aProto, &address, &port, &txt, &flags)
	if err != nil {
		slog.Debug("resolve mDNS announcement", "name", name, "err", err)
		return Nearby{}, false
	}
	ip, err := netip.ParseAddr(address)
	if err != nil || !ip.Is4() || ip.IsLoopback() {
		return Nearby{}, false
	}
	n := Nearby{Name: name, IP: ip.String(), Port: int(port)}
	for _, t := range txt {
		if fp, ok := strings.CutPrefix(string(t), "fp="); ok {
			n.Fingerprint = fp
		}
	}
	return n, n.Fingerprint != ""
}
