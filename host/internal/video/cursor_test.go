package video

import (
	"bytes"
	"testing"
)

// collectCursor records the updates the hook reports while the test runs.
func collectCursor(t *testing.T) *[]CursorUpdate {
	t.Helper()
	var got []CursorUpdate
	t.Cleanup(SetCursorHandler(func(u CursorUpdate) { got = append(got, u) }))
	return &got
}

func TestCursorHookReadsMuttersMetadata(t *testing.T) {
	t.Run("a shape comes with its position, hotspot and pixels", func(t *testing.T) {
		got := collectCursor(t)
		inspectTestBuffer(100, 200, 2, 2, 1, 0, spaRGBA, false)
		if len(*got) != 1 || (*got)[0].Shape == nil {
			t.Fatalf("updates %+v, want one with a shape", *got)
		}
		u := (*got)[0]
		want := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
		if u.X != 100 || u.Y != 200 || u.Shape.W != 2 || u.Shape.H != 2 || u.Shape.HotX != 1 || u.Shape.HotY != 0 ||
			!bytes.Equal(u.Shape.RGBA, want) {
			t.Errorf("update %+v shape %+v", u, *u.Shape)
		}
	})
	t.Run("a BGRA bitmap becomes RGBA", func(t *testing.T) {
		got := collectCursor(t)
		inspectTestBuffer(1, 1, 1, 1, 0, 0, spaBGRA, false)
		if len(*got) != 1 || !bytes.Equal((*got)[0].Shape.RGBA, []byte{2, 1, 0, 3}) {
			t.Errorf("updates %+v", *got)
		}
	})
	t.Run("a pointer-only buffer moves the pointer, and an unchanged one is not reported", func(t *testing.T) {
		got := collectCursor(t)
		inspectTestBuffer(5, 6, 0, 0, 0, 0, 0, true)
		inspectTestBuffer(5, 6, 0, 0, 0, 0, 0, true)
		inspectTestBuffer(7, 6, 0, 0, 0, 0, 0, false)
		if len(*got) != 2 || (*got)[0].X != 5 || (*got)[1].X != 7 || (*got)[0].Shape != nil {
			t.Errorf("updates %+v, want 5,6 then 7,6 without shapes", *got)
		}
	})
	t.Run("a new handler hears the same position again", func(t *testing.T) {
		inspectTestBuffer(9, 9, 0, 0, 0, 0, 0, true)
		got := collectCursor(t)
		inspectTestBuffer(9, 9, 0, 0, 0, 0, 0, true)
		if len(*got) != 1 {
			t.Errorf("updates %+v, want the position for the new cast", *got)
		}
	})
	t.Run("an unknown format keeps the position and drops the shape", func(t *testing.T) {
		got := collectCursor(t)
		inspectTestBuffer(3, 4, 1, 1, 0, 0, 7, false)
		if len(*got) != 1 || (*got)[0].Shape != nil || (*got)[0].X != 3 {
			t.Errorf("updates %+v", *got)
		}
	})
}

func TestAReplacedCursorHandlerDoesNotStopItsSuccessor(t *testing.T) {
	stopOld := SetCursorHandler(func(CursorUpdate) {})
	got := collectCursor(t)
	stopOld()
	inspectTestBuffer(11, 12, 0, 0, 0, 0, 0, true)
	if len(*got) != 1 {
		t.Errorf("updates %+v after the old session stopped its handler", *got)
	}
}

// The tests' binary does not export the hook; only desklinkd's build does.
func TestCursorHookIsNotExportedWithoutTheLinkerFlag(t *testing.T) {
	if CursorHookReady() {
		t.Error("the test binary exports pw_stream_dequeue_buffer")
	}
}
