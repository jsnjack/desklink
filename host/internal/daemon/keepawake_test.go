package daemon

import (
	"context"
	"testing"
	"time"

	dlnet "desklink/internal/net"
)

// fakeAwake records inhibitor calls and the scheduled release.
type fakeAwake struct {
	held, released int
	delay          time.Duration
	fire           func()
	cancelled      bool
}

func newFakeAwake() (*keepAwake, *fakeAwake) {
	f := &fakeAwake{}
	k := newKeepAwake(func(context.Context) (func() error, error) {
		f.held++
		return func() error { f.released++; return nil }, nil
	})
	k.after = func(d time.Duration, fn func()) func() bool {
		f.delay, f.fire = d, fn
		return func() bool { f.cancelled = true; return true }
	}
	return k, f
}

// always is a hold's wanted for a device that stays paired.
func always() bool { return true }

func TestKeepAwakeForAWhile(t *testing.T) {
	k, f := newFakeAwake()
	k.hold(context.Background(), 60, "tablet", always)
	if f.held != 1 || f.delay != time.Hour {
		t.Fatalf("held %d times, release scheduled after %v", f.held, f.delay)
	}
	f.fire() // an hour later
	if f.released != 1 {
		t.Errorf("released %d times after the hour, want 1", f.released)
	}
}

func TestKeepAwakeUntilTheNextSession(t *testing.T) {
	k, f := newFakeAwake()
	k.hold(context.Background(), dlnet.KeepAwakeUntilReturn, "tablet", always)
	if f.held != 1 || f.fire != nil {
		t.Fatalf("held %d times, timed release %v", f.held, f.fire != nil)
	}
	k.stop() // a session starts
	if f.released != 1 {
		t.Errorf("released %d times when the session started, want 1", f.released)
	}
	k.stop()
	if f.released != 1 {
		t.Errorf("a second stop released again")
	}
}

func TestAnEarlySessionCancelsTheTimedHold(t *testing.T) {
	k, f := newFakeAwake()
	k.hold(context.Background(), 240, "tablet", always)
	k.stop()
	if !f.cancelled || f.released != 1 {
		t.Errorf("timer cancelled %v, released %d times", f.cancelled, f.released)
	}
}

func TestKeepAwakeOffHoldsNothing(t *testing.T) {
	k, f := newFakeAwake()
	k.hold(context.Background(), dlnet.KeepAwakeOff, "tablet", always)
	if f.held != 0 {
		t.Errorf("held %d times with keep-awake off", f.held)
	}
}

func TestUnpairingTheDeviceEndsItsHold(t *testing.T) {
	k, f := newFakeAwake()
	k.hold(context.Background(), dlnet.KeepAwakeUntilReturn, "tablet", always)
	k.forget("laptop")
	if f.released != 0 {
		t.Fatalf("unpairing another device released the hold %d times", f.released)
	}
	k.forget("tablet")
	if f.released != 1 {
		t.Errorf("released %d times after unpairing the device that asked, want 1", f.released)
	}
	k.hold(context.Background(), 60, "tablet", always)
	k.forget("tablet")
	if !f.cancelled || f.released != 2 {
		t.Errorf("a timed hold: timer cancelled %v, released %d times in all, want 2", f.cancelled, f.released)
	}
}

func TestADeviceUnpairedWhileTheHoldStartsGetsNone(t *testing.T) {
	k, f := newFakeAwake()
	// The device is unpaired while the inhibitor is being taken: forget
	// found nothing to end, so the hold must notice by itself.
	k.hold(context.Background(), dlnet.KeepAwakeUntilReturn, "tablet", func() bool { return false })
	if f.held != 1 || f.released != 1 {
		t.Errorf("held %d and released %d times, want the inhibitor given back at once", f.held, f.released)
	}
	k.stop()
	if f.released != 1 {
		t.Errorf("released %d times, want 1: nothing was held", f.released)
	}
}

func TestKeepAwakeValues(t *testing.T) {
	for _, m := range []int{0, -1, dlnet.KeepAwakeSuspend, 60, 240, dlnet.KeepAwakeMaxMinutes} {
		if !validKeepAwake(m) {
			t.Errorf("%d refused", m)
		}
	}
	for _, m := range []int{-3, dlnet.KeepAwakeMaxMinutes + 1} {
		if validKeepAwake(m) {
			t.Errorf("%d accepted", m)
		}
	}
}

func TestSuspendAfterTheSession(t *testing.T) {
	setup := func() (*keepAwake, *fakeAwake, *int) {
		k, f := newFakeAwake()
		suspended := 0
		k.suspend = func(context.Context) error { suspended++; return nil }
		return k, f, &suspended
	}
	t.Run("the computer suspends once the grace period has passed", func(t *testing.T) {
		k, f, suspended := setup()
		k.hold(context.Background(), dlnet.KeepAwakeSuspend, "tablet", always)
		if f.held != 0 || f.delay != suspendGrace {
			t.Fatalf("held %d inhibitors, suspend scheduled after %v; want none and %v", f.held, f.delay, suspendGrace)
		}
		f.fire()
		if *suspended != 1 {
			t.Errorf("suspended %d times, want 1", *suspended)
		}
	})
	t.Run("a session starting within the grace period cancels it", func(t *testing.T) {
		k, f, suspended := setup()
		k.hold(context.Background(), dlnet.KeepAwakeSuspend, "tablet", always)
		k.stop()
		if !f.cancelled || *suspended != 0 {
			t.Errorf("cancelled %v, suspended %d times; want the suspend cancelled", f.cancelled, *suspended)
		}
	})
	t.Run("unpairing the device cancels it", func(t *testing.T) {
		k, f, suspended := setup()
		k.hold(context.Background(), dlnet.KeepAwakeSuspend, "tablet", always)
		k.forget("tablet")
		if !f.cancelled || *suspended != 0 {
			t.Errorf("cancelled %v, suspended %d times; want the suspend cancelled", f.cancelled, *suspended)
		}
	})
	t.Run("a device unpaired before the suspend fires gets nothing", func(t *testing.T) {
		k, f, suspended := setup()
		k.hold(context.Background(), dlnet.KeepAwakeSuspend, "tablet", func() bool { return false })
		f.fire()
		if *suspended != 0 {
			t.Errorf("suspended %d times for an unpaired device, want 0", *suspended)
		}
	})
}
