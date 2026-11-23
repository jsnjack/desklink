package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"desklink/internal/input"
	dlnet "desklink/internal/net"
)

func TestMediaLost(t *testing.T) {
	described := time.Unix(1000, 0)
	at := func(d time.Duration) time.Time { return described.Add(d) }
	cases := []struct {
		name        string
		now         time.Time
		lastReceive time.Time
		want        bool
	}{
		{"a client still starting up has time", at(mediaTimeout - time.Millisecond), time.Time{}, false},
		{"a client that never sent anything is lost", at(mediaTimeout), time.Time{}, true},
		{"recent telemetry keeps the session", at(time.Minute), at(time.Minute - 250*time.Millisecond), false},
		{"a gap shorter than the timeout keeps it", at(time.Minute), at(time.Minute - mediaTimeout + time.Second), false},
		{"silence for the timeout ends it", at(time.Minute), at(time.Minute - mediaTimeout), true},
		{"a datagram from before the description does not count", at(mediaTimeout), described.Add(-time.Second), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mediaLost(c.now, described, c.lastReceive); got != c.want {
				t.Errorf("mediaLost = %v, want %v", got, c.want)
			}
		})
	}
}

// fakeInjector records which keys are down, like the uinput devices would
// show them. afterRelease, when set, runs once ReleaseAll has lifted them.
type fakeInjector struct {
	mu           sync.Mutex
	down         map[uint16]bool
	released     int
	afterRelease func()
}

func newFakeInjector() *fakeInjector { return &fakeInjector{down: map[uint16]bool{}} }

func (f *fakeInjector) Handle(ev input.Event) error { return f.HandleAt(0, ev) }

func (f *fakeInjector) HandleAt(_ uint32, ev input.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ev.Kind == input.KindKey {
		f.down[ev.Code] = ev.Flags == 1
	}
	return nil
}

func (f *fakeInjector) Reconcile(uint32, input.Held) error { return nil }

func (f *fakeInjector) ReleaseAll() error {
	f.mu.Lock()
	f.released++
	clear(f.down)
	f.mu.Unlock()
	if f.afterRelease != nil {
		f.afterRelease()
	}
	return nil
}

func (f *fakeInjector) Close() error { return nil }

// held reports whether key code is down.
func (f *fakeInjector) held(code uint16) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.down[code]
}

// sessionFixture is a session with fakes for everything its input, control
// loop and cleanup touch when it has no capture, pipeline or sound.
type sessionFixture struct {
	s     *session
	in    *fakeInjector
	ctl   *testControl
	awake *fakeAwake
}

func newSessionFixture(t *testing.T) sessionFixture {
	t.Helper()
	f := sessionFixture{in: newFakeInjector(), ctl: newTestControl(tablet)}
	d := &Daemon{injector: f.in, ctl: f.ctl.sessionControl}
	d.awake, f.awake = newFakeAwake()
	ctx, cancel := context.WithCancelCause(context.Background())
	t.Cleanup(func() { cancel(nil) })
	f.s = &session{d: d, ctx: ctx, cancel: cancel, cursor: newClientCursor(), screenW: 2560, screenH: 1600, levelChanges: make(chan int, 1)}
	active, _, err := f.ctl.admit(tablet, func() { cancel(nil) })
	if err != nil {
		t.Fatal(err)
	}
	f.s.active = active
	return f
}

const keyCtrl = 29

func keyDown(code uint16) []dlnet.InputEvent {
	return []dlnet.InputEvent{{Kind: dlnet.InputKey, Flags: 1, Code: code}}
}

func TestInputAfterTheSessionEndedIsDropped(t *testing.T) {
	f := newSessionFixture(t)
	f.s.HandleInput(1, keyDown(keyCtrl))
	if !f.in.held(keyCtrl) {
		t.Fatal("a running session's key press was not injected")
	}
	if err := f.in.ReleaseAll(); err != nil {
		t.Fatal(err)
	}
	f.s.cancel(errRevoked)
	f.s.HandleInput(2, keyDown(keyCtrl))
	if f.in.held(keyCtrl) {
		t.Error("an ended session's key press was injected")
	}
}

func TestAQueuedMessageIsDroppedOnceTheSessionEnded(t *testing.T) {
	// select picks a ready case at random, so one run could pass by luck.
	for range 50 {
		f := newSessionFixture(t)
		msgs := make(chan dlnet.Message, 1)
		msgs <- dlnet.Message{Type: dlnet.MsgReqIDR, Payload: json.RawMessage(`{}`)}
		f.s.levelChanges <- 0
		f.s.cancel(errMediaLost)
		if err := f.s.serve(f.s.ctx, msgs); !errors.Is(err, errMediaLost) {
			t.Fatalf("serve returned %v, want the reason the session ended", err)
		}
		if f.s.lastKeyframe.Load() != 0 {
			t.Fatal("a message queued before the end was handled after it")
		}
	}
}

func TestCleanupTakesControlFromTheClientFirst(t *testing.T) {
	f := newSessionFixture(t)
	f.s.HandleInput(1, keyDown(keyCtrl))
	// A datagram from the client arrives just after cleanup lifted every
	// key; the session ended with an error, so nobody cancelled it before.
	f.in.afterRelease = func() { f.s.HandleInput(2, keyDown(keyCtrl)) }
	f.s.cleanup()
	if f.in.released != 1 {
		t.Fatalf("held input released %d times, want 1", f.in.released)
	}
	if f.in.held(keyCtrl) {
		t.Error("a key pressed as the session ended is still down after cleanup")
	}
	if f.s.ctx.Err() == nil {
		t.Error("the session's context is still live after cleanup")
	}
}

func TestWhatOutlastsTheSession(t *testing.T) {
	t.Run("a session ending in control keeps the computer awake", func(t *testing.T) {
		f := newSessionFixture(t)
		f.s.keepAwake = dlnet.KeepAwakeUntilReturn
		f.s.cleanup()
		if f.awake.held != 1 {
			t.Errorf("held %d times, want 1", f.awake.held)
		}
	})
	t.Run("a revoked session does not, but still locks the desk it unlocked", func(t *testing.T) {
		f := newSessionFixture(t)
		f.s.keepAwake = dlnet.KeepAwakeUntilReturn
		f.s.wasLocked = true
		if ended, err := f.ctl.revoke(tablet.ID, func() error { f.ctl.reg.remove(tablet.ID); return nil }); !ended || err != nil {
			t.Fatalf("revoke: ended %v, err %v", ended, err)
		}
		f.s.cleanup()
		if f.awake.held != 0 {
			t.Errorf("held %d times for an unpaired device, want 0", f.awake.held)
		}
		if n := f.ctl.locks.Load(); n != 1 {
			t.Errorf("desk locked %d times, want 1", n)
		}
	})
	t.Run("a replaced session leaves it to its successor", func(t *testing.T) {
		f := newSessionFixture(t)
		f.s.keepAwake = dlnet.KeepAwakeUntilReturn
		mustAdmit(t, f.ctl, tablet)
		f.s.cleanup()
		if f.awake.held != 0 {
			t.Errorf("held %d times, want 0", f.awake.held)
		}
	})
}

func TestHelloScale(t *testing.T) {
	cases := []struct {
		asked, want float64
	}{
		{0, defaultScale},
		{1, 1},
		{1.75, 1.75},
		{3, 3},
		{0.5, defaultScale},
		{-2, defaultScale},
		{3.01, defaultScale},
		{1e9, defaultScale},
		{math.NaN(), defaultScale},
		{math.Inf(1), defaultScale},
	}
	for _, c := range cases {
		if got := helloScale(c.asked); got != c.want {
			t.Errorf("helloScale(%v) = %v, want %v", c.asked, got, c.want)
		}
	}
}
