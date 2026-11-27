package desktop

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

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
// user's session, and the pairing PIN is what authorises a client. Each cast
// belongs to a Mutter remote desktop session when Mutter makes one, which
// types the client's text; keys, buttons, the pen and the pointer still go
// through uinput.
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
	stream  dbus.ObjectPath
	// remote is the remote desktop session the screen-cast session belongs
	// to, empty for a plain screen cast.
	remote dbus.ObjectPath
	NodeID uint32
	// X, Y, Width and Height place the recorded monitor in the desktop's
	// logical coordinates; zero when Mutter does not report them.
	X, Y          int32
	Width, Height int32
	// Virtual is set for a virtual monitor made for this cast.
	Virtual bool
	// scrollChecked is set once Mutter answered a Scroll, after which
	// scrolls go out without waiting for replies.
	scrollChecked atomic.Bool
}

// Start records the monitor named connector, or the primary monitor when
// connector is empty, with the pointer drawn into the frames. It returns
// once Mutter has created the PipeWire stream.
func (s *ScreenCast) Start(ctx context.Context, connector string) (*Capture, error) {
	return s.start(ctx, ifaceCastSession+".RecordMonitor", connector,
		map[string]dbus.Variant{"cursor-mode": dbus.MakeVariant(cursorEmbedded)})
}

// StartVirtual records a new virtual monitor that exists while the cast
// runs. Its size and refresh rate are those the PipeWire consumer
// negotiates, so the pipeline's caps decide them; Mutter adds it to the
// layout once the stream is negotiated.
func (s *ScreenCast) StartVirtual(ctx context.Context) (*Capture, error) {
	c, err := s.start(ctx, ifaceCastSession+".RecordVirtual", nil,
		map[string]dbus.Variant{"cursor-mode": dbus.MakeVariant(cursorEmbedded)})
	if c != nil {
		c.Virtual = true
	}
	return c, err
}

// start creates a session, calls method (with arg first, unless nil) and
// starts the cast.
func (s *ScreenCast) start(ctx context.Context, method string, arg any, props map[string]dbus.Variant) (*Capture, error) {
	session, remote, err := s.createSession(ctx)
	if err != nil {
		return nil, err
	}
	c := &Capture{conn: s.conn, session: session, remote: remote}
	ok := false
	defer func() {
		if !ok {
			closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), castStopTimeout)
			defer cancel()
			if err := c.Close(closeCtx); err != nil {
				slog.Debug("stop failed screen-cast session", "err", err)
			}
		}
	}()

	var stream dbus.ObjectPath
	args := []any{props}
	if arg != nil {
		args = []any{arg, props}
	}
	if err := s.conn.Object(mutterCastBus, session).CallWithContext(ctx, method, 0, args...).Store(&stream); err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	c.stream = stream

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

	if err := c.startSession(ctx); err != nil {
		return nil, err
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
	c.readParameters(ctx, stream)
	ok = true
	return c, nil
}

// createSession creates the screen-cast session inside a new remote desktop
// session, so the client's text can be typed (Capture.Type). When Mutter
// makes no remote desktop session, the screen-cast session is a plain one
// and remote is empty: the cast works as before, without typing.
func (s *ScreenCast) createSession(ctx context.Context) (session, remote dbus.ObjectPath, err error) {
	cast := s.conn.Object(mutterCastBus, mutterCastPath)
	remote, id, err := s.createRemoteDesktop(ctx)
	if err == nil {
		err = cast.CallWithContext(ctx, ifaceMutterCast+".CreateSession", 0,
			map[string]dbus.Variant{remoteSessionIDProp: dbus.MakeVariant(id)}).Store(&session)
		if err == nil {
			return session, remote, nil
		}
		// Mutter stops only a started remote desktop session; this one goes
		// when desklinkd's bus connection closes and holds nothing until then.
		err = fmt.Errorf("create a screen cast in the remote desktop session: %w", err)
	}
	slog.Warn("casting without a remote desktop session; the client's text cannot be typed", "err", err)
	err = cast.CallWithContext(ctx, ifaceMutterCast+".CreateSession", 0, map[string]dbus.Variant{}).Store(&session)
	if err != nil {
		return "", "", fmt.Errorf("create screen-cast session: %w", err)
	}
	return session, "", nil
}

// startSession starts the cast. A screen cast that belongs to a remote
// desktop session starts with it: Mutter refuses to start it alone.
func (c *Capture) startSession(ctx context.Context) error {
	if c.remote != "" {
		if err := c.conn.Object(mutterRemoteBus, c.remote).CallWithContext(ctx, ifaceRemoteSession+".Start", 0).Err; err != nil {
			return fmt.Errorf("start remote desktop session: %w", err)
		}
		return nil
	}
	if err := c.conn.Object(mutterCastBus, c.session).CallWithContext(ctx, ifaceCastSession+".Start", 0).Err; err != nil {
		return fmt.Errorf("start screen cast: %w", err)
	}
	return nil
}

// readParameters reads the recorded monitor's logical position and size.
func (c *Capture) readParameters(ctx context.Context, stream dbus.ObjectPath) {
	v, err := getProperty(ctx, c.conn.Object(mutterCastBus, stream), ifaceCastStream, "Parameters")
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

// Open reports whether Mutter still runs the cast's stream. Mutter closes a
// cast by itself when someone stops it from GNOME's screen-sharing
// indicator, when the desk locks (GNOME Shell then closes every screen-cast
// and remote desktop session) or when its PipeWire stream fails, and from
// then on the stream's object is no longer on the bus. The stream is asked,
// not the session: a screen-cast session that belongs to a remote desktop
// session stays open when only its stream fails.
func (c *Capture) Open(ctx context.Context) bool {
	err := c.conn.Object(mutterCastBus, c.stream).
		CallWithContext(ctx, "org.freedesktop.DBus.Properties.GetAll", 0, ifaceCastStream).Err
	if err != nil {
		slog.Debug("the screen cast has closed", "err", err)
	}
	return err == nil
}

// castStopTimeout bounds stopping a cast that failed to start.
const castStopTimeout = 5 * time.Second

// Close stops the screen-cast session. One that belongs to a remote desktop
// session stops with it: Mutter refuses to stop it alone.
func (c *Capture) Close(ctx context.Context) error {
	if c.remote != "" {
		if err := c.conn.Object(mutterRemoteBus, c.remote).CallWithContext(ctx, ifaceRemoteSession+".Stop", 0).Err; err != nil {
			return fmt.Errorf("stop remote desktop session: %w", err)
		}
		return nil
	}
	if err := c.conn.Object(mutterCastBus, c.session).CallWithContext(ctx, ifaceCastSession+".Stop", 0).Err; err != nil {
		return fmt.Errorf("stop screen cast: %w", err)
	}
	return nil
}
