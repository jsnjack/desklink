package daemon

import (
	"context"
	"testing"
	"time"

	dlnet "desklink/internal/net"
)

// fakeAwake records inhibitor calls and the scheduled release or suspend:
// the last one's delay and what fires it, and how many were scheduled.
type fakeAwake struct {
	held, released int
	delay          time.Duration
	fire           func()
	cancelled      bool
	scheduled      int
}

func newFakeAwake() (*keepAwake, *fakeAwake) {
	f := &fakeAwake{}
	k := newKeepAwake(func(context.Context) (func() error, error) {
		f.held++
		return func() error { f.released++; return nil }, nil
	})
	k.after = func(d time.Duration, fn func()) func() bool {
		f.delay, f.fire = d, fn
		f.scheduled++
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

// TestTheSuspendWaitsForAConnectionStartingItsSession: a connection that
// is admitted stops the suspend at once, since waiting for a replaced
// session, the hello and unlocking the desk can take longer than the grace
// period; if its session never starts, the suspend is scheduled again.
func TestTheSuspendWaitsForAConnectionStartingItsSession(t *testing.T) {
	setup := func() (*keepAwake, *fakeAwake, *int) {
		k, f := newFakeAwake()
		suspended := 0
		k.suspend = func(context.Context) error { suspended++; return nil }
		k.hold(context.Background(), dlnet.KeepAwakeSuspend, "tablet", always)
		return k, f, &suspended
	}
	t.Run("a session that starts ends the suspend", func(t *testing.T) {
		k, f, suspended := setup()
		rearm := k.deferSuspend()
		if !f.cancelled {
			t.Fatal("the admitted connection did not stop the suspend")
		}
		k.stop() // the session starts
		rearm()
		if f.scheduled != 1 || *suspended != 0 {
			t.Errorf("scheduled %d times, suspended %d times; want the suspend over", f.scheduled, *suspended)
		}
	})
	t.Run("a session that never starts schedules it again", func(t *testing.T) {
		k, f, suspended := setup()
		rearm := k.deferSuspend()
		rearm()
		if f.scheduled != 2 || f.delay != suspendGrace {
			t.Fatalf("scheduled %d times, the last after %v; want it again after %v", f.scheduled, f.delay, suspendGrace)
		}
		f.fire()
		if *suspended != 1 {
			t.Errorf("suspended %d times, want 1", *suspended)
		}
	})
	t.Run("a device unpaired meanwhile gets no suspend again", func(t *testing.T) {
		k, f, _ := setup()
		rearm := k.deferSuspend()
		k.forget("tablet")
		rearm()
		if f.scheduled != 1 {
			t.Errorf("scheduled %d times, want no second suspend", f.scheduled)
		}
	})
	t.Run("a connection that replaces a waiting one takes the suspend over", func(t *testing.T) {
		k, f, _ := setup()
		first := k.deferSuspend()
		second := k.deferSuspend()
		first() // the first connection gives way to the second
		if f.scheduled != 1 {
			t.Fatalf("scheduled %d times while the second connection starts its session, want 1", f.scheduled)
		}
		second() // whose session never starts either
		if f.scheduled != 2 {
			t.Errorf("scheduled %d times, want the suspend back once", f.scheduled)
		}
	})
	t.Run("a suspend that has fired is not scheduled again by the next connection", func(t *testing.T) {
		k, f, suspended := setup()
		f.fire()
		rearm := k.deferSuspend()
		rearm()
		if f.scheduled != 1 || *suspended != 1 {
			t.Errorf("scheduled %d times, suspended %d times; want one of each", f.scheduled, *suspended)
		}
	})
	t.Run("without a suspend scheduled there is nothing to defer", func(t *testing.T) {
		k, f := newFakeAwake()
		k.hold(context.Background(), dlnet.KeepAwakeUntilReturn, "tablet", always)
		k.deferSuspend()()
		if f.released != 0 || f.scheduled != 0 {
			t.Errorf("released %d, scheduled %d; want the hold kept", f.released, f.scheduled)
		}
	})
}
