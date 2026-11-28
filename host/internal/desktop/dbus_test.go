package desktop

import (
	"bufio"
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// privateBus starts a dbus-daemon of its own and returns its address, so
// tests can run fake services without touching the desktop's buses.
func privateBus(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("dbus-daemon"); err != nil {
		t.Skip("dbus-daemon is not installed")
	}
	cmd := exec.Command("dbus-daemon", "--session", "--nofork", "--print-address=1", "--address=unix:dir="+t.TempDir())
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	addr, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("read the bus address: %v", err)
	}
	return strings.TrimSpace(addr)
}

func connect(t *testing.T, addr string) *dbus.Conn {
	t.Helper()
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

const (
	fakeUserPath    = dbus.ObjectPath("/org/freedesktop/login1/user/self")
	fakeSessionPath = dbus.ObjectPath("/org/freedesktop/login1/session/self")
)

// fakeLogind is logind with one user whose graphical session is the one at
// fakeSessionPath, or none when noDisplay is set.
type fakeLogind struct {
	mu sync.Mutex
	// locked is the session's LockedHint; refuseUnlock makes Session.Unlock
	// fail; hold, when not nil, keeps LockedHint reads from answering until
	// it closes.
	locked, refuseUnlock, noDisplay bool
	hold                            chan struct{}
	// unlocks counts Session.Unlock calls; unlockedAll is set once
	// Manager.UnlockSessions is called.
	unlocks     int
	unlockedAll bool
}

type fakeManager struct{ l *fakeLogind }

func (m fakeManager) GetUser(uint32) (dbus.ObjectPath, *dbus.Error) { return fakeUserPath, nil }

func (m fakeManager) UnlockSessions() *dbus.Error {
	m.l.mu.Lock()
	defer m.l.mu.Unlock()
	m.l.unlockedAll = true
	return nil
}

type fakeSession struct{ l *fakeLogind }

func (s fakeSession) Unlock() *dbus.Error {
	s.l.mu.Lock()
	defer s.l.mu.Unlock()
	s.l.unlocks++
	if s.l.refuseUnlock {
		return dbus.NewError("org.freedesktop.DBus.Error.AccessDenied", nil)
	}
	s.l.locked = false
	return nil
}

func (s fakeSession) Lock() *dbus.Error { return nil }

// fakeProperties answers org.freedesktop.DBus.Properties.Get with get.
type fakeProperties struct {
	get func(iface, name string) (dbus.Variant, *dbus.Error)
}

func (p fakeProperties) Get(iface, name string) (dbus.Variant, *dbus.Error) {
	return p.get(iface, name)
}

// serve exports l as org.freedesktop.login1 on the bus at addr.
func (l *fakeLogind) serve(t *testing.T, addr string) {
	t.Helper()
	conn := connect(t, addr)
	unknown := dbus.NewError("org.freedesktop.DBus.Error.UnknownProperty", nil)
	exports := []struct {
		v     any
		path  dbus.ObjectPath
		iface string
	}{
		{fakeManager{l}, login1Path, login1Bus + ".Manager"},
		{fakeSession{l}, fakeSessionPath, login1Bus + ".Session"},
		{fakeProperties{func(iface, name string) (dbus.Variant, *dbus.Error) {
			if iface != login1Bus+".User" || name != "Display" {
				return dbus.Variant{}, unknown
			}
			display := struct {
				ID   string
				Path dbus.ObjectPath
			}{"2", fakeSessionPath}
			if l.noDisplay {
				display.ID, display.Path = "", "/"
			}
			return dbus.MakeVariant(display), nil
		}}, fakeUserPath, "org.freedesktop.DBus.Properties"},
		{fakeProperties{func(iface, name string) (dbus.Variant, *dbus.Error) {
			switch {
			case iface != login1Bus+".Session":
				return dbus.Variant{}, unknown
			case name == "Name":
				return dbus.MakeVariant("jsn"), nil
			case name == "Type":
				return dbus.MakeVariant("wayland"), nil
			case name != "LockedHint":
				return dbus.Variant{}, unknown
			}
			if l.hold != nil {
				<-l.hold
			}
			l.mu.Lock()
			defer l.mu.Unlock()
			return dbus.MakeVariant(l.locked), nil
		}}, fakeSessionPath, "org.freedesktop.DBus.Properties"},
	}
	for _, e := range exports {
		if err := conn.Export(e.v, e.path, e.iface); err != nil {
			t.Fatal(err)
		}
	}
	if reply, err := conn.RequestName(login1Bus, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("own %s: %v, %v", login1Bus, reply, err)
	}
}

// TestUnlockSessionUnlocksOnlyItsOwnSession: Manager.UnlockSessions would
// unlock every session on the machine, other users' included, and the end of
// the session locks only the user's own again, so it is never called.
func TestUnlockSessionUnlocksOnlyItsOwnSession(t *testing.T) {
	cases := []struct {
		name                            string
		locked, refuseUnlock, noDisplay bool
		wantLocked, wantErr             bool
		wantUnlock                      int
	}{
		{"a locked session is unlocked", true, false, false, true, false, 1},
		{"an unlocked session is left alone", false, false, false, false, false, 0},
		{"a refused unlock leaves the desk locked", true, true, false, true, true, 1},
		{"without a graphical session nothing is unlocked", true, false, true, false, true, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			addr := privateBus(t)
			l := &fakeLogind{locked: c.locked, refuseUnlock: c.refuseUnlock, noDisplay: c.noDisplay}
			l.serve(t, addr)
			b := &Bus{System: connect(t, addr)}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			locked, err := b.UnlockSession(ctx)
			if locked != c.wantLocked || (err != nil) != c.wantErr {
				t.Errorf("UnlockSession = %v, %v; want locked %v, error %v", locked, err, c.wantLocked, c.wantErr)
			}
			l.mu.Lock()
			defer l.mu.Unlock()
			if l.unlocks != c.wantUnlock {
				t.Errorf("Session.Unlock called %d times, want %d", l.unlocks, c.wantUnlock)
			}
			if l.unlockedAll {
				t.Error("Manager.UnlockSessions was called")
			}
		})
	}
}

func TestGraphicalSession(t *testing.T) {
	cases := []struct {
		name      string
		noDisplay bool
		want      GraphicalSession
		wantErr   error
	}{
		{"the user's graphical session", false, GraphicalSession{User: "jsn", Type: "wayland"}, nil},
		{"nobody logged in to a desktop", true, GraphicalSession{}, ErrNoGraphicalSession},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			addr := privateBus(t)
			(&fakeLogind{noDisplay: c.noDisplay}).serve(t, addr)
			b := &Bus{System: connect(t, addr)}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			got, err := b.GraphicalSession(ctx)
			if got != c.want || !errors.Is(err, c.wantErr) {
				t.Errorf("GraphicalSession = %+v, %v; want %+v, %v", got, err, c.want, c.wantErr)
			}
		})
	}
}

// TestGNOMEServices: the names are asked about, never started.
func TestGNOMEServices(t *testing.T) {
	addr := privateBus(t)
	owner := connect(t, addr)
	for _, name := range []string{gnomeShellBus, mutterCastBus} {
		if reply, err := owner.RequestName(name, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
			t.Fatalf("own %s: %v, %v", name, reply, err)
		}
	}
	b := &Bus{Session: connect(t, addr)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := b.GNOMEServices(ctx)
	if want := (GNOMEServices{Shell: true, ScreenCast: true}); err != nil || got != want {
		t.Errorf("GNOMEServices = %+v, %v; want %+v", got, err, want)
	}
}

// TestLogindReadsEndAtTheirDeadline: a logind that stops answering a property
// read must not hold the session past the deadline its caller set.
func TestLogindReadsEndAtTheirDeadline(t *testing.T) {
	addr := privateBus(t)
	l := &fakeLogind{locked: true, hold: make(chan struct{})}
	t.Cleanup(func() { close(l.hold) })
	l.serve(t, addr)
	b := &Bus{System: connect(t, addr)}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := b.UnlockSession(ctx)
		done <- err
	}()
	select {
	case err := <-done:
		// The deadline has passed by then, so the unlock fails too.
		if err == nil {
			t.Error("UnlockSession succeeded after its deadline")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading LockedHint outlasted the caller's deadline")
	}
}
