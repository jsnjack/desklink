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

func TestKeepAwakeForAWhile(t *testing.T) {
	k, f := newFakeAwake()
	k.hold(context.Background(), 60)
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
	k.hold(context.Background(), dlnet.KeepAwakeUntilReturn)
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
	k.hold(context.Background(), 240)
	k.stop()
	if !f.cancelled || f.released != 1 {
		t.Errorf("timer cancelled %v, released %d times", f.cancelled, f.released)
	}
}

func TestKeepAwakeOffHoldsNothing(t *testing.T) {
	k, f := newFakeAwake()
	k.hold(context.Background(), dlnet.KeepAwakeOff)
	if f.held != 0 {
		t.Errorf("held %d times with keep-awake off", f.held)
	}
}

func TestKeepAwakeValues(t *testing.T) {
	for _, m := range []int{0, -1, 60, 240, dlnet.KeepAwakeMaxMinutes} {
		if !validKeepAwake(m) {
			t.Errorf("%d refused", m)
		}
	}
	for _, m := range []int{-2, dlnet.KeepAwakeMaxMinutes + 1} {
		if validKeepAwake(m) {
			t.Errorf("%d accepted", m)
		}
	}
}
