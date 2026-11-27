//go:build cursorprobe

package video

import (
	"context"
	"sync"
	"testing"
	"time"

	"desklink/internal/desktop"
)

// TestLivePointerApart mirrors DP-2 with the pointer as metadata through the
// production pipeline, moves the pointer and reports what the session's
// handler got. Run with the Makefile's export flag:
//
//	go test -tags cursorprobe -ldflags=-extldflags=-Wl,--export-dynamic-symbol=pw_stream_dequeue_buffer \
//	  ./internal/video -run TestLivePointerApart -v
func TestLivePointerApart(t *testing.T) {
	if !CursorHookReady() {
		t.Fatal("the test binary does not export the hook")
	}
	Init()
	bus, err := desktop.ConnectBus()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	c, err := desktop.NewScreenCast(bus.Session).Start(ctx, "DP-2", true)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	var mu sync.Mutex
	var updates, shapes int
	var last CursorUpdate
	var shape *CursorShape
	defer SetCursorHandler(func(u CursorUpdate) {
		mu.Lock()
		defer mu.Unlock()
		updates++
		last = u
		if u.Shape != nil {
			shapes++
			shape = u.Shape
		}
	})()
	frames := 0
	enc, err := Start(Settings{Codec: CodecAV1, Width: 1280, Height: 720, QP: 20, FPS: 60, NodeID: c.NodeID}, func(Frame) { frames++ })
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	for i := 0; i < 200; i++ {
		dx := 4.0
		if i >= 100 {
			dx = -4
		}
		if err := c.MovePointer(ctx, dx, 0); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	enc.Stop()
	mu.Lock()
	defer mu.Unlock()
	t.Logf("%d frames encoded; %d pointer updates, %d shapes; last at %d,%d", frames, updates, shapes, last.X, last.Y)
	if shape != nil {
		t.Logf("shape %dx%d hotspot %d,%d", shape.W, shape.H, shape.HotX, shape.HotY)
	}
	if updates < 50 || shapes == 0 {
		t.Error("the pointer's moves and shape did not arrive")
	}
}
