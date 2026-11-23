package client

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	stdnet "net"
	"net/netip"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// wolPort is the discard port that Wake-on-LAN senders use by convention.
	wolPort = 9
	// WakeTimeout is how long WakeAndWait waits for a host to boot or resume.
	WakeTimeout = 90 * time.Second
	wakeResend  = 10 * time.Second
	wakePoll    = 2 * time.Second
	probeTime   = 3 * time.Second
)

// ErrNotWoken means the host did not answer within WakeTimeout after the
// magic packets.
var ErrNotWoken = errors.New("host did not wake up; check that Wake-on-LAN is on in its BIOS and network settings")

// MagicPacket returns the Wake-on-LAN packet for mac: six 0xFF bytes and the
// address sixteen times.
func MagicPacket(mac string) ([]byte, error) {
	hw, err := stdnet.ParseMAC(mac)
	if err != nil || len(hw) != 6 {
		return nil, fmt.Errorf("bad MAC address %q", mac)
	}
	pkt := make([]byte, 0, 102)
	for range 6 {
		pkt = append(pkt, 0xFF)
	}
	for range 16 {
		pkt = append(pkt, hw...)
	}
	return pkt, nil
}

// wakeTargets are where the packet goes: the limited broadcast, which reaches
// the LAN this client is on, the host's /24 broadcast for routers that
// forward directed broadcasts from a VPN, and the host itself.
func wakeTargets(ip string) []string {
	out := []string{"255.255.255.255"}
	if a, err := netip.ParseAddr(ip); err == nil && a.Is4() {
		b := a.As4()
		b[3] = 255
		out = append(out, netip.AddrFrom4(b).String())
	}
	return append(out, ip)
}

// Wake sends a magic packet for mac towards the host at ip.
func Wake(mac, ip string) error {
	pkt, err := MagicPacket(mac)
	if err != nil {
		return err
	}
	conn, err := listenBroadcast()
	if err != nil {
		return err
	}
	defer func() {
		if err := conn.Close(); err != nil {
			slog.Debug("close Wake-on-LAN socket", "err", err)
		}
	}()
	var errs []error
	sent := 0
	for _, t := range wakeTargets(ip) {
		addr, err := stdnet.ResolveUDPAddr("udp4", stdnet.JoinHostPort(t, fmt.Sprint(wolPort)))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if _, err := conn.WriteToUDP(pkt, addr); err != nil {
			errs = append(errs, fmt.Errorf("send to %s: %w", t, err))
			continue
		}
		sent++
	}
	if sent == 0 {
		return fmt.Errorf("send Wake-on-LAN packet: %w", errors.Join(errs...))
	}
	return nil
}

// listenBroadcast opens a UDP socket that may send to broadcast addresses.
func listenBroadcast() (*stdnet.UDPConn, error) {
	lc := stdnet.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) {
			serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_BROADCAST, 1)
		}); err != nil {
			return err
		}
		return serr
	}}
	pc, err := lc.ListenPacket(context.Background(), "udp4", ":0")
	if err != nil {
		return nil, fmt.Errorf("open broadcast socket: %w", err)
	}
	return pc.(*stdnet.UDPConn), nil
}

// WakeAndWait returns the host reached at one of its addresses (Reach) at
// once when it answers. Otherwise, if its MAC is known, it sends magic packets
// towards every address, repeating them every few seconds, and returns once
// the host answers, or ErrNotWoken after WakeTimeout. With no MAC it returns
// the profile unchanged and leaves the error to the caller's connection
// attempt. status reports progress for the user.
func WakeAndWait(ctx context.Context, p Profile, status func(string)) (Profile, error) {
	if reached, _, err := Reach(ctx, p); err == nil || p.MAC == "" {
		return reached, nil
	}
	ctx, cancel := context.WithTimeout(ctx, WakeTimeout)
	defer cancel()
	status(fmt.Sprintf("Waking %s…", p.Name))
	var lastSent time.Time
	t := time.NewTicker(wakePoll)
	defer t.Stop()
	for {
		if time.Since(lastSent) >= wakeResend {
			for _, ip := range p.Candidates() {
				if err := Wake(p.MAC, ip); err != nil {
					return p, err
				}
			}
			lastSent = time.Now()
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return p, ErrNotWoken
			}
			return p, ctx.Err()
		case <-t.C:
		}
		if reached, _, err := Reach(ctx, p); err == nil {
			return reached, nil
		}
	}
}
