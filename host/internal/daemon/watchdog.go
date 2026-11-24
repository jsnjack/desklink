package daemon

import (
	"context"
	"fmt"
	"log/slog"
	stdnet "net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"desklink/internal/client"
)

// The user unit is Type=notify with WatchdogSec (sd_notify(3)): desklinkd
// sends READY=1 once it listens, then WATCHDOG=1 each time its own /health
// answers, so systemd restarts a daemon that hangs as well as one that exits.

// notify sends state to systemd's notification socket. Without NOTIFY_SOCKET,
// when desklinkd does not run under systemd, it does nothing.
func notify(state string) error {
	socket := os.Getenv("NOTIFY_SOCKET")
	if socket == "" {
		return nil
	}
	if strings.HasPrefix(socket, "@") {
		socket = "\x00" + socket[1:]
	}
	conn, err := stdnet.DialUnix("unixgram", nil, &stdnet.UnixAddr{Name: socket, Net: "unixgram"})
	if err != nil {
		return fmt.Errorf("connect to systemd's notification socket: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte(state)); err != nil {
		return fmt.Errorf("notify systemd: %w", err)
	}
	return nil
}

// watchdogInterval is how often systemd expects WATCHDOG=1: WATCHDOG_USEC,
// unless WATCHDOG_PID names another process. Zero means no watchdog.
func watchdogInterval(getenv func(string) string, pid int) time.Duration {
	usec, err := strconv.ParseInt(getenv("WATCHDOG_USEC"), 10, 64)
	if err != nil || usec <= 0 {
		return 0
	}
	if p := getenv("WATCHDOG_PID"); p != "" && p != strconv.Itoa(pid) {
		return 0
	}
	return time.Duration(usec) * time.Microsecond
}

// probeTarget is where the watchdog asks for /health: the control server's
// address, or loopback when it listens on every address. ok is false when
// allowed_subnets would drop a connection from there.
func probeTarget(addr stdnet.Addr, allow func(netip.Addr) bool) (ip string, port int, ok bool) {
	ap, err := netip.ParseAddrPort(addr.String())
	if err != nil {
		return "", 0, false
	}
	host := ap.Addr().Unmap()
	if host.IsUnspecified() {
		host = netip.AddrFrom4([4]byte{127, 0, 0, 1})
	}
	if !allow(host) {
		return "", 0, false
	}
	return host.String(), int(ap.Port()), true
}

// watchdog checks the daemon every third of interval and pets systemd's
// watchdog when healthy reports no error, until ctx ends. A daemon whose
// check keeps failing for interval is killed and restarted by systemd.
func watchdog(ctx context.Context, interval time.Duration, healthy func(context.Context) error, pet func() error) {
	every := interval / 3
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		cctx, cancel := context.WithTimeout(ctx, every)
		err := healthy(cctx)
		cancel()
		switch {
		case err == nil:
			if err := pet(); err != nil {
				slog.Warn("tell systemd desklinkd is alive", "err", err)
			}
		case ctx.Err() == nil:
			slog.Warn("desklinkd's own /health did not answer; systemd restarts it if this goes on", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// superviseStartup tells systemd that desklinkd is ready once the control
// server (whose address arrives on control) and the local socket (local
// closes) listen, then runs the watchdog when the unit asks for one.
func (d *Daemon) superviseStartup(ctx context.Context, control <-chan stdnet.Addr, local <-chan struct{}) {
	var addr stdnet.Addr
	select {
	case addr = <-control:
	case <-ctx.Done():
		return
	}
	select {
	case <-local:
	case <-ctx.Done():
		return
	}
	if err := notify("READY=1"); err != nil {
		slog.Warn("tell systemd desklinkd is ready", "err", err)
	}
	interval := watchdogInterval(os.Getenv, os.Getpid())
	if interval == 0 {
		return
	}
	pet := func() error { return notify("WATCHDOG=1") }
	ip, port, ok := probeTarget(addr, d.allow)
	if !ok {
		slog.Info("allowed_subnets leaves out the control server's own address, so the watchdog checks only that desklinkd runs")
		watchdog(ctx, interval, func(context.Context) error { return nil }, pet)
		return
	}
	watchdog(ctx, interval, func(ctx context.Context) error {
		_, err := client.Health(ctx, ip, port, d.fingerprint)
		return err
	}, pet)
}
