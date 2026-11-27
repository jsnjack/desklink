package daemon

import (
	"testing"

	"desklink/internal/input"
	dlnet "desklink/internal/net"
	"desklink/internal/video"
)

func TestCursorFrame(t *testing.T) {
	t.Run("the own screen fills the frame", func(t *testing.T) {
		f := cursorFrame{cw: 1, ch: 1, sw: 2560, sh: 1600}
		if got, want := f.position(1280, 400), (dlnet.CursorPosition{X: 32767, Y: 16383}); got != want {
			t.Errorf("position %+v, want %+v", got, want)
		}
		if got := f.span(); got != 1.0/2560 {
			t.Errorf("span %v, want a 2560th of the frame", got)
		}
	})
	t.Run("a letterboxed monitor sits between the borders", func(t *testing.T) {
		// A 2560x1440 monitor in a 2560x1600 frame: 80 rows of border above.
		f := cursorFrame{cx: 0, cy: 80.0 / 1600, cw: 1, ch: 1440.0 / 1600, sw: 2560, sh: 1440}
		top := 80.0 / 1600 * absMax
		if got := f.position(0, 0); got.Y != uint16(top) {
			t.Errorf("the monitor's top is at %d, want below the border", got.Y)
		}
	})
	t.Run("a pointer off the monitor stays on the frame's edge", func(t *testing.T) {
		f := cursorFrame{cw: 1, ch: 1, sw: 100, sh: 100}
		if got, want := f.position(-50, 500), (dlnet.CursorPosition{X: 0, Y: absMax}); got != want {
			t.Errorf("position %+v, want %+v", got, want)
		}
	})
}

func TestCursorRelayKeepsTheNewestPositionAndEveryShape(t *testing.T) {
	r := newCursorRelay()
	shape := &video.CursorShape{W: 1, H: 1, RGBA: []byte{0, 0, 0, 0}}
	r.put(video.CursorUpdate{X: 1, Y: 1, Shape: shape})
	r.put(video.CursorUpdate{X: 2, Y: 2})
	r.put(video.CursorUpdate{X: 3, Y: 3})
	select {
	case <-r.pending:
	default:
		t.Fatal("no update pending")
	}
	pos, got := r.take()
	if pos == nil || pos.X != 3 || got != shape {
		t.Errorf("took %+v and %v, want the newest position and the shape", pos, got)
	}
	if pos, got := r.take(); pos != nil || got != nil {
		t.Errorf("took %+v and %v again", pos, got)
	}
}

// After someone at the desk moved the pointer, the client's next move starts
// where the desk left it, as on the client, instead of jumping back.
func TestTheClientsNextMoveStartsWhereTheDeskLeftThePointer(t *testing.T) {
	f := newSessionFixture(t)
	f.s.pointer = identityMap
	f.s.deskPointer.Store(&dlnet.CursorPosition{X: 1000, Y: 2000})
	f.s.injectInput(1, []dlnet.InputEvent{{Kind: dlnet.InputRelMove, X: 0, Y: 0}})
	var got []input.Event
	for _, ev := range f.in.events {
		if ev.Kind == input.KindAbsPointer {
			got = append(got, ev)
		}
	}
	if len(got) != 1 || got[0].X != 1000 || got[0].Y != 2000 {
		t.Errorf("pointer records %+v, want one at 1000,2000", got)
	}
	if f.s.deskPointer.Load() != nil {
		t.Error("the desk's position is applied again")
	}
	if !movesPointer([]dlnet.InputEvent{{Kind: dlnet.InputAbsPointer}}) || movesPointer([]dlnet.InputEvent{{Kind: dlnet.InputKey}}) {
		t.Error("movesPointer")
	}
}
