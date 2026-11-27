package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"desklink/internal/desktop"
	"desklink/internal/input"
	dlnet "desklink/internal/net"
	"desklink/internal/video"
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
// show them, and every event in order. afterRelease, when set, runs once
// ReleaseAll has lifted them. While gate is set, injecting a key signals
// entered and waits for gate to close.
type fakeInjector struct {
	mu           sync.Mutex
	down         map[uint16]bool
	events       []input.Event
	released     int
	afterRelease func()
	gate         chan struct{}
	entered      chan struct{}
}

func newFakeInjector() *fakeInjector { return &fakeInjector{down: map[uint16]bool{}} }

func (f *fakeInjector) Handle(ev input.Event) error { return f.HandleAt(0, ev) }

func (f *fakeInjector) HandleAt(_ uint32, ev input.Event) error {
	f.mu.Lock()
	gate, entered := f.gate, f.entered
	f.mu.Unlock()
	if gate != nil && ev.Kind == input.KindKey {
		entered <- struct{}{}
		<-gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
	if ev.Kind == input.KindKey {
		f.down[ev.Code] = ev.Flags == 1
	}
	return nil
}

// injected is every event injected so far, as "key 29 down", "button 1 up"
// and so on.
func (f *fakeInjector) injected() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.events))
	for _, ev := range f.events {
		state := "up"
		if ev.Flags == 1 {
			state = "down"
		}
		switch ev.Kind {
		case input.KindKey:
			out = append(out, fmt.Sprintf("key %d %s", ev.Code, state))
		case uint8(dlnet.InputMouseButton):
			out = append(out, fmt.Sprintf("button %d %s", ev.Code, state))
		}
	}
	return out
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
	s      *session
	in     *fakeInjector
	ctl    *testControl
	awake  *fakeAwake
	relock *fakeRelock
}

// fakeRelock is the relock recorded in state.json.
type fakeRelock struct {
	mu   sync.Mutex
	owed bool
}

func (r *fakeRelock) owe(context.Context) { r.mu.Lock(); r.owed = true; r.mu.Unlock() }
func (r *fakeRelock) forget()             { r.mu.Lock(); r.owed = false; r.mu.Unlock() }

func (r *fakeRelock) recorded() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.owed
}

func newSessionFixture(t *testing.T) sessionFixture {
	t.Helper()
	f := sessionFixture{in: newFakeInjector(), ctl: newTestControl(tablet), relock: &fakeRelock{}}
	d := &Daemon{injector: f.in, ctl: f.ctl.sessionControl, relock: f.relock}
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
	t.Run("a session asked to lock afterwards locks a desk it found unlocked", func(t *testing.T) {
		f := newSessionFixture(t)
		f.s.lockAfter = true
		f.s.cleanup()
		if n := f.ctl.locks.Load(); n != 1 {
			t.Errorf("desk locked %d times, want 1", n)
		}
	})
	t.Run("without it a desk found unlocked stays unlocked", func(t *testing.T) {
		f := newSessionFixture(t)
		f.s.cleanup()
		if n := f.ctl.locks.Load(); n != 0 {
			t.Errorf("desk locked %d times, want 0", n)
		}
	})
	t.Run("a replaced session asked to lock afterwards leaves the desk unlocked to its successor", func(t *testing.T) {
		f := newSessionFixture(t)
		f.s.setLockAfter(f.s.ctx, true)
		mustAdmit(t, f.ctl, tablet)
		f.s.cleanup()
		if n := f.ctl.locks.Load(); n != 0 {
			t.Errorf("desk locked %d times while another session took over, want 0", n)
		}
		if f.ctl.takeRelock() {
			t.Error("the successor was handed lock_after, so it would treat the desk as found locked")
		}
		if f.relock.recorded() {
			t.Error("the relock lock_after recorded outlasts the session that a successor replaced")
		}
	})
	t.Run("a replaced session that found the desk locked keeps the relock recorded for its successor", func(t *testing.T) {
		f := newSessionFixture(t)
		f.s.wasLocked, f.s.lockAfter = true, true
		f.relock.owe(f.s.ctx)
		mustAdmit(t, f.ctl, tablet)
		f.s.cleanup()
		if !f.ctl.takeRelock() || !f.relock.recorded() {
			t.Error("the relock of a desk found locked was not handed over")
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

// TestLockAfterIsRecordedForARestart: lock_after, from the hello or a
// lock_after message, is recorded as a relock owed, so that a desklinkd that
// dies before the session ends locks the desk on its next start.
func TestLockAfterIsRecordedForARestart(t *testing.T) {
	t.Run("turning it on records the relock and turning it off drops it", func(t *testing.T) {
		f := newSessionFixture(t)
		f.s.setLockAfter(f.s.ctx, true)
		if !f.relock.recorded() || !f.s.lockAfter {
			t.Fatal("lock_after on recorded no relock")
		}
		f.s.setLockAfter(f.s.ctx, false)
		if f.relock.recorded() || f.s.lockAfter {
			t.Error("lock_after off kept the relock recorded")
		}
	})
	t.Run("turning it off keeps the relock of a desk found locked", func(t *testing.T) {
		f := newSessionFixture(t)
		f.s.wasLocked = true
		f.relock.owe(f.s.ctx)
		f.s.setLockAfter(f.s.ctx, false)
		if !f.relock.recorded() {
			t.Error("lock_after off dropped the relock a desk found locked owes")
		}
	})
}

// TestASessionStartingBelowTheTopRecordsThatLevel: the session history's
// lowest level counts the level the session started at.
func TestASessionStartingBelowTheTopRecordsThatLevel(t *testing.T) {
	f := newSessionFixture(t)
	f.s.width, f.s.height, f.s.fps = 2560, 1600, 120
	f.s.startQuality(dlnet.Hello{QP: 16, Auto: true, Start: &dlnet.Level{Width: 2560, Height: 1600, FPS: 60}}, 16, 38)
	if got := f.s.levelName(f.s.worstLevel); got != "2560x1600@60" {
		t.Errorf("lowest level %q, want the start level 2560x1600@60", got)
	}
	if f.s.width != 2560 || f.s.fps != 60 {
		t.Errorf("the stream starts at %dx%d@%d, want the start level", f.s.width, f.s.height, f.s.fps)
	}
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

func TestNewQuality(t *testing.T) {
	low := &dlnet.Level{Width: 1280, Height: 800, FPS: 30}
	cases := []struct {
		name               string
		hello              dlnet.Hello
		wantQP, wantLevels int
		wantStart          video.Level
		wantAuto           bool
	}{
		{"auto starts at full size although the client remembers the smallest level",
			dlnet.Hello{QP: 12, Auto: true, Start: low}, 16, 5, video.Level{Width: 2560, Height: 1600, FPS: 60}, true},
		{"auto without a remembered level starts at the top",
			dlnet.Hello{QP: 12, Auto: true}, 16, 5, video.Level{Width: 2560, Height: 1600, FPS: 120}, true},
		{"fixed keeps one level and may go below min_qp",
			dlnet.Hello{QP: 12, Auto: true, Fixed: true, Start: low}, 12, 1, video.Level{Width: 2560, Height: 1600, FPS: 120}, false},
		{"fixed stops at the floor",
			dlnet.Hello{QP: 2, Fixed: true}, video.FixedMinQP, 1, video.Level{Width: 2560, Height: 1600, FPS: 120}, false},
		{"manual quality keeps min_qp too",
			dlnet.Hello{QP: 12}, 16, 1, video.Level{Width: 2560, Height: 1600, FPS: 120}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := newQuality(c.hello, 16, 38, 2560, 1600, 120)
			got := q.Levels()[q.Level()]
			if q.QP() != c.wantQP || len(q.Levels()) != c.wantLevels || got != c.wantStart || q.Auto() != c.wantAuto {
				t.Errorf("qp %d, %d levels, starts at %v, auto %v; want %d, %d, %v, %v",
					q.QP(), len(q.Levels()), got, q.Auto(), c.wantQP, c.wantLevels, c.wantStart, c.wantAuto)
			}
		})
	}
}

// fakeMutter is Mutter's monitor configuration for a desk with DP-5 and the
// built-in eDP-1, plus the virtual monitor Meta-0 while a cast runs. log
// records in order each layout applied, by the connectors it turns on, and
// the cast stopping.
type fakeMutter struct {
	st  desktop.DisplayState
	log []string
	// failApply and failState are how many ApplyLayout and DisplayState
	// calls fail next.
	failApply, failState int
	// afterCast is the layout Mutter applies by itself once the virtual
	// monitor has gone: the user's saved one, or, when nil, nothing.
	afterCast *desktop.DisplayLayout
	// castClosed is set when Mutter has closed the cast by itself; it still
	// reports the virtual monitor until stopCast, where it catches up.
	castClosed bool
	// casts maps each running cast's PipeWire node to the connector it
	// records; pending holds the nodes of virtual casts whose monitor Mutter
	// adds once a pipeline negotiates its size.
	casts    map[uint32]string
	pending  map[uint32]bool
	lastNode uint32
	virtuals int
	// failCast is how many casts fail to start next.
	failCast int
	// pipelines are the settings of every pipeline started, in order.
	pipelines []video.Settings
	// godbus makes every call on a context that has ended fail without
	// reaching Mutter, as godbus does: it does not even send it.
	godbus bool
	// linger is how many DisplayState calls still list a virtual monitor
	// after its cast stopped: Mutter frees it with the cast but updates its
	// monitor list only when idle. freed is that monitor's connector, and
	// freedLeft the calls left; applying a layout meanwhile crashes
	// gnome-shell, as it did on 2026-11-25.
	linger, freedLeft int
	freed             string
}

// ended reports a call that godbus would not send.
func (m *fakeMutter) ended(ctx context.Context) error {
	if m.godbus {
		return ctx.Err()
	}
	return nil
}

func fakeMonitor(connector, modeID string, w, h int32, scales ...float64) desktop.Monitor {
	return desktop.Monitor{Connector: connector, Modes: []desktop.DisplayMode{
		{ID: modeID, Width: w, Height: h, Refresh: 60, PreferredScale: 1, SupportedScales: scales, Current: true},
	}}
}

const virtualConnector = "Meta-0"

// deskBefore is the desk's layout before the session: the built-in panel at
// 133 % left of DP-5, which is primary.
func deskBefore() desktop.DisplayLayout {
	return desktop.DisplayLayout{LayoutMode: 1, Logical: []desktop.LogicalConfig{
		{X: 1920, Scale: 1, Primary: true, Monitors: []desktop.MonitorAssignment{{Connector: "DP-5", ModeID: "d"}}},
		{Y: 240, Scale: 1.3333333, Monitors: []desktop.MonitorAssignment{{Connector: "eDP-1", ModeID: "e"}}},
	}}
}

// linearDesk is the layout Mutter makes for the desk's monitors when it has
// none saved.
func linearDesk() desktop.DisplayLayout {
	return desktop.DisplayLayout{LayoutMode: 1, Logical: []desktop.LogicalConfig{
		{Scale: 1, Primary: true, Monitors: []desktop.MonitorAssignment{{Connector: "DP-5", ModeID: "d"}}},
		{X: 2560, Scale: 1.3333333, Monitors: []desktop.MonitorAssignment{{Connector: "eDP-1", ModeID: "e"}}},
	}}
}

func newFakeMutter() *fakeMutter {
	m := &fakeMutter{st: desktop.DisplayState{Serial: 1, LayoutMode: 1, Monitors: []desktop.Monitor{
		fakeMonitor("DP-5", "d", 2560, 1440, 1, 2),
		fakeMonitor("eDP-1", "e", 2560, 1600, 1, 1.3333333, 2),
	}}, casts: map[uint32]string{}, pending: map[uint32]bool{}}
	m.set(deskBefore())
	return m
}

// startCast adds the virtual monitor Meta-0 of the cast on node 0, the way
// Mutter does once the cast's stream is negotiated (addVirtual).
func (m *fakeMutter) startCast() { m.addVirtual(0, 2560, 1600) }

// addVirtual adds a virtual monitor of w x h for the cast on node and lays
// out the unknown monitor set from scratch as Mutter does: every monitor on,
// left to right, which moves the desk's monitors. Like Mutter, it names the
// monitor Meta-0, or Meta-1 and so on while Mutter lists those, so a screen
// replaced after the old one went is Meta-0 again; its mode is new.
func (m *fakeMutter) addVirtual(node uint32, w, h int32) {
	n := 0
	for slices.Contains(m.st.Connectors(), fmt.Sprintf("Meta-%d", n)) {
		n++
	}
	connector, modeID := fmt.Sprintf("Meta-%d", n), "v"
	if m.virtuals > 0 {
		modeID = fmt.Sprintf("v%d", m.virtuals)
	}
	m.virtuals++
	m.casts[node] = connector
	m.st.Monitors = append(m.st.Monitors, fakeMonitor(connector, modeID, w, h, 1, 1.25, 1.6666666, 2, 2.5))
	l := linearDesk()
	x := int32(4480)
	for _, mon := range m.st.Monitors {
		if strings.HasPrefix(mon.Connector, "Meta-") {
			l.Logical = append(l.Logical, desktop.LogicalConfig{X: x, Scale: 1, Monitors: []desktop.MonitorAssignment{{Connector: mon.Connector, ModeID: mon.Modes[0].ID}}})
			x += mon.Modes[0].Width
		}
	}
	m.set(l)
}

// Start records a desk monitor, as Mutter's RecordMonitor does.
func (m *fakeMutter) Start(ctx context.Context, connector string) (*desktop.Capture, error) {
	if err := m.ended(ctx); err != nil {
		return nil, err
	}
	if m.failCast > 0 {
		m.failCast--
		return nil, errors.New("Mutter refused the cast")
	}
	r, ok := m.st.MonitorRect(connector)
	if !ok {
		return nil, fmt.Errorf("unknown monitor %q", connector)
	}
	m.lastNode++
	m.casts[m.lastNode] = connector
	m.log = append(m.log, "record "+connector)
	return &desktop.Capture{NodeID: m.lastNode, X: r.X, Y: r.Y, Width: r.W, Height: r.H}, nil
}

// StartVirtual records a virtual monitor, which Mutter adds to the layout
// once a pipeline negotiates its size (startVideo).
func (m *fakeMutter) StartVirtual(ctx context.Context) (*desktop.Capture, error) {
	if err := m.ended(ctx); err != nil {
		return nil, err
	}
	if m.failCast > 0 {
		m.failCast--
		return nil, errors.New("Mutter refused the cast")
	}
	m.lastNode++
	m.pending[m.lastNode] = true
	m.log = append(m.log, "record virtual")
	return &desktop.Capture{NodeID: m.lastNode, Virtual: true}, nil
}

// startVideo starts a fake pipeline. The first one on a virtual cast's node
// gives the virtual monitor its mode, the pipeline's capture size.
func (m *fakeMutter) startVideo(s video.Settings, _ func(video.Frame)) (videoEncoder, error) {
	m.pipelines = append(m.pipelines, s)
	if m.pending[s.NodeID] {
		delete(m.pending, s.NodeID)
		m.addVirtual(s.NodeID, int32(s.CaptureWidth), int32(s.CaptureHeight))
	}
	return &fakeEncoder{errs: make(chan error, 1), done: make(chan struct{})}, nil
}

// fakeEncoder is a running video pipeline that does nothing.
type fakeEncoder struct {
	errs chan error
	done chan struct{}
	once sync.Once
}

func (e *fakeEncoder) Stop()                 { e.once.Do(func() { close(e.done) }) }
func (e *fakeEncoder) ForceKeyframe() bool   { return true }
func (e *fakeEncoder) SetQP(int)             {}
func (e *fakeEncoder) SetMaxFPS(int)         {}
func (e *fakeEncoder) SetScanning(bool)      {}
func (e *fakeEncoder) Errors() <-chan error  { return e.errs }
func (e *fakeEncoder) Done() <-chan struct{} { return e.done }

func (m *fakeMutter) set(l desktop.DisplayLayout) {
	m.st.Serial++
	m.st.Logical = nil
	for _, c := range l.Logical {
		lm := desktop.LogicalMonitor{X: c.X, Y: c.Y, Scale: c.Scale, Transform: c.Transform, Primary: c.Primary}
		for _, a := range c.Monitors {
			lm.Connectors = append(lm.Connectors, a.Connector)
		}
		m.st.Logical = append(m.st.Logical, lm)
	}
}

func (m *fakeMutter) layout(t *testing.T) desktop.DisplayLayout {
	t.Helper()
	l, err := m.st.CurrentLayout()
	if err != nil {
		t.Fatalf("no monitor is on: %v", err)
	}
	return l
}

func (m *fakeMutter) DisplayState(ctx context.Context) (desktop.DisplayState, error) {
	if err := m.ended(ctx); err != nil {
		return desktop.DisplayState{}, err
	}
	if m.failState > 0 {
		m.failState--
		return desktop.DisplayState{}, errors.New("no reply")
	}
	if m.freed != "" {
		if m.freedLeft == 0 {
			m.removeVirtual(m.freed)
			m.freed = ""
		} else {
			m.freedLeft--
		}
	}
	return m.st, nil
}

func (m *fakeMutter) ApplyLayout(ctx context.Context, serial uint32, l desktop.DisplayLayout) error {
	if err := m.ended(ctx); err != nil {
		return err
	}
	if m.failApply > 0 {
		m.failApply--
		m.log = append(m.log, "apply failed")
		return errors.New("refused")
	}
	if m.freed != "" {
		m.log = append(m.log, "gnome-shell crashed")
		return errors.New("apply monitor configuration: Remote peer disconnected")
	}
	if serial != m.st.Serial {
		return errors.New("the requested configuration is based on stale information")
	}
	var on []string
	for _, c := range l.Logical {
		for _, a := range c.Monitors {
			on = append(on, a.Connector)
		}
	}
	m.log = append(m.log, "apply "+strings.Join(on, " "))
	m.set(l)
	return nil
}

// RestoreLayout is desktop.Bus.RestoreLayout without its retries.
func (m *fakeMutter) RestoreLayout(ctx context.Context, l desktop.DisplayLayout) error {
	st, err := m.DisplayState(ctx)
	if err != nil {
		return err
	}
	if err := l.ValidFor(st); err != nil {
		return err
	}
	if cur, err := st.CurrentLayout(); err == nil && cur.Equal(l) {
		return nil
	}
	return m.ApplyLayout(ctx, st.Serial, l)
}

func (m *fakeMutter) stopCast(ctx context.Context, c *desktop.Capture) error {
	if err := m.ended(ctx); err != nil {
		return err
	}
	m.log = append(m.log, "stop cast")
	connector := m.casts[c.NodeID]
	delete(m.casts, c.NodeID)
	delete(m.pending, c.NodeID)
	if strings.HasPrefix(connector, "Meta-") {
		if m.linger > 0 {
			m.freed, m.freedLeft = connector, m.linger
		} else {
			m.removeVirtual(connector)
		}
	}
	if m.castClosed {
		return errors.New("object does not exist")
	}
	return nil
}

// removeVirtual takes a virtual monitor out of the layout, then applies the
// layout Mutter picks by itself, if any.
func (m *fakeMutter) removeVirtual(connector string) {
	m.st.Serial++
	m.st.Monitors = slices.DeleteFunc(m.st.Monitors, func(mon desktop.Monitor) bool { return mon.Connector == connector })
	m.st.Logical = slices.DeleteFunc(m.st.Logical, func(lm desktop.LogicalMonitor) bool { return slices.Contains(lm.Connectors, connector) })
	if m.afterCast != nil {
		m.log = append(m.log, "Mutter lays out the desk")
		m.set(*m.afterCast)
	}
}

func (m *fakeMutter) castOpen(ctx context.Context, _ *desktop.Capture) bool {
	return m.ended(ctx) == nil && !m.castClosed
}

// useFakeMutter gives the session m for its monitors, casts and pipelines,
// and an auto ladder for a 2560x1600 client at 120 fps.
func useFakeMutter(f sessionFixture, m *fakeMutter) {
	d := f.s.d
	d.display, d.stopCast, d.castOpen, d.cast, d.startVideo = m, m.stopCast, m.castOpen, m, m.startVideo
	d.opts.Config.MinQP, d.opts.Config.MaxQP = 16, 26
	f.s.codec, f.s.fps, f.s.requestedQP = video.CodecAV1, 120, 18
	f.s.quality = video.NewQualityController(video.QualityConfig{Requested: 18, MinQP: 16, MaxQP: 26, Levels: ladder(2560, 1600, 120), Auto: true})
}

// castingFixture is a session that has just started the cast of its own
// screen on m, for a desk that was locked or not as the client came. Once the
// virtual monitor has gone, m applies the desk's layout from before.
func castingFixture(t *testing.T, locked bool) (sessionFixture, *fakeMutter) {
	t.Helper()
	f := newSessionFixture(t)
	m := newFakeMutter()
	saved := deskBefore()
	m.afterCast = &saved
	useFakeMutter(f, m)
	f.s.wasLocked = locked
	f.s.width, f.s.height, f.s.scale = 2560, 1600, 2
	f.s.captureW, f.s.captureH, f.s.captureFPS = 2560, 1600, 120
	// What startVirtualCapture records before Mutter adds the virtual monitor.
	f.s.deskState, f.s.deskMonitors = m.st, m.st.Connectors()
	f.s.capture = &desktop.Capture{Virtual: true}
	m.startCast()
	return f, m
}

// ownScreenFixture is castingFixture with the own screen arranged and the
// log cleared.
func ownScreenFixture(t *testing.T, locked bool) (sessionFixture, *fakeMutter) {
	t.Helper()
	f, m := castingFixture(t, locked)
	f.s.arrangeVirtualScreen(f.s.ctx)
	if f.s.captured != virtualConnector {
		t.Fatalf("the session found %q, want the virtual monitor", f.s.captured)
	}
	m.log = nil
	return f, m
}

// beside is the desk's layout from before the session with the virtual
// screen at 200 % to its right, primary or not.
func beside(primary bool) desktop.DisplayLayout {
	l := deskBefore()
	l.Logical[0].Primary = !primary
	l.Logical = append(l.Logical, desktop.LogicalConfig{X: 4480, Scale: 2, Primary: primary, Monitors: []desktop.MonitorAssignment{{Connector: virtualConnector, ModeID: "v"}}})
	return l
}

func TestOwnScreenLayout(t *testing.T) {
	virtualOnly := desktop.DisplayLayout{LayoutMode: 1, Logical: []desktop.LogicalConfig{
		{Scale: 2, Primary: true, Monitors: []desktop.MonitorAssignment{{Connector: virtualConnector, ModeID: "v"}}},
	}}
	cases := []struct {
		name      string
		locked    bool
		failApply int
		want      desktop.DisplayLayout
		log       []string
	}{
		{"a locked desk's monitors go off, the virtual screen alone and primary", true, 0, virtualOnly, []string{"apply Meta-0"}},
		{"an unlocked desk keeps its layout and primary, the virtual screen right of it", false, 0, beside(false), []string{"apply DP-5 eDP-1 Meta-0"}},
		{"a locked desk keeps its monitors if Mutter refuses to turn them off", true, 1, beside(true), []string{"apply failed", "apply DP-5 eDP-1 Meta-0"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, m := castingFixture(t, c.locked)
			m.failApply = c.failApply
			f.s.arrangeVirtualScreen(f.s.ctx)
			if got := m.layout(t); !got.Equal(c.want) {
				t.Errorf("layout %+v, want %+v", got.Logical, c.want.Logical)
			}
			if !slices.Equal(m.log, c.log) {
				t.Errorf("applied %q, want %q", m.log, c.log)
			}
			if f.s.appliedScale != 2 {
				t.Errorf("applied scale %v, want 2", f.s.appliedScale)
			}
		})
	}
}

func TestOwnScreenInput(t *testing.T) {
	cases := []struct {
		name   string
		locked bool
		scale  float64
		// corners are where the frame's top-left and bottom-right corners
		// land on the absolute devices, which span the whole desktop.
		corners [2][2]float64
	}{
		{"with the desk off the frame spans the whole desktop", true, 0, [2][2]float64{{0, 0}, {1, 1}}},
		{"and still does after a scale change", true, 1.25, [2][2]float64{{0, 0}, {1, 1}}},
		{"beside the desk the frame maps onto the virtual screen's place", false, 0, [2][2]float64{{4480.0 / 5760, 0}, {1, 800.0 / 1440}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, _ := ownScreenFixture(t, c.locked)
			if c.scale != 0 {
				f.s.scale = c.scale
				if changed, err := f.s.rescaleVirtualScreen(f.s.ctx); err != nil || !changed {
					t.Fatalf("rescale: changed %v, err %v", changed, err)
				}
				f.s.updatePointerMap(f.s.ctx)
			}
			for i, frame := range [2]int32{0, absMax} {
				x, y := f.s.pointer.apply(frame, frame)
				wantX, wantY := c.corners[i][0]*absMax, c.corners[i][1]*absMax
				if math.Abs(float64(x)-wantX) > 1 || math.Abs(float64(y)-wantY) > 1 {
					t.Errorf("frame corner %d,%d maps to %d,%d, want %.0f,%.0f", frame, frame, x, y, wantX, wantY)
				}
			}
		})
	}
}

func TestOwnScreenPrimary(t *testing.T) {
	f, m := ownScreenFixture(t, false)
	steps := []struct {
		name    string
		primary bool
		want    desktop.DisplayLayout
		log     []string
	}{
		{"the client's screen becomes primary; only the primary moves", true, beside(true), []string{"apply DP-5 eDP-1 Meta-0"}},
		{"asking again changes nothing", true, beside(true), nil},
		{"the desk's primary from before gets it back", false, beside(false), []string{"apply DP-5 eDP-1 Meta-0"}},
	}
	for _, step := range steps {
		m.log = nil
		if err := f.s.movePrimary(f.s.ctx, step.primary); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if got := m.layout(t); !got.Equal(step.want) {
			t.Errorf("%s: layout %+v, want %+v", step.name, got.Logical, step.want.Logical)
		}
		if !slices.Equal(m.log, step.log) {
			t.Errorf("%s: applied %q, want %q", step.name, m.log, step.log)
		}
		if p := f.s.primaryState(); p == nil || *p != step.primary {
			t.Errorf("%s: session info reports primary %v, want %v", step.name, p, step.primary)
		}
	}

	t.Run("a mirroring session cannot move the primary, so it reports none", func(t *testing.T) {
		f := newSessionFixture(t)
		f.s.capture = &desktop.Capture{}
		if p := f.s.primaryState(); p != nil {
			t.Errorf("session info reports primary %v, want none", *p)
		}
	})

	t.Run("a virtual screen placed again stays primary", func(t *testing.T) {
		f, m := ownScreenFixture(t, false)
		if err := f.s.movePrimary(f.s.ctx, true); err != nil {
			t.Fatal(err)
		}
		if err := f.s.placeVirtualScreen(f.s.ctx); err != nil {
			t.Fatal(err)
		}
		if got := m.layout(t); !got.Equal(beside(true)) {
			t.Errorf("layout %+v, want the client's screen still primary", got.Logical)
		}
	})
	t.Run("a screen placed again keeps the client's choice", func(t *testing.T) {
		// A locked desk whose monitors Mutter refused to turn off: the
		// client's screen beside the desk, primary by default.
		f, m := castingFixture(t, true)
		m.failApply = 1
		f.s.arrangeVirtualScreen(f.s.ctx)
		if got := m.layout(t); !got.Equal(beside(true)) {
			t.Fatalf("layout %+v, want the client's screen primary beside the desk", got.Logical)
		}
		if err := f.s.movePrimary(f.s.ctx, false); err != nil {
			t.Fatal(err)
		}
		m.failApply = 1 // Mutter refuses to turn the desk off again
		if err := f.s.placeVirtualScreen(f.s.ctx); err != nil {
			t.Fatal(err)
		}
		if got := m.layout(t); !got.Equal(beside(false)) {
			t.Errorf("layout %+v, want the primary still on the desk as the client chose", got.Logical)
		}
	})
	t.Run("with the desk's monitors off the client's screen stays primary", func(t *testing.T) {
		f, m := ownScreenFixture(t, true)
		if p := f.s.primaryState(); p != nil {
			t.Errorf("session info reports primary %v for the only monitor on, want none so no switch shows", *p)
		}
		if err := f.s.movePrimary(f.s.ctx, false); !errors.Is(err, desktop.ErrDeskOff) {
			t.Errorf("err = %v, want ErrDeskOff", err)
		}
		if !m.st.DeskOff(virtualConnector) || len(m.log) != 0 {
			t.Errorf("the layout changed: %q, %+v", m.log, m.st.Logical)
		}
	})
}

func TestOwnScreenDeskSwitch(t *testing.T) {
	virtualOnly := desktop.DisplayLayout{LayoutMode: 1, Logical: []desktop.LogicalConfig{
		{Scale: 2, Primary: true, Monitors: []desktop.MonitorAssignment{{Connector: virtualConnector, ModeID: "v"}}},
	}}
	deskOff := func(f sessionFixture) *bool { return f.s.deskOffState() }

	f, m := ownScreenFixture(t, false)
	if p := deskOff(f); p == nil || *p {
		t.Fatalf("an unlocked desk beside the client's screen reports desk off %v, want false", p)
	}
	if err := f.s.switchDesk(f.s.ctx, true); err != nil {
		t.Fatal(err)
	}
	if got := m.layout(t); !got.Equal(virtualOnly) {
		t.Errorf("layout %+v, want the client's screen alone, so every window moves there", got.Logical)
	}
	if p := deskOff(f); p == nil || !*p {
		t.Errorf("desk off %v after the client turned it off, want true", p)
	}
	if p := f.s.primaryState(); p != nil {
		t.Errorf("primary %v while the client's screen is alone, want none", *p)
	}
	if err := f.s.placeVirtualScreen(f.s.ctx); err != nil {
		t.Fatal(err)
	}
	if !m.st.DeskOff(virtualConnector) {
		t.Errorf("placing the screen again turned the desk on: %+v", m.st.Logical)
	}
	if err := f.s.switchDesk(f.s.ctx, false); err != nil {
		t.Fatal(err)
	}
	if got := m.layout(t); !got.Equal(beside(false)) {
		t.Errorf("layout %+v, want the desk back as before with the client's screen beside it", got.Logical)
	}
	if p := deskOff(f); p == nil || *p {
		t.Errorf("desk off %v after the client turned it on, want false", p)
	}

	t.Run("a desk the session found locked stays dark", func(t *testing.T) {
		f, m := ownScreenFixture(t, true)
		if p := deskOff(f); p != nil {
			t.Errorf("desk off %v reported for a locked desk, want none so no switch shows", *p)
		}
		if err := f.s.switchDesk(f.s.ctx, false); err == nil {
			t.Error("turning on a desk that was locked succeeded")
		}
		if !m.st.DeskOff(virtualConnector) || len(m.log) != 0 {
			t.Errorf("the desk changed: %q, %+v", m.log, m.st.Logical)
		}
	})
	t.Run("a mirroring session cannot switch the desk", func(t *testing.T) {
		f := newSessionFixture(t)
		f.s.capture = &desktop.Capture{}
		if p := deskOff(f); p != nil {
			t.Errorf("desk off %v reported while mirroring, want none", *p)
		}
		if err := f.s.switchDesk(f.s.ctx, true); err == nil {
			t.Error("a mirroring session turned the desk off")
		}
	})
}

func TestOwnScreenScaleChangeKeepsTheDeskOff(t *testing.T) {
	f, m := ownScreenFixture(t, true)
	f.s.scale = 1.25
	if changed, err := f.s.rescaleVirtualScreen(f.s.ctx); err != nil || !changed {
		t.Fatalf("rescale: changed %v, err %v", changed, err)
	}
	if !m.st.DeskOff(virtualConnector) {
		t.Errorf("a scale change turned desk monitors on: %+v", m.st.Logical)
	}
	got := m.layout(t)
	if v := got.Logical[0]; v.X != 0 || v.Y != 0 || !v.Primary || v.Scale != 1.25 {
		t.Errorf("virtual screen %+v, want it alone at the origin, primary, at 125 %%", v)
	}
}

func TestOwnScreenCleanup(t *testing.T) {
	cases := []struct {
		name   string
		locked bool
		// during runs after the screen is arranged and the log is cleared.
		during func(*testing.T, *session, *fakeMutter)
		log    []string
		want   desktop.DisplayLayout
	}{
		{
			name:   "the desk's monitors come back on before the cast stops",
			locked: true,
			log:    []string{"apply DP-5 eDP-1 Meta-0", "stop cast", "Mutter lays out the desk"},
			want:   deskBefore(),
		},
		{
			name:   "after a scale change too",
			locked: true,
			during: func(t *testing.T, s *session, _ *fakeMutter) {
				s.scale = 1.25
				if _, err := s.rescaleVirtualScreen(s.ctx); err != nil {
					t.Fatal(err)
				}
			},
			log:  []string{"apply Meta-0", "apply DP-5 eDP-1 Meta-0", "stop cast", "Mutter lays out the desk"},
			want: deskBefore(),
		},
		{
			name:   "if turning them on fails they come back after the cast stops, even if Mutter does nothing",
			locked: true,
			during: func(_ *testing.T, _ *session, m *fakeMutter) { m.failApply, m.afterCast = 1, nil },
			log:    []string{"apply failed", "stop cast", "apply DP-5 eDP-1"},
			want:   deskBefore(),
		},
		{
			// Applying while Mutter removes the virtual monitor crashes gnome-shell.
			name:   "a cast Mutter closed by itself gets no configuration before the virtual screen has gone",
			locked: true,
			during: func(_ *testing.T, _ *session, m *fakeMutter) { m.castClosed = true },
			log:    []string{"stop cast", "Mutter lays out the desk"},
			want:   deskBefore(),
		},
		{
			name:   "and if Mutter leaves the desk's monitors off then, they come back after the virtual screen has gone",
			locked: true,
			during: func(_ *testing.T, _ *session, m *fakeMutter) { m.castClosed, m.afterCast = true, nil },
			log:    []string{"stop cast", "apply DP-5 eDP-1"},
			want:   deskBefore(),
		},
		{
			name:   "if the layout cannot be read as the session ends, they come back after the cast stops",
			locked: true,
			during: func(_ *testing.T, _ *session, m *fakeMutter) { m.failState, m.afterCast = 1, nil },
			log:    []string{"stop cast", "apply DP-5 eDP-1"},
			want:   deskBefore(),
		},
		{
			// 2026-11-25: restoring at once crashed gnome-shell.
			name:   "a session that lost its screen's connector turns the desk on and waits for the screen to go",
			locked: true,
			during: func(_ *testing.T, s *session, m *fakeMutter) {
				s.captured, s.deskArranged = "", nil
				m.linger, m.afterCast = 2, nil
			},
			log:  []string{"apply DP-5 eDP-1 Meta-0", "stop cast"},
			want: deskBefore(),
		},
		{
			name:   "and when Mutter closed its cast, the desk comes back once the screen has gone",
			locked: true,
			during: func(_ *testing.T, s *session, m *fakeMutter) {
				s.captured, s.deskArranged = "", nil
				m.castClosed, m.linger, m.afterCast = true, 2, nil
			},
			log:  []string{"stop cast", "apply DP-5 eDP-1"},
			want: deskBefore(),
		},
		{
			name:   "a monitor turned on at the desk during the session leaves the layout to GNOME",
			locked: true,
			during: func(_ *testing.T, _ *session, m *fakeMutter) {
				m.set(desktop.DisplayLayout{LayoutMode: 1, Logical: []desktop.LogicalConfig{
					{Scale: 1.3333333, Monitors: []desktop.MonitorAssignment{{Connector: "eDP-1", ModeID: "e"}}},
					{X: 1920, Scale: 2, Primary: true, Monitors: []desktop.MonitorAssignment{{Connector: virtualConnector, ModeID: "v"}}},
				}})
				linear := linearDesk()
				m.afterCast = &linear
			},
			log:  []string{"stop cast", "Mutter lays out the desk"},
			want: linearDesk(),
		},
		{
			name:   "an unlocked desk keeps its monitors on, so nothing changes before the cast stops",
			locked: false,
			log:    []string{"stop cast", "Mutter lays out the desk"},
			want:   deskBefore(),
		},
		{
			name:   "a desk the client turned off comes back on before the cast stops",
			locked: false,
			during: func(t *testing.T, s *session, _ *fakeMutter) {
				if err := s.switchDesk(s.ctx, true); err != nil {
					t.Fatal(err)
				}
			},
			log:  []string{"apply Meta-0", "apply DP-5 eDP-1 Meta-0", "stop cast", "Mutter lays out the desk"},
			want: deskBefore(),
		},
		{
			name:   "a client's screen made primary gives the desk its primary back",
			locked: false,
			during: func(t *testing.T, s *session, m *fakeMutter) {
				if err := s.movePrimary(s.ctx, true); err != nil {
					t.Fatal(err)
				}
				// Mutter has no saved layout to return to.
				linear := linearDesk()
				m.afterCast = &linear
			},
			log:  []string{"apply DP-5 eDP-1 Meta-0", "stop cast", "Mutter lays out the desk", "apply DP-5 eDP-1"},
			want: deskBefore(),
		},
		{
			name:   "an unsaved layout from before the session is restored once the virtual screen has gone",
			locked: true,
			during: func(_ *testing.T, _ *session, m *fakeMutter) {
				linear := linearDesk()
				m.afterCast = &linear
			},
			log:  []string{"apply DP-5 eDP-1 Meta-0", "stop cast", "Mutter lays out the desk", "apply DP-5 eDP-1"},
			want: deskBefore(),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, m := ownScreenFixture(t, c.locked)
			if c.during != nil {
				c.during(t, f.s, m)
			}
			f.s.cleanup()
			if !slices.Equal(m.log, c.log) {
				t.Errorf("cleanup did %q, want %q", m.log, c.log)
			}
			if got := m.layout(t); !got.Equal(c.want) {
				t.Errorf("layout after the session %+v, want %+v", got.Logical, c.want.Logical)
			}
		})
	}
}

func TestTheDeskComesBackHoweverTheSessionEnds(t *testing.T) {
	cases := []struct {
		name string
		end  func(sessionFixture)
	}{
		{"the client left", func(f sessionFixture) { f.s.cancel(nil) }},
		{"the media connection was lost", func(f sessionFixture) { f.s.cancel(errMediaLost) }},
		{"the device was unpaired", func(f sessionFixture) {
			if ended, err := f.ctl.revoke(tablet.ID, func() error { f.ctl.reg.remove(tablet.ID); return nil }); !ended || err != nil {
				t.Fatalf("revoke: ended %v, err %v", ended, err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, m := ownScreenFixture(t, true)
			c.end(f)
			f.s.cleanup()
			want := []string{"apply DP-5 eDP-1 Meta-0", "stop cast", "Mutter lays out the desk"}
			if !slices.Equal(m.log, want) {
				t.Errorf("cleanup did %q, want %q", m.log, want)
			}
			if m.st.DeskOff(virtualConnector) {
				t.Error("the desk's monitors are off after the session")
			}
		})
	}
}

// A desk the session found locked and whose monitors it turned off is locked
// again before its monitors come back, so nobody at the desk sees the
// unlocked desktop, however the session ends.
func TestTheDeskLocksBeforeItsMonitorsComeBack(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		t.Run(fmt.Sprintf("revoked=%v", revoked), func(t *testing.T) {
			f, m := ownScreenFixture(t, true)
			f.s.d.ctl.lock = func(context.Context) error { m.log = append(m.log, "lock"); return nil }
			if revoked {
				if ended, err := f.ctl.revoke(tablet.ID, func() error { f.ctl.reg.remove(tablet.ID); return nil }); !ended || err != nil {
					t.Fatalf("revoke: ended %v, err %v", ended, err)
				}
			}
			f.s.cleanup()
			want := []string{"lock", "apply DP-5 eDP-1 Meta-0", "stop cast", "Mutter lays out the desk"}
			if !slices.Equal(m.log, want) {
				t.Errorf("cleanup did %q, want %q", m.log, want)
			}
		})
	}
}

func TestLadderTurnsWithTheScreen(t *testing.T) {
	landscape, portrait := ladder(2560, 1600, 120), ladder(1600, 2560, 120)
	if len(portrait) != len(landscape) {
		t.Fatalf("portrait ladder %+v, want the rungs of %+v turned", portrait, landscape)
	}
	for i, l := range landscape {
		if p := portrait[i]; p.Width != l.Height || p.Height != l.Width || p.FPS != l.FPS {
			t.Errorf("portrait rung %d is %+v, want %+v turned", i, p, l)
		}
	}
	// A level remembered from a landscape session starts a portrait one on
	// the same rung.
	remembered := video.Level{Width: 1920, Height: 1200, FPS: 60}
	want := video.LevelFor(landscape, remembered)
	if got := video.LevelFor(portrait, turnedLike(remembered, 1600, 2560)); got != want {
		t.Errorf("a portrait session starts on rung %d, want %d as in landscape", got, want)
	}
	if got := turnedLike(remembered, 2560, 1600); got != remembered {
		t.Errorf("a landscape level for a landscape screen became %+v", got)
	}
}

func TestTheMirroredMonitor(t *testing.T) {
	cases := []struct {
		name, chosen, configured, want string
	}{
		{"the client's choice when it is on", "DP-5", "", "DP-5"},
		{"the client's choice wins over the configured monitor", "eDP-1", "DP-5", "eDP-1"},
		{"the configured monitor when the client's choice is not on", "HDMI-1", "DP-5", "DP-5"},
		{"otherwise the monitor shaped most like the client's screen", "", "", "eDP-1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newSessionFixture(t)
			m := newFakeMutter()
			useFakeMutter(f, m)
			f.s.d.opts.Config.Monitor = c.configured
			f.s.width, f.s.height = 2560, 1600
			if err := f.s.startMonitorCapture(f.s.ctx, c.chosen); err != nil {
				t.Fatal(err)
			}
			if f.s.captured != c.want || !slices.Equal(m.log, []string{"record " + c.want}) {
				t.Errorf("mirrors %q after %q, want %q", f.s.captured, m.log, c.want)
			}
		})
	}
}

// mirrorFixture is a session that mirrors eDP-1 of the desk on m, with its
// pipeline running and the log cleared.
func mirrorFixture(t *testing.T) (sessionFixture, *fakeMutter) {
	t.Helper()
	f := newSessionFixture(t)
	m := newFakeMutter()
	useFakeMutter(f, m)
	f.s.width, f.s.height = 2560, 1600
	if err := f.s.startMonitorCapture(f.s.ctx, "eDP-1"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.startPipeline(f.s.ctx); err != nil {
		t.Fatal(err)
	}
	m.log = nil
	return f, m
}

func TestMirrorSwitch(t *testing.T) {
	t.Run("a client that leaves during the switch leaves no cast running", func(t *testing.T) {
		f, m := mirrorFixture(t)
		m.godbus = true
		// The client leaves while the old pipeline stops.
		f.s.enc = &fakePipeline{stop: func() { f.s.cancel(nil) }}
		if err := f.s.switchMirror(f.s.ctx, "DP-5"); err == nil {
			t.Error("the switch of an ended session succeeded")
		}
		f.s.cleanup()
		if len(m.casts) != 0 {
			t.Errorf("casts still running after the session: %v", m.casts)
		}
		if n := len(m.pipelines); n != 1 {
			t.Errorf("%d pipelines started, want only the first: the ended session started another", n)
		}
	})
	t.Run("a monitor that is on replaces the mirrored one", func(t *testing.T) {
		f, m := mirrorFixture(t)
		if err := f.s.switchMirror(f.s.ctx, "DP-5"); err != nil {
			t.Fatal(err)
		}
		if want := []string{"record DP-5", "stop cast"}; !slices.Equal(m.log, want) {
			t.Errorf("did %q, want %q: the new cast before the old one stops", m.log, want)
		}
		if f.s.captured != "DP-5" || f.s.mirroring() != "DP-5" {
			t.Errorf("mirrors %q, reports %q, want DP-5", f.s.captured, f.s.mirroring())
		}
		if len(m.pipelines) != 2 || m.pipelines[1].NodeID != f.s.capture.NodeID {
			t.Errorf("pipelines %+v, want a second one on the new cast's node %d", m.pipelines, f.s.capture.NodeID)
		}
		// The middle of the frame lands in the middle of DP-5, which sits
		// right of eDP-1 on a desktop 4480x1440 points large.
		x, y := f.s.pointer.apply(absMax/2, absMax/2)
		if wantX, wantY := 3200.0/4480*absMax, 0.5*absMax; math.Abs(float64(x)-wantX) > 2 || math.Abs(float64(y)-wantY) > 2 {
			t.Errorf("the frame's middle maps to %d,%d, want %.0f,%.0f", x, y, wantX, wantY)
		}
	})
	t.Run("the monitor mirrored already changes nothing", func(t *testing.T) {
		f, m := mirrorFixture(t)
		if err := f.s.switchMirror(f.s.ctx, "eDP-1"); err != nil {
			t.Fatal(err)
		}
		if len(m.log) != 0 || len(m.pipelines) != 1 {
			t.Errorf("did %q and started %d pipelines, want nothing", m.log, len(m.pipelines))
		}
	})
	refused := []struct {
		name      string
		connector string
		setup     func(*fakeMutter)
	}{
		{"an unknown connector", "HDMI-9", nil},
		{"an empty connector", "", nil},
		{"a monitor that is off", "DP-5", func(m *fakeMutter) {
			m.set(desktop.DisplayLayout{LayoutMode: 1, Logical: []desktop.LogicalConfig{
				{Scale: 1, Primary: true, Monitors: []desktop.MonitorAssignment{{Connector: "eDP-1", ModeID: "e"}}},
			}})
		}},
		{"a monitor Mutter cannot record", "DP-5", func(m *fakeMutter) { m.failCast = 1 }},
	}
	for _, c := range refused {
		t.Run("refuses "+c.name+" and keeps mirroring", func(t *testing.T) {
			f, m := mirrorFixture(t)
			if c.setup != nil {
				c.setup(m)
			}
			if err := f.s.switchMirror(f.s.ctx, c.connector); err == nil {
				t.Error("the switch succeeded")
			}
			if f.s.captured != "eDP-1" || slices.Contains(m.log, "stop cast") || len(m.pipelines) != 1 || f.s.ctx.Err() != nil {
				t.Errorf("mirrors %q after %q with %d pipelines, session ended: %v; want eDP-1 as before",
					f.s.captured, m.log, len(m.pipelines), f.s.ctx.Err() != nil)
			}
		})
	}
	t.Run("a session with its own screen has no monitor to switch", func(t *testing.T) {
		f, m := ownScreenFixture(t, false)
		if err := f.s.switchMirror(f.s.ctx, "DP-5"); err == nil {
			t.Error("the switch succeeded")
		}
		if len(m.log) != 0 || f.s.captured != virtualConnector {
			t.Errorf("did %q, shows %q", m.log, f.s.captured)
		}
	})
}

func TestSessionInfoMonitors(t *testing.T) {
	desk := []dlnet.MonitorInfo{
		{Connector: "DP-5", Width: 2560, Height: 1440, Refresh: 60, Primary: true},
		{Connector: "eDP-1", Width: 2560, Height: 1600, Refresh: 60},
	}
	t.Run("a mirroring session lists every monitor and names the one it mirrors", func(t *testing.T) {
		f, _ := mirrorFixture(t)
		if got := f.s.monitorList(f.s.ctx); !slices.Equal(got, desk) {
			t.Errorf("monitors %+v, want %+v", got, desk)
		}
		if got := f.s.mirroring(); got != "eDP-1" {
			t.Errorf("mirroring %q, want eDP-1", got)
		}
	})
	t.Run("an own screen beside the desk lists the desk's monitors, not itself", func(t *testing.T) {
		f, _ := ownScreenFixture(t, false)
		if got := f.s.monitorList(f.s.ctx); !slices.Equal(got, desk) {
			t.Errorf("monitors %+v, want %+v", got, desk)
		}
		if got := f.s.mirroring(); got != "" {
			t.Errorf("mirroring %q for the own screen, want none", got)
		}
	})
	t.Run("an own screen not yet in the layout is left out too", func(t *testing.T) {
		f, _ := castingFixture(t, false)
		if got := f.s.monitorList(f.s.ctx); !slices.Equal(got, desk) {
			t.Errorf("monitors %+v, want %+v", got, desk)
		}
	})
	t.Run("with the desk's monitors off there is nothing to mirror", func(t *testing.T) {
		f, _ := ownScreenFixture(t, true)
		if got := f.s.monitorList(f.s.ctx); len(got) != 0 {
			t.Errorf("monitors %+v, want none", got)
		}
	})
	t.Run("the last list stands in when the layout cannot be read", func(t *testing.T) {
		f, m := mirrorFixture(t)
		f.s.monitorList(f.s.ctx)
		m.failState = 1
		if got := f.s.monitorList(f.s.ctx); !slices.Equal(got, desk) {
			t.Errorf("monitors %+v, want the last list %+v", got, desk)
		}
	})
}

func TestSessionInfoMouse(t *testing.T) {
	slower := func(context.Context) desktop.MouseSettings { return desktop.MouseSettings{Speed: -0.3} }
	// mouse reads the layout and describes the mouse, as sessionInfo does.
	mouse := func(f sessionFixture) dlnet.MouseInfo {
		f.s.monitorList(f.s.ctx)
		return *f.s.mouseInfo(f.s.ctx)
	}
	t.Run("the own screen sends the desk's settings at its scale", func(t *testing.T) {
		f, _ := ownScreenFixture(t, false)
		f.s.d.mouseSettings = slower
		if got, want := mouse(f), (dlnet.MouseInfo{Speed: -0.3, Scale: 2}); got != want {
			t.Errorf("mouse %+v, want %+v at 200 %%", got, want)
		}
	})
	t.Run("a mirrored monitor's scale counts in the client's pixels", func(t *testing.T) {
		f, _ := mirrorFixture(t)
		f.s.d.mouseSettings = slower
		// eDP-1 is 2560 pixels wide at 133 %.
		if got := mouse(f); math.Abs(got.Scale-4.0/3) > 1e-6 || got.Speed != -0.3 {
			t.Errorf("mouse %+v mirroring eDP-1 on a 2560 wide client, want speed -0.3 at scale 4/3", got)
		}
		f.s.pointerMu.Lock()
		f.s.screenW = 1280
		f.s.pointerMu.Unlock()
		if got := mouse(f); math.Abs(got.Scale-2.0/3) > 1e-6 {
			t.Errorf("mouse %+v on a 1280 wide client, want scale 2/3", got)
		}
	})
	t.Run("settings that cannot be read are GNOME's defaults", func(t *testing.T) {
		f, _ := mirrorFixture(t)
		if got := mouse(f); got.Speed != 0 || got.Flat {
			t.Errorf("mouse %+v, want speed 0, adaptive", got)
		}
	})
}

// portraitCorners are where the frame's corners land for a portrait
// 1600x2560 screen at 200 % (800x1280 points) right of the desk, which spans
// 4480x1440 points.
var portraitCorners = [2][2]float64{{4480.0 / 5280, 0}, {1, 1280.0 / 1440}}

func TestOwnScreenResize(t *testing.T) {
	alone := func(scale float64) desktop.DisplayLayout {
		return desktop.DisplayLayout{LayoutMode: 1, Logical: []desktop.LogicalConfig{
			{Scale: scale, Primary: true, Monitors: []desktop.MonitorAssignment{{Connector: "Meta-0", ModeID: "v1"}}},
		}}
	}
	besideDesk := func(primary bool) desktop.DisplayLayout {
		l := deskBefore()
		l.Logical[0].Primary = !primary
		l.Logical = append(l.Logical, desktop.LogicalConfig{X: 4480, Scale: 2, Primary: primary, Monitors: []desktop.MonitorAssignment{{Connector: "Meta-0", ModeID: "v1"}}})
		return l
	}
	cases := []struct {
		name   string
		locked bool
		// before runs once the screen is arranged, before the log is
		// cleared and the screen resized.
		before  func(*testing.T, *session)
		want    desktop.DisplayLayout
		log     []string
		corners [2][2]float64
	}{
		{
			name:    "a desk the session found locked comes on while the screen is replaced, then goes off again",
			locked:  true,
			want:    alone(2),
			log:     []string{"apply DP-5 eDP-1 Meta-0", "stop cast", "Mutter lays out the desk", "record virtual", "apply Meta-0"},
			corners: [2][2]float64{{0, 0}, {1, 1}},
		},
		{
			name:    "beside an unlocked desk the new screen takes the old one's place",
			want:    besideDesk(false),
			log:     []string{"stop cast", "Mutter lays out the desk", "record virtual", "apply DP-5 eDP-1 Meta-0"},
			corners: portraitCorners,
		},
		{
			name: "the scale stays as the client set it",
			before: func(t *testing.T, s *session) {
				s.scale = 1.25
				if _, err := s.rescaleVirtualScreen(s.ctx); err != nil {
					t.Fatal(err)
				}
			},
			locked:  true,
			want:    alone(1.25),
			log:     []string{"apply DP-5 eDP-1 Meta-0", "stop cast", "Mutter lays out the desk", "record virtual", "apply Meta-0"},
			corners: [2][2]float64{{0, 0}, {1, 1}},
		},
		{
			name: "the client's screen stays primary",
			before: func(t *testing.T, s *session) {
				if err := s.movePrimary(s.ctx, true); err != nil {
					t.Fatal(err)
				}
			},
			want:    besideDesk(true),
			log:     []string{"stop cast", "Mutter lays out the desk", "record virtual", "apply DP-5 eDP-1 Meta-0"},
			corners: portraitCorners,
		},
		{
			name: "desk screens the client turned off stay off",
			before: func(t *testing.T, s *session) {
				if err := s.switchDesk(s.ctx, true); err != nil {
					t.Fatal(err)
				}
			},
			want:    alone(2),
			log:     []string{"apply DP-5 eDP-1 Meta-0", "stop cast", "Mutter lays out the desk", "record virtual", "apply Meta-0"},
			corners: [2][2]float64{{0, 0}, {1, 1}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, m := ownScreenFixture(t, c.locked)
			if c.before != nil {
				c.before(t, f.s)
			}
			m.log = nil
			if err := f.s.resizeOwnScreen(f.s.ctx, 1600, 2560); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(m.log, c.log) {
				t.Errorf("did %q, want %q", m.log, c.log)
			}
			if got := m.layout(t); !got.Equal(c.want) {
				t.Errorf("layout %+v, want %+v", got.Logical, c.want.Logical)
			}
			last := m.pipelines[len(m.pipelines)-1]
			if last.CaptureWidth != 1600 || last.CaptureHeight != 2560 || last.Width != 1600 || last.Height != 2560 || last.NodeID != f.s.capture.NodeID {
				t.Errorf("the new pipeline has %+v, want a 1600x2560 capture and stream on the new cast", last)
			}
			if f.s.captured != "Meta-0" || f.s.captureW != 1600 || f.s.captureH != 2560 {
				t.Errorf("the session shows %q at %dx%d, want Meta-0 at 1600x2560", f.s.captured, f.s.captureW, f.s.captureH)
			}
			if f.s.screenW != 1600 || f.s.screenH != 2560 {
				t.Errorf("relative motion is measured on a %dx%d screen, want 1600x2560", f.s.screenW, f.s.screenH)
			}
			for i, frame := range [2]int32{0, absMax} {
				x, y := f.s.pointer.apply(frame, frame)
				wantX, wantY := c.corners[i][0]*absMax, c.corners[i][1]*absMax
				if math.Abs(float64(x)-wantX) > 1 || math.Abs(float64(y)-wantY) > 1 {
					t.Errorf("frame corner %d,%d maps to %d,%d, want %.0f,%.0f", frame, frame, x, y, wantX, wantY)
				}
			}
		})
	}

	t.Run("the quality ladder turns and keeps its rung", func(t *testing.T) {
		f, _ := ownScreenFixture(t, true)
		f.s.quality = video.NewQualityController(video.QualityConfig{Requested: 18, MinQP: 16, MaxQP: 26, Levels: ladder(2560, 1600, 120), Start: 1, Auto: true})
		if err := f.s.resizeOwnScreen(f.s.ctx, 1600, 2560); err != nil {
			t.Fatal(err)
		}
		top, at := f.s.quality.Levels()[0], f.s.quality.Levels()[f.s.quality.Level()]
		if top != (video.Level{Width: 1600, Height: 2560, FPS: 120}) || f.s.quality.Level() != 1 || !f.s.quality.Auto() {
			t.Errorf("ladder top %+v at rung %d, want 1600x2560 at 120 fps on rung 1", top, f.s.quality.Level())
		}
		if f.s.width != at.Width || f.s.height != at.Height || f.s.fps != at.FPS {
			t.Errorf("the stream is %dx%d at %d fps, want rung %+v", f.s.width, f.s.height, f.s.fps, at)
		}
	})
	t.Run("the same size changes nothing", func(t *testing.T) {
		f, m := ownScreenFixture(t, true)
		if err := f.s.resizeOwnScreen(f.s.ctx, 2560, 1600); err != nil {
			t.Fatal(err)
		}
		if len(m.log) != 0 || len(m.pipelines) != 0 {
			t.Errorf("did %q and started %d pipelines", m.log, len(m.pipelines))
		}
	})
	t.Run("the end of the session gives the desk back after a resize", func(t *testing.T) {
		f, m := ownScreenFixture(t, true)
		if err := f.s.resizeOwnScreen(f.s.ctx, 1600, 2560); err != nil {
			t.Fatal(err)
		}
		m.log = nil
		f.s.cleanup()
		if want := []string{"apply DP-5 eDP-1 Meta-0", "stop cast", "Mutter lays out the desk"}; !slices.Equal(m.log, want) {
			t.Errorf("cleanup did %q, want %q", m.log, want)
		}
		if got := m.layout(t); !got.Equal(deskBefore()) {
			t.Errorf("layout after the session %+v, want the desk as before", got.Logical)
		}
	})
	// 2026-11-25: Mutter named the new screen Meta-0 again, the session took
	// it for the old one and lost its screen, and the end of the session
	// crashed gnome-shell.
	t.Run("rotating there and back while Mutter is slow to drop the old screen, then leaving", func(t *testing.T) {
		f, m := ownScreenFixture(t, true)
		m.linger = 2
		for _, size := range [][2]int{{1600, 2560}, {2560, 1600}} {
			if err := f.s.resizeOwnScreen(f.s.ctx, size[0], size[1]); err != nil {
				t.Fatal(err)
			}
			if f.s.captured != virtualConnector || !f.s.ownAlone {
				t.Errorf("after the resize to %dx%d the session shows %q (alone %v), want Meta-0 alone", size[0], size[1], f.s.captured, f.s.ownAlone)
			}
		}
		f.s.cleanup()
		if slices.Contains(m.log, "gnome-shell crashed") {
			t.Errorf("a layout was applied while Mutter still listed a freed screen: %q", m.log)
		}
		if got := m.layout(t); !got.Equal(deskBefore()) {
			t.Errorf("layout after the session %+v, want the desk as before", got.Logical)
		}
	})
	t.Run("a client that leaves while the screen is replaced gets the desk back", func(t *testing.T) {
		f, m := ownScreenFixture(t, true)
		m.godbus = true
		// The client leaves while the old pipeline stops.
		f.s.enc = &fakePipeline{stop: func() { f.s.cancel(nil) }}
		if err := f.s.resizeOwnScreen(f.s.ctx, 1600, 2560); err == nil {
			t.Error("the resize of an ended session succeeded")
		}
		f.s.cleanup()
		if len(m.casts) != 0 {
			t.Errorf("casts still running after the session: %v", m.casts)
		}
		if got := m.layout(t); !got.Equal(deskBefore()) {
			t.Errorf("layout after the session %+v, want the desk as before", got.Logical)
		}
	})
	t.Run("a screen that cannot start again ends the session", func(t *testing.T) {
		f, m := ownScreenFixture(t, true)
		m.failCast = 1
		if err := f.s.resizeOwnScreen(f.s.ctx, 1600, 2560); err == nil || f.s.ctx.Err() == nil {
			t.Errorf("err %v, session ended %v; want both", err, f.s.ctx.Err() != nil)
		}
		if m.st.DeskOff("") {
			t.Error("no monitor is on")
		}
	})
	refused := []struct {
		name string
		w, h int
	}{
		{"too small", 200, 2560},
		{"too large", 1600, 5000},
	}
	for _, c := range refused {
		t.Run("refuses a size "+c.name, func(t *testing.T) {
			f, m := ownScreenFixture(t, true)
			if err := f.s.resizeOwnScreen(f.s.ctx, c.w, c.h); err == nil {
				t.Error("the resize succeeded")
			}
			if len(m.log) != 0 || f.s.captureW != 2560 || f.s.ctx.Err() != nil {
				t.Errorf("did %q, screen %dx%d", m.log, f.s.captureW, f.s.captureH)
			}
		})
	}
	t.Run("a mirroring session refuses to resize", func(t *testing.T) {
		f, m := mirrorFixture(t)
		if err := f.s.resizeOwnScreen(f.s.ctx, 1600, 2560); err == nil {
			t.Error("the resize succeeded")
		}
		if len(m.log) != 0 || len(m.pipelines) != 1 {
			t.Errorf("did %q with %d pipelines", m.log, len(m.pipelines))
		}
	})
}
