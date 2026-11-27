package daemon

import (
	"context"
	"log/slog"
	"sync"
	"time"

	dlnet "desklink/internal/net"
	"desklink/internal/video"
)

// The pointer apart from the video: when the client draws the desk's pointer
// itself (Hello.Pointer) and desklinkd can read Mutter's pointer metadata
// (video.CursorHookReady), the casts leave the pointer out of the frames and
// the session sends where it is (PacketCursor) and what it looks like
// (MsgCursor). The client moves its own copy at once with its input, so
// pointing no longer waits for the video's round trip.

// cursorResend is how often the last position goes out again while the
// pointer stands still, for a client that missed it (UDP) or connected
// after it last moved.
const cursorResend = 500 * time.Millisecond

// pointerIdle is how long after the client's last pointer input the desk's
// reports of the pointer count: something else moved it (someone at the
// desk, an app). The session's copy of the client's pointer (clientCursor)
// then moves there, as the client's own copy does after the same time
// (LocalPointer.IDLE_MS), so the client's next move starts from the same
// place on both sides.
const pointerIdle = 300 * time.Millisecond

// cursorFrame places the captured monitor's pixels, which Mutter gives the
// pointer's position in, inside the video frame.
type cursorFrame struct {
	// cx, cy, cw, ch are the monitor's picture inside the frame, as
	// fractions, as in pointerMap.
	cx, cy, cw, ch float64
	// sw, sh are the monitor's size in the cast's pixels.
	sw, sh float64
}

// position turns a position in the cast's pixels into the frame's 0..65535
// coordinates, clamped to the frame.
func (f cursorFrame) position(x, y int32) dlnet.CursorPosition {
	conv := func(v int32, c, cs, size float64) uint16 {
		u := c + float64(v)/size*cs
		return uint16(max(0, min(u, 1)) * absMax)
	}
	return dlnet.CursorPosition{X: conv(x, f.cx, f.cw, f.sw), Y: conv(y, f.cy, f.ch, f.sh)}
}

// span is how much of the frame's width one of the cast's pixels covers.
func (f cursorFrame) span() float64 { return f.cw / f.sw }

func (f cursorFrame) known() bool { return f.sw > 0 && f.sh > 0 }

// cursorRelay hands the pointer updates from the capture's streaming thread
// to the session's relay goroutine: the latest position and the shape not
// yet sent, so a burst of updates costs the streaming thread nothing and
// the client gets the newest.
type cursorRelay struct {
	mu      sync.Mutex
	pos     *video.CursorUpdate
	shape   *video.CursorShape
	pending chan struct{}
}

func newCursorRelay() *cursorRelay { return &cursorRelay{pending: make(chan struct{}, 1)} }

// put takes an update; it never blocks.
func (r *cursorRelay) put(u video.CursorUpdate) {
	r.mu.Lock()
	r.pos = &u
	if u.Shape != nil {
		r.shape = u.Shape
	}
	r.mu.Unlock()
	select {
	case r.pending <- struct{}{}:
	default:
	}
}

// take returns the latest position and the shape to send, if any, and
// clears them.
func (r *cursorRelay) take() (*video.CursorUpdate, *video.CursorShape) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pos, shape := r.pos, r.shape
	r.pos, r.shape = nil, nil
	return pos, shape
}

// startPointerApart starts relaying the pointer when the session leaves it
// out of the frames; it stops when ctx ends.
func (s *session) startPointerApart(ctx context.Context) {
	if !s.pointerApart {
		return
	}
	relay := newCursorRelay()
	stop := video.SetCursorHandler(relay.put)
	go func() {
		defer stop()
		s.relayCursor(ctx, relay)
	}()
}

// relayCursor sends each pointer update: the shape as MsgCursor over the
// control connection, the position as PacketCursor.
func (s *session) relayCursor(ctx context.Context, relay *cursorRelay) {
	tick := time.NewTicker(cursorResend)
	defer tick.Stop()
	var last *dlnet.CursorPosition
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if last != nil {
				s.sendCursorPosition(*last)
			}
		case <-relay.pending:
			pos, shape := relay.take()
			s.pointerMu.Lock()
			frame := s.cursorFrame
			s.pointerMu.Unlock()
			if shape != nil {
				msg := dlnet.CursorShape{W: shape.W, H: shape.H, HotX: shape.HotX, HotY: shape.HotY, RGBA: shape.RGBA}
				if frame.known() {
					msg.Span = frame.span()
				}
				if err := s.send(ctx, dlnet.MsgCursor, msg); err != nil {
					slog.Debug("send the pointer's shape", "err", err)
				}
			}
			if pos != nil && frame.known() {
				p := frame.position(pos.X, pos.Y)
				last = &p
				s.sendCursorPosition(p)
				if time.Since(time.Unix(0, s.lastPointerInput.Load())) >= pointerIdle {
					s.deskPointer.Store(&p)
				}
			}
		}
	}
}

func (s *session) sendCursorPosition(p dlnet.CursorPosition) {
	if s.media == nil {
		return
	}
	if err := s.media.SendCursor(p); err != nil {
		slog.Debug("send the pointer's position", "err", err)
	}
}
