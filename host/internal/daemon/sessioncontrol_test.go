package daemon

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"desklink/internal/config"
)

// fakeRegistry stands in for peers.json.
type fakeRegistry struct {
	mu    sync.Mutex
	peers map[string][]byte
}

func (r *fakeRegistry) paired(id string, key []byte) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.peers[id]
	return ok && bytes.Equal(k, key)
}

func (r *fakeRegistry) remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.peers, id)
}

// testControl is a sessionControl over a fake registry whose lock only
// counts calls.
type testControl struct {
	*sessionControl
	reg   *fakeRegistry
	locks atomic.Int32
}

func newTestControl(peers ...config.Peer) *testControl {
	t := &testControl{reg: &fakeRegistry{peers: map[string][]byte{}}}
	for _, p := range peers {
		t.reg.peers[p.ID] = p.Key
	}
	t.sessionControl = &sessionControl{
		paired: t.reg.paired,
		lock:   func(context.Context) error { t.locks.Add(1); return nil },
	}
	return t
}

var (
	tablet = config.Peer{ID: "tablet", Key: []byte{1}}
	laptop = config.Peer{ID: "laptop", Key: []byte{2}}
)

// cancelCounter counts how often a session's context was cancelled.
type cancelCounter struct{ n atomic.Int32 }

func (c *cancelCounter) cancel() { c.n.Add(1) }

func mustAdmit(t *testing.T, c *testControl, p config.Peer) (*activeSession, *activeSession, *cancelCounter) {
	t.Helper()
	var cc cancelCounter
	mine, prev, err := c.admit(p, cc.cancel)
	if err != nil {
		t.Fatalf("admit %s: %v", p.ID, err)
	}
	return mine, prev, &cc
}

func TestAReplacementThatFailsBeforeSetupRelocksTheDesk(t *testing.T) {
	ctx := context.Background()
	c := newTestControl(tablet)
	a, _, _ := mustAdmit(t, c, tablet)
	// A reconnect from the same tablet replaces a, which had unlocked the desk.
	b, prev, _ := mustAdmit(t, c, tablet)
	if prev != a {
		t.Fatal("the new session did not replace the running one")
	}
	c.leave(ctx, a, true)
	c.finish(ctx, a)
	if n := c.locks.Load(); n != 0 {
		t.Fatalf("desk locked %d times while the replacement was starting", n)
	}
	// The replacement fails before it took over the relock.
	c.finish(ctx, b)
	if n := c.locks.Load(); n != 1 {
		t.Errorf("desk locked %d times after the failed replacement, want 1", n)
	}
	if c.current() != nil {
		t.Error("a session is still registered")
	}
}

func TestAHandoffBetweenWorkingSessionsKeepsTheDeskUnlocked(t *testing.T) {
	ctx := context.Background()
	c := newTestControl(tablet)
	a, _, _ := mustAdmit(t, c, tablet)
	b, _, _ := mustAdmit(t, c, tablet)
	c.leave(ctx, a, true)
	c.finish(ctx, a)
	if !c.takeRelock() {
		t.Fatal("the replacement did not get the relock")
	}
	if n := c.locks.Load(); n != 0 {
		t.Fatalf("desk locked %d times during a successful handoff", n)
	}
	// The replacement ends normally and locks the desk it holds.
	c.leave(ctx, b, true)
	c.finish(ctx, b)
	if n := c.locks.Load(); n != 1 {
		t.Errorf("desk locked %d times after the last session, want 1", n)
	}
}

func TestTheRelockPassesAlongAChainOfReplacements(t *testing.T) {
	ctx := context.Background()
	c := newTestControl(tablet)
	a, _, _ := mustAdmit(t, c, tablet)
	b, _, _ := mustAdmit(t, c, tablet)
	c.leave(ctx, a, true)
	c.finish(ctx, a)
	// b is replaced before it took the relock; c takes it instead.
	last, prev, _ := mustAdmit(t, c, tablet)
	if prev != b {
		t.Fatal("the third session did not replace the second")
	}
	c.finish(ctx, b)
	if n := c.locks.Load(); n != 0 {
		t.Fatalf("desk locked %d times while a session was still in control", n)
	}
	if !c.takeRelock() {
		t.Fatal("the relock was lost along the chain")
	}
	c.leave(ctx, last, true)
	c.finish(ctx, last)
	if n := c.locks.Load(); n != 1 {
		t.Errorf("desk locked %d times, want 1", n)
	}
}

func TestADeskThatWasUnlockedIsNeverLocked(t *testing.T) {
	ctx := context.Background()
	c := newTestControl(tablet)
	a, _, _ := mustAdmit(t, c, tablet)
	b, _, _ := mustAdmit(t, c, tablet)
	c.leave(ctx, a, false)
	c.finish(ctx, a)
	c.finish(ctx, b) // fails before setup
	alone, _, _ := mustAdmit(t, c, tablet)
	c.leave(ctx, alone, false)
	c.finish(ctx, alone)
	if n := c.locks.Load(); n != 0 {
		t.Errorf("desk locked %d times though nobody unlocked it", n)
	}
}

func TestASessionEndingAloneLocksTheDeskItUnlocked(t *testing.T) {
	ctx := context.Background()
	c := newTestControl(tablet)
	a, _, _ := mustAdmit(t, c, tablet)
	c.leave(ctx, a, true)
	c.finish(ctx, a)
	if n := c.locks.Load(); n != 1 {
		t.Errorf("desk locked %d times, want 1", n)
	}
}

func TestAnUnpairedDeviceCannotBecomeActive(t *testing.T) {
	c := newTestControl(tablet, laptop)
	// The tablet authenticated (its config.Peer is in hand) and is still
	// waiting to register when it is unpaired.
	if _, err := c.revoke(tablet.ID, func() error { c.reg.remove(tablet.ID); return nil }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.admit(tablet, func() {}); !errors.Is(err, errRevoked) {
		t.Fatalf("admit after unpairing: %v, want errRevoked", err)
	}
	if c.current() != nil {
		t.Error("the unpaired device became active")
	}
	// A device paired again under the same ID with a new key is a different
	// pairing: the old credentials do not get in either.
	c.reg.peers[laptop.ID] = []byte{9}
	if _, _, err := c.admit(laptop, func() {}); !errors.Is(err, errRevoked) {
		t.Errorf("admit with a replaced key: %v, want errRevoked", err)
	}
}

func TestUnpairingEndsOnlyThatDevicesSession(t *testing.T) {
	c := newTestControl(tablet, laptop)
	_, _, tabletCancel := mustAdmit(t, c, tablet)
	ended, err := c.revoke(laptop.ID, func() error { c.reg.remove(laptop.ID); return nil })
	if err != nil || ended || tabletCancel.n.Load() != 0 {
		t.Fatalf("unpairing the laptop: ended %v, err %v, tablet cancelled %d times", ended, err, tabletCancel.n.Load())
	}
	ended, err = c.revoke(tablet.ID, func() error { c.reg.remove(tablet.ID); return nil })
	if err != nil || !ended || tabletCancel.n.Load() != 1 {
		t.Errorf("unpairing the tablet: ended %v, err %v, cancelled %d times", ended, err, tabletCancel.n.Load())
	}
}

func TestUnpairingEndsTheSessionEvenIfSavingFails(t *testing.T) {
	c := newTestControl(tablet)
	_, _, cc := mustAdmit(t, c, tablet)
	saveErr := errors.New("disk full")
	ended, err := c.revoke(tablet.ID, func() error { c.reg.remove(tablet.ID); return saveErr })
	if !errors.Is(err, saveErr) || !ended || cc.n.Load() != 1 {
		t.Errorf("ended %v, err %v, cancelled %d times", ended, err, cc.n.Load())
	}
}

// Run with -race: admissions racing an unpairing never leave the device in
// control afterwards without its session being cancelled.
func TestAdmissionRacingRevocation(t *testing.T) {
	for range 200 {
		c := newTestControl(tablet)
		var wg sync.WaitGroup
		var admitted sync.Map // *activeSession -> *cancelCounter
		for range 4 {
			wg.Go(func() {
				var cc cancelCounter
				if mine, _, err := c.admit(tablet, cc.cancel); err == nil {
					admitted.Store(mine, &cc)
				}
			})
		}
		wg.Go(func() {
			if _, err := c.revoke(tablet.ID, func() error { c.reg.remove(tablet.ID); return nil }); err != nil {
				t.Error(err)
			}
		})
		wg.Wait()
		if _, _, err := c.admit(tablet, func() {}); !errors.Is(err, errRevoked) {
			t.Fatalf("admit after unpairing: %v", err)
		}
		// Whichever session was in control when the unpairing happened was
		// cancelled; any admitted later is impossible.
		if active := c.current(); active != nil {
			cc, _ := admitted.Load(active)
			if cc.(*cancelCounter).n.Load() == 0 {
				t.Fatal("the session in control after unpairing was not cancelled")
			}
		}
	}
}

// TestANewSessionWaitsForTheRelockBeforeIt: a replacement that ends before
// it took the relock over locks the desk as it finishes. A connection that
// comes during that lock must wait for it, not unlock the desk under it, and
// a stopping daemon must find the session still finishing.
func TestANewSessionWaitsForTheRelockBeforeIt(t *testing.T) {
	ctx := context.Background()
	c := newTestControl(tablet)
	locking, release := make(chan struct{}), make(chan struct{})
	c.lock = func(context.Context) error {
		close(locking)
		<-release
		return nil
	}
	a, _, _ := mustAdmit(t, c, tablet)
	b, _, _ := mustAdmit(t, c, tablet)
	c.leave(ctx, a, true)
	c.finish(ctx, a)
	finished := make(chan struct{})
	go func() {
		c.finish(ctx, b)
		close(finished)
	}()
	<-locking
	if c.current() != b {
		t.Error("the session relocking the desk is no longer registered, so a stopping daemon would not wait for it")
	}
	next, prev, _ := mustAdmit(t, c, tablet)
	if prev != b {
		t.Fatalf("a connection during the relock found %v before it, want the session relocking the desk", prev)
	}
	select {
	case <-b.done:
		t.Fatal("the session was done before its relock ended")
	default:
	}
	close(release)
	<-finished
	<-b.done
	if c.current() != next {
		t.Error("the new session is not in control after the relock")
	}
}

func TestTheSessionHistorySaysWhySessionsEnded(t *testing.T) {
	d := &Daemon{}
	live := context.Background()
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	cases := []struct {
		name  string
		setup func(c *testControl) *activeSession
		ctx   context.Context
		err   error
		want  string
	}{
		{"the client left", func(c *testControl) *activeSession { a, _, _ := mustAdmit(t, c, tablet); return a }, live, nil, "client left"},
		{"the session failed", func(c *testControl) *activeSession { a, _, _ := mustAdmit(t, c, tablet); return a }, live, errMediaLost, "failed: " + errMediaLost.Error()},
		{"another device took over", func(c *testControl) *activeSession {
			a, _, _ := mustAdmit(t, c, tablet)
			mustAdmit(t, c, laptop)
			return a
		}, ended, nil, "taken over by " + laptop.Name},
		{"the same device reconnected", func(c *testControl) *activeSession {
			a, _, _ := mustAdmit(t, c, tablet)
			mustAdmit(t, c, tablet)
			return a
		}, ended, nil, "reconnected"},
		{"the device was unpaired", func(c *testControl) *activeSession {
			a, _, _ := mustAdmit(t, c, tablet)
			_, _ = c.revoke(tablet.ID, func() error { c.reg.remove(tablet.ID); return nil })
			return a
		}, ended, nil, "device unpaired"},
		{"the daemon stopped", func(c *testControl) *activeSession { a, _, _ := mustAdmit(t, c, tablet); return a }, ended, nil, "desklinkd stopped"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctl := newTestControl(tablet, laptop)
			d.ctl = ctl.sessionControl
			mine := c.setup(ctl)
			if got := d.endReason(c.ctx, mine, c.err); got != c.want {
				t.Errorf("endReason = %q, want %q", got, c.want)
			}
		})
	}
}
