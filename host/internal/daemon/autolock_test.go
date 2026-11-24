package daemon

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"desklink/internal/desktop"
	"desklink/internal/input"
)

func TestShouldAutolock(t *testing.T) {
	now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		service string
		age     time.Duration
		want    bool
	}{
		{"automatic login that just started", "gdm-autologin", 20 * time.Second, true},
		{"automatic login hours ago, daemon restarted", "gdm-autologin", 3 * time.Hour, false},
		{"password login", "gdm-password", 20 * time.Second, false},
		{"fingerprint login", "gdm-fingerprint", 5 * time.Second, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldAutolock(tt.service, now.Add(-tt.age), now); got != tt.want {
				t.Errorf("shouldAutolock(%q, %v ago) = %v, want %v", tt.service, tt.age, got, tt.want)
			}
		})
	}
}

// desklinkd warns at startup unless GDM logs the user back in whenever the
// session ends, and says how to turn that on.
func TestAutologinWarning(t *testing.T) {
	if got := autologinWarning(desktop.GDMLogin{Automatic: true, Timed: true, TimedDelay: 10}); got != "" {
		t.Errorf("on: warned %q", got)
	}
	if got := autologinWarning(desktop.GDMLogin{Timed: true, TimedDelay: 10}); got != "" {
		t.Errorf("timed login also covers boot: warned %q", got)
	}
	for name, l := range map[string]desktop.GDMLogin{"off": {}, "at boot only": {Automatic: true}} {
		if got := autologinWarning(l); !strings.Contains(got, "desklinkd autologin on") {
			t.Errorf("%s: %q does not say how to fix it", name, got)
		}
	}
}

// A relock a killed run left is paid only in the login session it was owed
// to, never in one opened since, at the desk or by automatic login.
func TestRelockStillOwed(t *testing.T) {
	started := time.Date(2026, 11, 24, 23, 48, 0, 610495000, time.Local)
	if !relockStillOwed(started.UnixMicro(), started) {
		t.Error("the same login session no longer owes its relock")
	}
	if relockStillOwed(started.UnixMicro(), started.Add(2*time.Hour)) {
		t.Error("a later login session is locked for an earlier one")
	}
}

// recordingInjector keeps the events injected through it.
type recordingInjector struct {
	mu  sync.Mutex
	evs []input.Event
}

func (r *recordingInjector) Handle(ev input.Event) error { return r.HandleAt(0, ev) }
func (r *recordingInjector) HandleAt(_ uint32, ev input.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, ev)
	return nil
}
func (r *recordingInjector) Reconcile(uint32, input.Held) error { return nil }
func (r *recordingInjector) ReleaseAll() error                  { return nil }
func (r *recordingInjector) Close() error                       { return nil }

// After locking a session GDM just opened, desklinkd shows the lock screen
// again with pointer moves that GNOME counts as user activity and that
// leave the pointer where it was.
func TestShowLockScreenNudgesThePointerBack(t *testing.T) {
	saved := lockScreenNudges
	lockScreenNudges = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { lockScreenNudges = saved })
	rec := &recordingInjector{}
	d := &Daemon{injector: rec}
	d.showLockScreen(context.Background())
	if len(rec.evs) != 4 {
		t.Fatalf("injected %d events, want two nudges of two moves", len(rec.evs))
	}
	var x, y int32
	for _, ev := range rec.evs {
		if ev.Kind != input.KindRelMove {
			t.Fatalf("injected %+v, want only relative moves (a key could open the password prompt)", ev)
		}
		x, y = x+ev.X, y+ev.Y
	}
	if x != 0 || y != 0 {
		t.Errorf("the pointer ends %d,%d from where it was", x, y)
	}
	t.Run("not after the daemon stopped", func(t *testing.T) {
		rec := &recordingInjector{}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		(&Daemon{injector: rec}).showLockScreen(ctx)
		if len(rec.evs) != 0 {
			t.Errorf("injected %d events after the daemon stopped", len(rec.evs))
		}
	})
}
