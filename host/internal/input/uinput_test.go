package input

import (
	"bytes"
	"encoding/binary"
	"testing"
)

type report []rawEvent

// decode splits written bytes back into reports ending in SYN_REPORT.
func decode(t *testing.T, b []byte) []report {
	t.Helper()
	if len(b)%24 != 0 {
		t.Fatalf("%d bytes is not a whole number of events", len(b))
	}
	var out []report
	var cur report
	for ; len(b) > 0; b = b[24:] {
		e := rawEvent{binary.LittleEndian.Uint16(b[16:]), binary.LittleEndian.Uint16(b[18:]), int32(binary.LittleEndian.Uint32(b[20:]))}
		if e.typ == evSyn {
			out = append(out, cur)
			cur = nil
			continue
		}
		cur = append(cur, e)
	}
	if cur != nil {
		t.Fatal("trailing events without SYN_REPORT")
	}
	return out
}

func newTestInjector() (*Injector, *bytes.Buffer, *bytes.Buffer, *bytes.Buffer) {
	var kbd, mouse, pen, abs bytes.Buffer
	return newInjector(&kbd, &mouse, &pen, &abs), &kbd, &mouse, &pen
}

func TestAbsolutePointer(t *testing.T) {
	var kbd, mouse, pen, abs bytes.Buffer
	in := newInjector(&kbd, &mouse, &pen, &abs)
	if err := in.Handle(Event{Kind: KindAbsPointer, X: 32768, Y: 70000}); err != nil {
		t.Fatal(err)
	}
	want := []report{{{evAbs, absX, 32768}, {evAbs, absY, AbsMax}}}
	if got := decode(t, abs.Bytes()); !equalReports(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if mouse.Len() != 0 || pen.Len() != 0 {
		t.Error("absolute pointer wrote to another device")
	}
}

func TestEncodeEvents(t *testing.T) {
	b := encodeEvents([]rawEvent{{evKey, 30, 1}})
	if len(b) != 24 {
		t.Fatalf("%d bytes, want 24", len(b))
	}
	if !bytes.Equal(b[:16], make([]byte, 16)) {
		t.Error("timestamp is not zero")
	}
	if binary.LittleEndian.Uint16(b[16:]) != evKey || binary.LittleEndian.Uint16(b[18:]) != 30 || binary.LittleEndian.Uint32(b[20:]) != 1 {
		t.Errorf("record %x", b)
	}
}

func TestKeyboard(t *testing.T) {
	in, kbd, _, _ := newTestInjector()
	steps := []struct {
		name string
		ev   Event
		want []report
		err  bool
	}{
		{"press", Event{Kind: KindKey, Flags: 1, Code: 29}, []report{{{evKey, 29, 1}}}, false},
		{"held key repeat is dropped", Event{Kind: KindKey, Flags: 1, Code: 29}, nil, false},
		{"release", Event{Kind: KindKey, Flags: 0, Code: 29}, []report{{{evKey, 29, 0}}}, false},
		{"code above the registered range", Event{Kind: KindKey, Flags: 1, Code: 300}, nil, true},
	}
	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			kbd.Reset()
			err := in.Handle(s.ev)
			if (err != nil) != s.err {
				t.Fatalf("err %v", err)
			}
			if got := decode(t, kbd.Bytes()); !equalReports(got, s.want) {
				t.Errorf("got %v, want %v", got, s.want)
			}
		})
	}
}

func TestMouse(t *testing.T) {
	in, _, mouse, _ := newTestInjector()
	steps := []struct {
		name string
		ev   Event
		want []report
	}{
		{"relative move", Event{Kind: KindRelMove, X: 3, Y: -4}, []report{{{evRel, relX, 3}, {evRel, relY, -4}}}},
		{"zero move writes nothing", Event{Kind: KindRelMove}, nil},
		{"right button", Event{Kind: KindMouseButton, Flags: 1, Code: 2}, []report{{{evKey, btnRight, 1}}}},
		{"half notch scroll is high resolution only", Event{Kind: KindScroll, Y: 60}, []report{{{evRel, relWheelHiRes, 60}}}},
		{"second half completes a notch", Event{Kind: KindScroll, Y: 60}, []report{{{evRel, relWheelHiRes, 60}, {evRel, relWheel, 1}}}},
		{"horizontal scroll", Event{Kind: KindScroll, X: -240}, []report{{{evRel, relHWheelHiRes, -240}, {evRel, relHWheel, -2}}}},
	}
	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			mouse.Reset()
			if err := in.Handle(s.ev); err != nil {
				t.Fatal(err)
			}
			if got := decode(t, mouse.Bytes()); !equalReports(got, s.want) {
				t.Errorf("got %v, want %v", got, s.want)
			}
		})
	}
	t.Run("unknown button", func(t *testing.T) {
		if err := in.Handle(Event{Kind: KindMouseButton, Code: 9}); err == nil {
			t.Error("accepted button 9")
		}
	})
}

func TestPen(t *testing.T) {
	in, _, _, pen := newTestInjector()
	steps := []struct {
		name string
		ev   Event
		want []report
	}{
		{"hover brings the pen into proximity", Event{Kind: KindAbsMove, Flags: PenHover, X: 100, Y: 200}, []report{{{evKey, btnToolPen, 1}, {evAbs, absX, 100}, {evAbs, absY, 200}}}},
		{"a touch without pressure draws at full pressure", Event{Kind: KindAbsMove, Flags: PenTouch, X: 70000, Y: -5},
			[]report{{{evAbs, absX, AbsMax}, {evAbs, absY, 0}, {evAbs, absPressure, PressureMax}, {evKey, btnTouch, 1}}}},
		{"lift", Event{Kind: KindAbsMove, Flags: PenHover, X: 1, Y: 1}, []report{{{evAbs, absX, 1}, {evAbs, absY, 1}, {evKey, btnTouch, 0}, {evAbs, absPressure, 0}}}},
		{"a touch with pressure and tilt", Event{Kind: KindAbsMove, Flags: PenTouch, Code: 20000, X: 2, Y: 2, Tilt: true, TiltX: 3000, TiltY: -12000},
			[]report{{{evAbs, absX, 2}, {evAbs, absY, 2}, {evAbs, absTiltX, 3000}, {evAbs, absTiltY, -TiltMax}, {evAbs, absPressure, 20000}, {evKey, btnTouch, 1}}}},
		{"unchanged tilt is not repeated", Event{Kind: KindAbsMove, Flags: PenTouch, Code: 30000, X: 3, Y: 3, Tilt: true, TiltX: 3000, TiltY: -9000},
			[]report{{{evAbs, absX, 3}, {evAbs, absY, 3}, {evAbs, absPressure, 30000}}}},
		{"leaving lifts the pen first", Event{Kind: KindAbsMove, Flags: PenProximity}, []report{{{evKey, btnTouch, 0}, {evAbs, absPressure, 0}, {evKey, btnToolPen, 0}}}},
		{"leaving twice writes nothing", Event{Kind: KindAbsMove, Flags: PenProximity}, nil},
	}
	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			pen.Reset()
			if err := in.Handle(s.ev); err != nil {
				t.Fatal(err)
			}
			if got := decode(t, pen.Bytes()); !equalReports(got, s.want) {
				t.Errorf("got %v, want %v", got, s.want)
			}
		})
	}
}

func TestReleaseAll(t *testing.T) {
	in, kbd, mouse, pen := newTestInjector()
	for _, ev := range []Event{
		{Kind: KindKey, Flags: 1, Code: 125},
		{Kind: KindMouseButton, Flags: 1, Code: 1},
		{Kind: KindAbsMove, Flags: PenTouch, X: 5, Y: 5},
	} {
		if err := in.Handle(ev); err != nil {
			t.Fatal(err)
		}
	}
	kbd.Reset()
	mouse.Reset()
	pen.Reset()
	if err := in.ReleaseAll(); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		buf  *bytes.Buffer
		want []report
	}{
		{"key", kbd, []report{{{evKey, 125, 0}}}},
		{"button", mouse, []report{{{evKey, btnLeft, 0}}}},
		{"pen", pen, []report{{{evKey, btnTouch, 0}, {evAbs, absPressure, 0}, {evKey, btnToolPen, 0}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := decode(t, c.buf.Bytes()); !equalReports(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
	t.Run("a second release writes nothing", func(t *testing.T) {
		kbd.Reset()
		if err := in.ReleaseAll(); err != nil {
			t.Fatal(err)
		}
		if kbd.Len() != 0 {
			t.Error("released keys twice")
		}
	})
}

func TestReconcile(t *testing.T) {
	type step struct {
		seq   uint32
		ev    *Event
		held  *Held
		check func(*Injector) string
	}
	keyDown := func(code uint16) *Event { return &Event{Kind: KindKey, Flags: 1, Code: code} }
	cases := []struct {
		name  string
		steps []step
	}{
		{"a key whose up packet was lost is lifted", []step{
			{seq: 1, ev: keyDown(14)},
			{seq: 5, held: &Held{}, check: func(in *Injector) string {
				if in.keys[14] {
					return "backspace still down"
				}
				return ""
			}},
		}},
		{"a key the client still holds stays down", []step{
			{seq: 1, ev: keyDown(29)},
			{seq: 5, held: &Held{Keys: []uint16{29}}, check: func(in *Injector) string {
				if !in.keys[29] {
					return "ctrl released"
				}
				return ""
			}},
		}},
		{"an older snapshot does not lift a newer press", []step{
			{seq: 9, ev: keyDown(30)},
			{seq: 8, held: &Held{}, check: func(in *Injector) string {
				if !in.keys[30] {
					return "key lifted by a stale snapshot"
				}
				return ""
			}},
		}},
		{"a held button is lifted", []step{
			{seq: 1, ev: &Event{Kind: KindMouseButton, Flags: 1, Code: 1}},
			{seq: 2, held: &Held{}, check: func(in *Injector) string {
				if in.buttons[btnLeft] {
					return "left button still down"
				}
				return ""
			}},
		}},
		{"a pen contact is lifted but stays in range", []step{
			{seq: 1, ev: &Event{Kind: KindAbsMove, Flags: PenTouch, X: 5, Y: 5}},
			{seq: 2, held: &Held{PenInRange: true}, check: func(in *Injector) string {
				if in.penDown || !in.penIn {
					return "pen not lifted to hover"
				}
				return ""
			}},
		}},
		{"a snapshot never presses", []step{
			{seq: 3, held: &Held{Keys: []uint16{30}}, check: func(in *Injector) string {
				if in.keys[30] {
					return "snapshot pressed a key"
				}
				return ""
			}},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in, _, _, _ := newTestInjector()
			for _, s := range c.steps {
				if s.ev != nil {
					if err := in.HandleAt(s.seq, *s.ev); err != nil {
						t.Fatal(err)
					}
				}
				if s.held != nil {
					if err := in.Reconcile(s.seq, *s.held); err != nil {
						t.Fatal(err)
					}
				}
				if s.check != nil {
					if msg := s.check(in); msg != "" {
						t.Error(msg)
					}
				}
			}
		})
	}
}

func TestReorderedAndNewSessionSequences(t *testing.T) {
	t.Run("a late key-up from an older packet is ignored", func(t *testing.T) {
		in, kbd, _, _ := newTestInjector()
		if err := in.HandleAt(6, Event{Kind: KindKey, Flags: 1, Code: 30}); err != nil {
			t.Fatal(err)
		}
		kbd.Reset()
		if err := in.HandleAt(5, Event{Kind: KindKey, Flags: 0, Code: 30}); err != nil {
			t.Fatal(err)
		}
		if kbd.Len() != 0 || !in.keys[30] {
			t.Error("a stale key-up released a newer press")
		}
	})
	t.Run("a new session's low sequence numbers are not stale", func(t *testing.T) {
		in, _, _, _ := newTestInjector()
		if err := in.HandleAt(9000, Event{Kind: KindKey, Flags: 1, Code: 30}); err != nil {
			t.Fatal(err)
		}
		if err := in.ReleaseAll(); err != nil {
			t.Fatal(err)
		}
		if err := in.HandleAt(1, Event{Kind: KindKey, Flags: 1, Code: 30}); err != nil {
			t.Fatal(err)
		}
		if err := in.Reconcile(2, Held{}); err != nil {
			t.Fatal(err)
		}
		if in.keys[30] {
			t.Error("the new session's snapshot could not lift the key")
		}
	})
}

func equalReports(a, b []report) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i]) != len(b[i]) {
			return false
		}
		for j := range a[i] {
			if a[i][j] != b[i][j] {
				return false
			}
		}
	}
	return true
}

func TestButtonsFollowThePointerDevice(t *testing.T) {
	var kbd, mouse, pen, abs bytes.Buffer
	in := newInjector(&kbd, &mouse, &pen, &abs)
	press := Event{Kind: KindMouseButton, Flags: 1, Code: 1}
	release := Event{Kind: KindMouseButton, Flags: 0, Code: 1}
	step := func(ev Event) {
		t.Helper()
		if err := in.Handle(ev); err != nil {
			t.Fatal(err)
		}
	}
	left := func(v int32) report { return report{{evKey, btnLeft, v}} }

	// A tap: absolute move, then the click on the same device.
	step(Event{Kind: KindAbsPointer, X: 100, Y: 100})
	abs.Reset()
	step(press)
	step(release)
	if got := decode(t, abs.Bytes()); !equalReports(got, []report{left(1), left(0)}) {
		t.Errorf("click after an absolute move on the pointer device: %v", got)
	}
	if mouse.Len() != 0 {
		t.Error("click after an absolute move went to the relative mouse")
	}

	// A press on the pointer device is released there even after a relative move.
	step(press)
	step(Event{Kind: KindRelMove, X: 5})
	abs.Reset()
	mouse.Reset()
	step(release)
	if got := decode(t, abs.Bytes()); !equalReports(got, []report{left(0)}) {
		t.Errorf("release did not go to the pressing device: %v", got)
	}

	// After a relative move, clicks use the relative mouse again.
	mouse.Reset()
	abs.Reset()
	step(press)
	step(release)
	if got := decode(t, mouse.Bytes()); !equalReports(got, []report{left(1), left(0)}) {
		t.Errorf("click after a relative move: %v", got)
	}
	if abs.Len() != 0 {
		t.Error("click after a relative move went to the pointer device")
	}
}
