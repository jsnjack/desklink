package desktop

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/godbus/dbus/v5"
)

const (
	avahiBus        = "org.freedesktop.Avahi"
	avahiServer     = "org.freedesktop.Avahi.Server"
	avahiEntryGroup = "org.freedesktop.Avahi.EntryGroup"
	avahiIfUnspec   = int32(-1)
	avahiProtoInet  = int32(0)
)

// ServiceType is the DNS-SD service type clients browse for.
const ServiceType = "_desklink._tcp"

// Announce publishes the control port on every local network with
// mDNS/DNS-SD through Avahi, so clients find the host on any LAN without
// knowing its address. The TXT record carries the certificate fingerprint,
// which clients match against the host they paired with; an announcement
// proves nothing by itself, the pinned certificate does. Avahi withdraws the
// announcement when the D-Bus connection closes; the returned function
// withdraws it earlier.
func (b *Bus) Announce(ctx context.Context, name string, port uint16, fingerprint string) (func(), error) {
	var path dbus.ObjectPath
	if err := b.System.Object(avahiBus, "/").CallWithContext(ctx, avahiServer+".EntryGroupNew", 0).Store(&path); err != nil {
		return nil, fmt.Errorf("create Avahi entry group (is avahi-daemon running?): %w", err)
	}
	group := b.System.Object(avahiBus, path)
	withdraw := func() {
		if err := group.Call(avahiEntryGroup+".Free", 0).Err; err != nil {
			slog.Debug("withdraw mDNS announcement", "err", err)
		}
	}
	txt := [][]byte{[]byte("fp=" + fingerprint), []byte("v=1")}
	if err := group.CallWithContext(ctx, avahiEntryGroup+".AddService", 0,
		avahiIfUnspec, avahiProtoInet, uint32(0), name, ServiceType, "", "", port, txt).Err; err != nil {
		withdraw()
		return nil, fmt.Errorf("add mDNS service: %w", err)
	}
	if err := group.CallWithContext(ctx, avahiEntryGroup+".Commit", 0).Err; err != nil {
		withdraw()
		return nil, fmt.Errorf("publish mDNS service: %w", err)
	}
	return withdraw, nil
}
