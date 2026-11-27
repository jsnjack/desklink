package desktop

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// fakeMutter is Mutter's ScreenCast and RemoteDesktop services as far as
// desklinkd uses them, with Mutter's rules: a screen cast that belongs to a
// remote desktop session is started and stopped only through that session,
// and only a started remote desktop session takes keysyms or stops.
type fakeMutter struct {
	conn *dbus.Conn
	// noRemote leaves org.gnome.Mutter.RemoteDesktop off the bus;
	// refuseLinked refuses screen casts in a remote desktop session.
	noRemote, refuseLinked bool

	mu      sync.Mutex
	next    int
	log     []string
	keys    []sentKeysym
	remotes map[string]*fakeRemoteSession
	// cursorModes are the cursor-mode of each cast, in order.
	cursorModes []uint32
}

func (m *fakeMutter) noteCursorMode(props map[string]dbus.Variant) {
	mode, _ := props["cursor-mode"].Value().(uint32)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cursorModes = append(m.cursorModes, mode)
}

// sentKeysym is one NotifyKeyboardKeysym call; serial orders the calls as the
// client sent them, which is the order Mutter handles them in.
type sentKeysym struct {
	serial uint32
	keysym uint32
	down   bool
}

func (m *fakeMutter) note(entry string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.log = append(m.log, entry)
}

func (m *fakeMutter) entries() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.log)
}

// typed is every keysym call in the order the client sent them.
func (m *fakeMutter) typed() []sentKeysym {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := slices.Clone(m.keys)
	slices.SortFunc(keys, func(a, b sentKeysym) int { return int(a.serial) - int(b.serial) })
	return keys
}

func (m *fakeMutter) newID() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	return m.next
}

func (m *fakeMutter) export(t *testing.T, v any, path dbus.ObjectPath, iface string) {
	t.Helper()
	if err := m.conn.Export(v, path, iface); err != nil {
		t.Fatal(err)
	}
}

func dbusFailed(msg string) *dbus.Error {
	return dbus.NewError("org.freedesktop.DBus.Error.Failed", []any{msg})
}

type fakeCastManager struct {
	m *fakeMutter
	t *testing.T
}

func (f fakeCastManager) CreateSession(props map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	m := f.m
	s := &fakeCastSession{m: m, t: f.t}
	if v, ok := props[remoteSessionIDProp]; ok {
		id, _ := v.Value().(string)
		m.mu.Lock()
		r, refuse := m.remotes[id], m.refuseLinked
		m.mu.Unlock()
		if refuse {
			return "", dbusFailed("Remote desktop session already have an associated screen cast session")
		}
		if r == nil {
			return "", dbusFailed("No remote desktop session found")
		}
		s.remote = r
		m.mu.Lock()
		r.cast = s
		m.mu.Unlock()
		m.note("create screen cast in remote desktop session " + id)
	} else {
		m.note("create screen cast")
	}
	s.path = dbus.ObjectPath(fmt.Sprintf("%s/Session/u%d", mutterCastPath, m.newID()))
	m.export(f.t, s, s.path, ifaceCastSession)
	return s.path, nil
}

type fakeCastSession struct {
	m      *fakeMutter
	t      *testing.T
	path   dbus.ObjectPath
	stream dbus.ObjectPath
	remote *fakeRemoteSession
}

// fakeStream answers the stream's property reads; readParameters gets no
// parameters and leaves the monitor's place unknown.
type fakeStream struct{}

func (fakeStream) Get(string, string) (dbus.Variant, *dbus.Error) {
	return dbus.Variant{}, dbus.NewError("org.freedesktop.DBus.Error.UnknownProperty", nil)
}

func (fakeStream) GetAll(string) (map[string]dbus.Variant, *dbus.Error) {
	return map[string]dbus.Variant{}, nil
}

func (s *fakeCastSession) record(what string) (dbus.ObjectPath, *dbus.Error) {
	s.stream = dbus.ObjectPath(fmt.Sprintf("%s/Stream/u%d", mutterCastPath, s.m.newID()))
	s.m.export(s.t, fakeStream{}, s.stream, "org.freedesktop.DBus.Properties")
	s.m.note(what)
	return s.stream, nil
}

func (s *fakeCastSession) RecordMonitor(connector string, props map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	s.m.noteCursorMode(props)
	return s.record("record monitor " + connector)
}

func (s *fakeCastSession) RecordVirtual(props map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	s.m.noteCursorMode(props)
	return s.record("record virtual monitor")
}

// startStream announces the stream's PipeWire node, as Mutter does once the
// stream starts.
func (s *fakeCastSession) startStream() *dbus.Error {
	if err := s.m.conn.Emit(s.stream, ifaceCastStream+".PipeWireStreamAdded", uint32(42)); err != nil {
		return dbusFailed(err.Error())
	}
	return nil
}

// closeStream takes the stream off the bus, as when its PipeWire stream fails.
func (s *fakeCastSession) closeStream() {
	if err := s.m.conn.Export(nil, s.stream, "org.freedesktop.DBus.Properties"); err != nil {
		s.t.Error(err)
	}
}

func (s *fakeCastSession) close() {
	s.closeStream()
	if err := s.m.conn.Export(nil, s.path, ifaceCastSession); err != nil {
		s.t.Error(err)
	}
}

func (s *fakeCastSession) Start() *dbus.Error {
	if s.remote != nil {
		return dbusFailed("Must be started from remote desktop session")
	}
	s.m.note("start screen cast")
	return s.startStream()
}

func (s *fakeCastSession) Stop() *dbus.Error {
	if s.remote != nil {
		return dbusFailed("Must be stopped from remote desktop session")
	}
	s.m.note("stop screen cast")
	s.close()
	return nil
}

type fakeRemoteManager struct {
	m *fakeMutter
	t *testing.T
}

func (f fakeRemoteManager) CreateSession() (dbus.ObjectPath, *dbus.Error) {
	m := f.m
	n := m.newID()
	r := &fakeRemoteSession{m: m, id: fmt.Sprintf("remote-%d", n), path: dbus.ObjectPath(fmt.Sprintf("%s/Session/u%d", mutterRemotePath, n))}
	m.mu.Lock()
	m.remotes[r.id] = r
	m.mu.Unlock()
	m.export(f.t, r, r.path, ifaceRemoteSession)
	m.export(f.t, fakeProperties{func(iface, name string) (dbus.Variant, *dbus.Error) {
		if iface != ifaceRemoteSession || name != "SessionId" {
			return dbus.Variant{}, dbus.NewError("org.freedesktop.DBus.Error.UnknownProperty", nil)
		}
		return dbus.MakeVariant(r.id), nil
	}}, r.path, "org.freedesktop.DBus.Properties")
	m.note("create remote desktop session")
	return r.path, nil
}

type fakeRemoteSession struct {
	m       *fakeMutter
	id      string
	path    dbus.ObjectPath
	cast    *fakeCastSession
	started bool
}

// linked is the screen cast that belongs to r, and whether r has started.
func (r *fakeRemoteSession) linked() (*fakeCastSession, bool) {
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	return r.cast, r.started
}

func (r *fakeRemoteSession) Start() *dbus.Error {
	r.m.note("start remote desktop session")
	r.m.mu.Lock()
	r.started = true
	r.m.mu.Unlock()
	if cast, _ := r.linked(); cast != nil {
		return cast.startStream()
	}
	return nil
}

func (r *fakeRemoteSession) Stop() *dbus.Error {
	cast, started := r.linked()
	if !started {
		return dbusFailed("Session not started")
	}
	r.m.note("stop remote desktop session")
	if cast != nil {
		cast.close()
	}
	return nil
}

// NotifyKeyboardKeysym takes the call's message for its serial: godbus
// handles each call on a goroutine of its own, so arrival order is lost.
func (r *fakeRemoteSession) NotifyKeyboardKeysym(msg dbus.Message, keysym uint32, down bool) *dbus.Error {
	if _, started := r.linked(); !started {
		return dbusFailed("Session not started")
	}
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	r.m.keys = append(r.m.keys, sentKeysym{serial: msg.Serial(), keysym: keysym, down: down})
	return nil
}

// serveFakeMutter runs a fakeMutter on a private bus and returns it with a
// ScreenCast that talks to it.
func serveFakeMutter(t *testing.T, noRemote bool) (*fakeMutter, *ScreenCast) {
	t.Helper()
	addr := privateBus(t)
	m := &fakeMutter{conn: connect(t, addr), noRemote: noRemote, remotes: map[string]*fakeRemoteSession{}}
	m.export(t, fakeCastManager{m, t}, mutterCastPath, ifaceMutterCast)
	names := []string{mutterCastBus}
	if !noRemote {
		m.export(t, fakeRemoteManager{m, t}, mutterRemotePath, ifaceMutterRemote)
		names = append(names, mutterRemoteBus)
	}
	for _, name := range names {
		if reply, err := m.conn.RequestName(name, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
			t.Fatalf("own %s: %v, %v", name, reply, err)
		}
	}
	return m, NewScreenCast(connect(t, addr))
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestThePointerGoesApartOnlyWhenAsked: a cast leaves the pointer out of the
// frames, as metadata (cursor-mode 2), only for a client that draws it.
func TestThePointerGoesApartOnlyWhenAsked(t *testing.T) {
	m, cast := serveFakeMutter(t, false)
	ctx := testContext(t)
	for _, start := range []func() (*Capture, error){
		func() (*Capture, error) { return cast.Start(ctx, "DP-1", false) },
		func() (*Capture, error) { return cast.Start(ctx, "DP-1", true) },
		func() (*Capture, error) { return cast.StartVirtual(ctx, false) },
		func() (*Capture, error) { return cast.StartVirtual(ctx, true) },
	} {
		c, err := start()
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if want := []uint32{1, 2, 1, 2}; !slices.Equal(m.cursorModes, want) {
		t.Errorf("cursor modes %v, want %v", m.cursorModes, want)
	}
}

// TestTheCastBelongsToARemoteDesktopSession: the screen cast is created inside
// a remote desktop session, which starts it, so text can be typed.
func TestTheCastBelongsToARemoteDesktopSession(t *testing.T) {
	cases := []struct {
		name   string
		start  func(*ScreenCast, context.Context) (*Capture, error)
		record string
	}{
		{"a mirrored monitor", func(s *ScreenCast, ctx context.Context) (*Capture, error) { return s.Start(ctx, "DP-1", false) }, "record monitor DP-1"},
		{"the client's own screen", func(s *ScreenCast, ctx context.Context) (*Capture, error) { return s.StartVirtual(ctx, false) }, "record virtual monitor"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, cast := serveFakeMutter(t, false)
			capture, err := c.start(cast, testContext(t))
			if err != nil {
				t.Fatal(err)
			}
			want := []string{
				"create remote desktop session",
				"create screen cast in remote desktop session remote-1",
				c.record,
				"start remote desktop session",
			}
			if got := m.entries(); !slices.Equal(got, want) {
				t.Errorf("Mutter saw %q, want %q", got, want)
			}
			if capture.NodeID != 42 {
				t.Errorf("PipeWire node %d, want 42", capture.NodeID)
			}
			if !capture.CanType() {
				t.Error("a cast in a remote desktop session cannot type")
			}
		})
	}
}

// TestTextBecomesKeysymPressesAndReleases: every character is pressed and
// released as its keysym, in order, and characters nothing types are skipped.
func TestTextBecomesKeysymPressesAndReleases(t *testing.T) {
	m, cast := serveFakeMutter(t, false)
	ctx := testContext(t)
	capture, err := cast.Start(ctx, "DP-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := capture.Type(ctx, "aÉ\r\nа€\x07😀\b"); err != nil {
		t.Fatal(err)
	}
	var want []sentKeysym
	for _, k := range []uint32{'a', 0xc9, keysymReturn, 0x6c1, 0x20ac, 0x0101f600, keysymBackSpace} {
		want = append(want, sentKeysym{keysym: k, down: true}, sentKeysym{keysym: k, down: false})
	}
	got := m.typed()
	for i := range got {
		got[i].serial = 0
	}
	if !slices.Equal(got, want) {
		t.Errorf("typed %+v, want %+v", got, want)
	}
}

// TestCleanupStopsTheRemoteDesktopSession: closing the capture stops the
// remote desktop session, which stops the cast with it; Mutter would refuse
// to stop the cast alone.
func TestCleanupStopsTheRemoteDesktopSession(t *testing.T) {
	m, cast := serveFakeMutter(t, false)
	ctx := testContext(t)
	capture, err := cast.Start(ctx, "DP-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := capture.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if got := m.entries(); got[len(got)-1] != "stop remote desktop session" {
		t.Errorf("Mutter saw %q, want the remote desktop session stopped last", got)
	}
	if capture.Open(ctx) {
		t.Error("the cast is still open after the remote desktop session stopped")
	}
}

// TestWithoutRemoteDesktopTheCastStillWorks: a Mutter without the remote
// desktop service still gets the plain screen cast of before, and text then
// fails with ErrNoKeysyms.
func TestWithoutRemoteDesktopTheCastStillWorks(t *testing.T) {
	m, cast := serveFakeMutter(t, true)
	ctx := testContext(t)
	capture, err := cast.Start(ctx, "DP-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if capture.CanType() {
		t.Error("a plain cast claims it can type")
	}
	if err := capture.Type(ctx, "é"); !errors.Is(err, ErrNoKeysyms) {
		t.Errorf("Type = %v, want ErrNoKeysyms", err)
	}
	if err := capture.Close(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{"create screen cast", "record monitor DP-1", "start screen cast", "stop screen cast"}
	if got := m.entries(); !slices.Equal(got, want) {
		t.Errorf("Mutter saw %q, want %q", got, want)
	}
}

// TestARefusedRemoteDesktopCastFallsBackToAPlainOne: when Mutter makes the
// remote desktop session but refuses a screen cast in it, the cast is a
// plain one, as before.
func TestARefusedRemoteDesktopCastFallsBackToAPlainOne(t *testing.T) {
	m, cast := serveFakeMutter(t, false)
	m.mu.Lock()
	m.refuseLinked = true
	m.mu.Unlock()
	capture, err := cast.Start(testContext(t), "DP-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if capture.CanType() {
		t.Error("a plain cast claims it can type")
	}
	want := []string{"create remote desktop session", "create screen cast", "record monitor DP-1", "start screen cast"}
	if got := m.entries(); !slices.Equal(got, want) {
		t.Errorf("Mutter saw %q, want %q", got, want)
	}
}

// TestOpenSeesAFailedStreamInARemoteDesktopSession: Mutter keeps a screen-cast
// session of a remote desktop session open when its stream fails, so Open
// asks for the stream.
func TestOpenSeesAFailedStreamInARemoteDesktopSession(t *testing.T) {
	m, cast := serveFakeMutter(t, false)
	ctx := testContext(t)
	capture, err := cast.StartVirtual(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if !capture.Open(ctx) {
		t.Fatal("a running cast reads as closed")
	}
	m.mu.Lock()
	session := m.remotes["remote-1"].cast
	m.mu.Unlock()
	session.closeStream()
	if capture.Open(ctx) {
		t.Error("a cast whose stream failed reads as open")
	}
}

func TestKeysym(t *testing.T) {
	cases := []struct {
		name string
		r    rune
		want uint32
		ok   bool
	}{
		{"printable ASCII is its own keysym", 'q', 'q', true},
		{"a space is its own keysym", ' ', 0x20, true},
		{"Latin-1 is its own keysym", 'é', 0xe9, true},
		{"a no-break space is Latin-1", 0xa0, 0xa0, true},
		{"Cyrillic has legacy keysyms", 'Ж', 0x6f6, true},
		{"Polish ą has a Latin-2 keysym", 'ą', 0x1b1, true},
		{"the euro sign has its own keysym", '€', 0x20ac, true},
		{"Greek has legacy keysyms", 'λ', 0x7eb, true},
		{"a character without one is a Unicode keysym", 'ə', 0x01000259, true},
		{"an emoji is a Unicode keysym", '😀', 0x0101f600, true},
		{"a newline presses Return", '\n', keysymReturn, true},
		{"a carriage return presses Return", '\r', keysymReturn, true},
		{"a tab presses Tab", '\t', keysymTab, true},
		{"a backspace presses BackSpace", '\b', keysymBackSpace, true},
		{"other control characters are ignored", 0x1b, 0, false},
		{"the null character is ignored", 0, 0, false},
		{"delete is ignored", 0x7f, 0, false},
		{"C1 control characters are ignored", 0x85, 0, false},
		{"a lone surrogate is ignored", 0xd800, 0, false},
		{"a noncharacter is ignored", 0xfffe, 0, false},
		{"a code point beyond Unicode is ignored", 0x110000, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := Keysym(c.r)
			if got != c.want || ok != c.ok {
				t.Errorf("Keysym(%U) = %#x, %v; want %#x, %v", c.r, got, ok, c.want, c.ok)
			}
		})
	}
}
