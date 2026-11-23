package desktop

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/godbus/dbus/v5"
)

const (
	mutterCastBus    = "org.gnome.Mutter.ScreenCast"
	mutterCastPath   = "/org/gnome/Mutter/ScreenCast"
	ifaceMutterCast  = "org.gnome.Mutter.ScreenCast"
	ifaceCastSession = "org.gnome.Mutter.ScreenCast.Session"
	ifaceCastStream  = "org.gnome.Mutter.ScreenCast.Stream"

	// cursorEmbedded draws the pointer into the frames.
	cursorEmbedded uint32 = 1
)

// ScreenCast records monitors through Mutter's own screen-cast API, the one
// gnome-remote-desktop uses. Unlike the ScreenCast portal it never asks the
// user, so a session can start with nobody at the desk: desklinkd runs in the
// user's session, and the pairing PIN is what authorises a client.
type ScreenCast struct {
	conn *dbus.Conn
}

// NewScreenCast returns a ScreenCast on the session bus. Mutter ends a
// screen-cast session when the bus connection that created it closes.
func NewScreenCast(conn *dbus.Conn) *ScreenCast {
	return &ScreenCast{conn: conn}
}

// Capture is a running Mutter screen-cast session with one PipeWire stream.
type Capture struct {
	conn    *dbus.Conn
	session dbus.ObjectPath
	NodeID  uint32
	// X, Y, Width and Height place the recorded monitor in the desktop's
	// logical coordinates; zero when Mutter does not report them.
	X, Y          int32
	Width, Height int32
}

// Start records the monitor named connector, or the primary monitor when
// connector is empty, with the pointer drawn into the frames. It returns
// once Mutter has created the PipeWire stream.
func (s *ScreenCast) Start(ctx context.Context, connector string) (*Capture, error) {
	var session dbus.ObjectPath
	err := s.conn.Object(mutterCastBus, mutterCastPath).
		CallWithContext(ctx, ifaceMutterCast+".CreateSession", 0, map[string]dbus.Variant{}).Store(&session)
	if err != nil {
		return nil, fmt.Errorf("create screen-cast session: %w", err)
	}
	c := &Capture{conn: s.conn, session: session}
	ok := false
	defer func() {
		if !ok {
			if err := c.Close(); err != nil {
				slog.Debug("stop failed screen-cast session", "err", err)
			}
		}
	}()

	var stream dbus.ObjectPath
	err = s.conn.Object(mutterCastBus, session).CallWithContext(ctx, ifaceCastSession+".RecordMonitor", 0,
		connector, map[string]dbus.Variant{"cursor-mode": dbus.MakeVariant(cursorEmbedded)}).Store(&stream)
	if err != nil {
		return nil, fmt.Errorf("record monitor %q: %w", connector, err)
	}

	// Subscribe before Start: the stream announces its PipeWire node once.
	match := []dbus.MatchOption{
		dbus.WithMatchObjectPath(stream),
		dbus.WithMatchInterface(ifaceCastStream),
		dbus.WithMatchMember("PipeWireStreamAdded"),
	}
	if err := s.conn.AddMatchSignalContext(ctx, match...); err != nil {
		return nil, fmt.Errorf("subscribe to screen-cast stream: %w", err)
	}
	defer func() {
		if err := s.conn.RemoveMatchSignal(match...); err != nil {
			slog.Debug("unsubscribe screen-cast stream", "err", err)
		}
	}()
	signals := make(chan *dbus.Signal, 8)
	s.conn.Signal(signals)
	defer s.conn.RemoveSignal(signals)

	if err := s.conn.Object(mutterCastBus, session).CallWithContext(ctx, ifaceCastSession+".Start", 0).Err; err != nil {
		return nil, fmt.Errorf("start screen cast: %w", err)
	}
	for c.NodeID == 0 {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for the screen-cast stream: %w", ctx.Err())
		case sig := <-signals:
			if sig.Path == stream && sig.Name == ifaceCastStream+".PipeWireStreamAdded" && len(sig.Body) == 1 {
				c.NodeID, _ = sig.Body[0].(uint32)
			}
		}
	}
	c.readParameters(stream)
	ok = true
	return c, nil
}

// readParameters reads the recorded monitor's logical position and size.
func (c *Capture) readParameters(stream dbus.ObjectPath) {
	v, err := c.conn.Object(mutterCastBus, stream).GetProperty(ifaceCastStream + ".Parameters")
	if err != nil {
		slog.Debug("read screen-cast stream parameters", "err", err)
		return
	}
	params, _ := v.Value().(map[string]dbus.Variant)
	var pos, size struct{ A, B int32 }
	if p, found := params["position"]; found && p.Store(&pos) == nil {
		c.X, c.Y = pos.A, pos.B
	}
	if p, found := params["size"]; found && p.Store(&size) == nil {
		c.Width, c.Height = size.A, size.B
	}
}

// Close stops the screen-cast session.
func (c *Capture) Close() error {
	if err := c.conn.Object(mutterCastBus, c.session).Call(ifaceCastSession+".Stop", 0).Err; err != nil {
		return fmt.Errorf("stop screen cast: %w", err)
	}
	return nil
}
