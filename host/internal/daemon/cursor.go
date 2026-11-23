package daemon

import dlnet "desklink/internal/net"

// clientCursor is where the client's pointer is on the screen it sees, in
// the stream's absolute coordinates (0..AbsMax across the frame).
type clientCursor struct {
	x, y float64
}

func newClientCursor() clientCursor {
	return clientCursor{x: absMax / 2, y: absMax / 2}
}

const absMax = 65535

// move applies a relative motion of dx, dy pixels on a screen of w x h
// pixels, stopping at its edges.
func (c *clientCursor) move(dx, dy int32, w, h int) {
	c.x = min(max(c.x+float64(dx)*absMax/float64(max(w, 1)), 0), absMax)
	c.y = min(max(c.y+float64(dy)*absMax/float64(max(h, 1)), 0), absMax)
}

func (c *clientCursor) set(x, y int32) {
	c.x, c.y = float64(min(max(x, 0), absMax)), float64(min(max(y, 0), absMax))
}

func (c *clientCursor) at() dlnet.InputEvent {
	return dlnet.InputEvent{Kind: dlnet.InputAbsPointer, X: int32(c.x), Y: int32(c.y)}
}

// confine rewrites the client's input so its pointer stays on the screen it
// sees. GNOME has one pointer for all monitors, so relative motion from a
// trackpad or mouse would move it wherever it is, often onto a monitor the
// client does not show. Relative motion therefore becomes an absolute position
// on the client's screen, and a scroll or button is preceded by one, in case
// someone at the desk moved the pointer meanwhile. Relative motion is in
// pixels of the client's w x h screen, whatever size the stream has now.
func confine(c *clientCursor, events []dlnet.InputEvent, w, h int) []dlnet.InputEvent {
	out := make([]dlnet.InputEvent, 0, len(events)+1)
	placed := false
	for _, e := range events {
		switch e.Kind {
		case dlnet.InputRelMove:
			c.move(e.X, e.Y, w, h)
			out = append(out, c.at())
			placed = true
			continue
		case dlnet.InputAbsPointer, dlnet.InputAbsMove:
			c.set(e.X, e.Y)
			placed = true
		case dlnet.InputScroll, dlnet.InputMouseButton:
			if !placed {
				out = append(out, c.at())
				placed = true
			}
		}
		out = append(out, e)
	}
	return out
}
