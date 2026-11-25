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
	}}}
	m.set(deskBefore())
	return m
}

// startCast adds the virtual monitor the way Mutter does: it lays out the
// unknown monitor set from scratch, moving the desk's monitors.
func (m *fakeMutter) startCast() {
	m.st.Monitors = append(m.st.Monitors, fakeMonitor(virtualConnector, "v", 2560, 1600, 1, 1.25, 1.6666666, 2, 2.5))
	l := linearDesk()
	l.Logical = append(l.Logical, desktop.LogicalConfig{X: 4480, Scale: 1, Monitors: []desktop.MonitorAssignment{{Connector: virtualConnector, ModeID: "v"}}})
	m.set(l)
}

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

func (m *fakeMutter) DisplayState(context.Context) (desktop.DisplayState, error) {
	if m.failState > 0 {
		m.failState--
		return desktop.DisplayState{}, errors.New("no reply")
	}
	return m.st, nil
}

func (m *fakeMutter) ApplyLayout(_ context.Context, serial uint32, l desktop.DisplayLayout) error {
	if m.failApply > 0 {
		m.failApply--
		m.log = append(m.log, "apply failed")
		return errors.New("refused")
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

func (m *fakeMutter) stopCast(context.Context, *desktop.Capture) error {
	m.log = append(m.log, "stop cast")
	m.st.Serial++
	m.st.Monitors = slices.DeleteFunc(m.st.Monitors, func(mon desktop.Monitor) bool { return mon.Connector == virtualConnector })
	m.st.Logical = slices.DeleteFunc(m.st.Logical, func(lm desktop.LogicalMonitor) bool { return slices.Contains(lm.Connectors, virtualConnector) })
	if m.afterCast != nil {
		m.log = append(m.log, "Mutter lays out the desk")
		m.set(*m.afterCast)
	}
	if m.castClosed {
		return errors.New("object does not exist")
	}
	return nil
}

func (m *fakeMutter) castOpen(context.Context, *desktop.Capture) bool {
	return !m.castClosed
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
	f.s.d.display, f.s.d.stopCast, f.s.d.castOpen = m, m.stopCast, m.castOpen
	f.s.wasLocked = locked
	f.s.width, f.s.height, f.s.scale = 2560, 1600, 2
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
