package daemon

import (
	"context"
	"errors"
	stdnet "net"
	"net/netip"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	dlnet "desklink/internal/net"
)

func TestNotifyWritesTheStateToSystemdsSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notify")
	conn, err := stdnet.ListenUnixgram("unixgram", &stdnet.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	t.Setenv("NOTIFY_SOCKET", path)
	if err := notify("READY=1"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(buf[:n]); got != "READY=1" {
		t.Errorf("systemd got %q, want READY=1", got)
	}
}

func TestNotifyOutsideSystemdDoesNothing(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	if err := notify("READY=1"); err != nil {
		t.Errorf("notify without NOTIFY_SOCKET: %v", err)
	}
}

func TestWatchdogInterval(t *testing.T) {
	cases := []struct {
		name string
		usec string
		pid  string
		want time.Duration
	}{
		{"WatchdogSec=30", "30000000", "", 30 * time.Second},
		{"for this process", "30000000", "42", 30 * time.Second},
		{"for another process", "30000000", "7", 0},
		{"no watchdog", "", "", 0},
		{"garbage", "soon", "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := map[string]string{"WATCHDOG_USEC": c.usec, "WATCHDOG_PID": c.pid}
			if got := watchdogInterval(func(k string) string { return env[k] }, 42); got != c.want {
				t.Errorf("interval %v, want %v", got, c.want)
			}
		})
	}
}

func TestProbeTarget(t *testing.T) {
	lan := dlnet.AllowList([]netip.Prefix{netip.MustParsePrefix("192.168.0.0/16")})
	withLoopback := dlnet.AllowList([]netip.Prefix{netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("127.0.0.0/8")})
	cases := []struct {
		name   string
		addr   string
		allow  func(netip.Addr) bool
		wantIP string
		wantOK bool
	}{
		{"every address: loopback", "0.0.0.0:9443", withLoopback, "127.0.0.1", true},
		{"one address: that address", "192.168.178.11:9443", lan, "192.168.178.11", true},
		{"loopback left out of allowed_subnets", "0.0.0.0:9443", lan, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			addr := stdnet.TCPAddrFromAddrPort(netip.MustParseAddrPort(c.addr))
			ip, port, ok := probeTarget(addr, c.allow)
			if ok != c.wantOK || ip != c.wantIP || (ok && port != 9443) {
				t.Errorf("probe %s:%d ok=%v, want %s:9443 ok=%v", ip, port, ok, c.wantIP, c.wantOK)
			}
		})
	}
}

// The watchdog pets systemd only while the daemon answers, so a hung daemon
// stops being petted and systemd restarts it.
func TestWatchdogPetsOnlyWhileHealthy(t *testing.T) {
	var answering atomic.Bool
	answering.Store(true)
	var pets atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		watchdog(ctx, 30*time.Millisecond, func(context.Context) error {
			if !answering.Load() {
				return errors.New("no answer")
			}
			return nil
		}, func() error { pets.Add(1); return nil })
	}()
	waitFor(t, func() bool { return pets.Load() >= 2 })
	answering.Store(false)
	time.Sleep(25 * time.Millisecond) // let a check that began while answering finish
	before := pets.Load()
	time.Sleep(100 * time.Millisecond)
	if after := pets.Load(); after != before {
		t.Errorf("petted %d times while /health did not answer", after-before)
	}
	cancel()
	<-done
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
