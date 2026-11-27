package video

/*
#include <stdlib.h>
#include "cursorhook.h"
*/
import "C"

import (
	"sync/atomic"
	"unsafe"
)

// CursorUpdate is the pointer as Mutter's screen cast describes it
// (cursorhook.c): its position in the captured monitor's pixels, the point
// its hotspot is at, and its shape when that changed.
type CursorUpdate struct {
	X, Y int32
	// Shape is the new shape, nil when only the position changed.
	Shape *CursorShape
}

// CursorShape is a pointer image: W x H pixels of premultiplied RGBA, rows
// back to back, with the hotspot at HotX, HotY. A hidden pointer is a single
// transparent pixel.
type CursorShape struct {
	W, H       int
	HotX, HotY int
	RGBA       []byte
}

// SPA video formats of pointer bitmaps (spa/param/video/raw.h).
const (
	spaRGBA = 11
	spaBGRA = 12
)

var cursorHandler atomic.Pointer[func(CursorUpdate)]

// SetCursorHandler makes h receive the pointer updates of every screen cast
// with cursor-mode metadata, until stop is called or another handler takes
// over (a session that replaces another). h runs on the capture's streaming
// thread and must not block.
func SetCursorHandler(h func(CursorUpdate)) (stop func()) {
	p := &h
	cursorHandler.Store(p)
	C.desklink_cursor_forget()
	return func() { cursorHandler.CompareAndSwap(p, nil) }
}

// CursorHookReady reports whether the executable exports its
// pw_stream_dequeue_buffer, so the pipewire plugin's calls reach the hook.
// Only desklinkd's build exports it (Makefile); without it a cast with
// cursor-mode metadata would show no pointer at all.
func CursorHookReady() bool { return C.desklink_cursor_hook_exported() != 0 }

// CursorHookBuffers counts the PipeWire buffers the hook has seen, so a
// session can tell that it works.
func CursorHookBuffers() int64 { return int64(C.desklink_cursor_hook_buffers()) }

//export desklinkCursor
func desklinkCursor(x, y, w, h, stride C.int, format C.uint32_t, hotX, hotY C.int, pixels unsafe.Pointer) {
	hp := cursorHandler.Load()
	if hp == nil {
		return
	}
	u := CursorUpdate{X: int32(x), Y: int32(y)}
	if pixels != nil && w > 0 && h > 0 {
		u.Shape = cursorShape(int(w), int(h), int(stride), uint32(format), int(hotX), int(hotY),
			unsafe.Slice((*byte)(pixels), int(stride)*int(h)))
	}
	(*hp)(u)
}

// cursorShape copies a bitmap of the given SPA format into premultiplied
// RGBA; nil for a format Mutter does not send.
func cursorShape(w, h, stride int, format uint32, hotX, hotY int, src []byte) *CursorShape {
	if format != spaRGBA && format != spaBGRA {
		return nil
	}
	out := make([]byte, w*h*4)
	for y := range h {
		row := src[y*stride : y*stride+w*4]
		dst := out[y*w*4 : (y+1)*w*4]
		copy(dst, row)
		if format == spaBGRA {
			for i := 0; i < len(dst); i += 4 {
				dst[i], dst[i+2] = dst[i+2], dst[i]
			}
		}
	}
	return &CursorShape{W: w, H: h, HotX: hotX, HotY: hotY, RGBA: out}
}

// inspectTestBuffer runs the hook's reader on a synthetic buffer, for tests.
func inspectTestBuffer(x, y, w, h, hotX, hotY int, format uint32, empty bool) {
	e := 0
	if empty {
		e = 1
	}
	b := C.desklink_cursor_test_buffer(C.int(x), C.int(y), C.int(w), C.int(h), C.int(hotX), C.int(hotY), C.uint32_t(format), C.int(e))
	defer C.free(b)
	C.desklink_cursor_inspect(b)
}
