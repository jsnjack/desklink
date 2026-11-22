package desktop

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/godbus/dbus/v5"
)

const (
	portalBus          = "org.freedesktop.portal.Desktop"
	portalPath         = "/org/freedesktop/portal/desktop"
	ifaceScreenCast    = "org.freedesktop.portal.ScreenCast"
	ifaceRequest       = "org.freedesktop.portal.Request"
	ifacePortalSession = "org.freedesktop.portal.Session"

	sourceMonitor   uint32 = 1
	cursorEmbedded  uint32 = 2
	persistUntilRev uint32 = 2
)

// ErrCaptureDenied means the user cancelled or GNOME refused screen sharing.
var ErrCaptureDenied = errors.New("screen sharing was not approved")

// TokenStore persists the ScreenCast restore token.
type TokenStore interface {
	RestoreToken() string
	SetRestoreToken(token string) error
}

// ScreenCast starts xdg-desktop-portal screen casts of one monitor. With
// persist_mode 2 and a saved restore token, GNOME starts the cast without a
// dialog; each start returns a new token that replaces the old one.
type ScreenCast struct {
	conn   *dbus.Conn
	tokens TokenStore
}

// NewScreenCast returns a ScreenCast on the session bus.
func NewScreenCast(conn *dbus.Conn, tokens TokenStore) *ScreenCast {
	return &ScreenCast{conn: conn, tokens: tokens}
}

// Capture is a running portal session with one PipeWire stream.
type Capture struct {
	conn    *dbus.Conn
	session dbus.ObjectPath
	NodeID  uint32
	Width   int32
	Height  int32
}

type stream struct {
	NodeID uint32
	Props  map[string]dbus.Variant
}

// Start runs CreateSession, SelectSources and Start. If GNOME has to ask the
// user (first run, or a monitor change invalidated the token), Start blocks
// until the dialog is answered or ctx ends.
func (s *ScreenCast) Start(ctx context.Context) (*Capture, error) {
	sessionToken := newToken()
	res, err := s.request(ctx, "CreateSession", func(handle string) []any {
		return []any{map[string]dbus.Variant{
			"handle_token":         dbus.MakeVariant(handle),
			"session_handle_token": dbus.MakeVariant(sessionToken),
		}}
	})
	if err != nil {
		return nil, fmt.Errorf("create screencast session: %w", err)
	}
	var sessionPath string
	if err := res["session_handle"].Store(&sessionPath); err != nil {
		return nil, fmt.Errorf("create screencast session: read session_handle: %w", err)
	}
	c := &Capture{conn: s.conn, session: dbus.ObjectPath(sessionPath)}
	ok := false
	defer func() {
		if !ok {
			if err := c.Close(); err != nil {
				slog.Debug("close failed screencast session", "err", err)
			}
		}
	}()

	_, err = s.request(ctx, "SelectSources", func(handle string) []any {
		opts := map[string]dbus.Variant{
			"handle_token": dbus.MakeVariant(handle),
			"types":        dbus.MakeVariant(sourceMonitor),
			"cursor_mode":  dbus.MakeVariant(cursorEmbedded),
			"persist_mode": dbus.MakeVariant(persistUntilRev),
			"multiple":     dbus.MakeVariant(false),
		}
		if t := s.tokens.RestoreToken(); t != "" {
			opts["restore_token"] = dbus.MakeVariant(t)
		}
		return []any{c.session, opts}
	})
	if err != nil {
		return nil, fmt.Errorf("select screencast source: %w", err)
	}

	res, err = s.request(ctx, "Start", func(handle string) []any {
		return []any{c.session, "", map[string]dbus.Variant{"handle_token": dbus.MakeVariant(handle)}}
	})
	if err != nil {
		return nil, fmt.Errorf("start screencast: %w", err)
	}
	if v, found := res["restore_token"]; found {
		var token string
		if err := v.Store(&token); err == nil && token != "" {
			if err := s.tokens.SetRestoreToken(token); err != nil {
				slog.Warn("save screencast restore token", "err", err)
			}
		}
	}
	var streams []stream
	if err := res["streams"].Store(&streams); err != nil || len(streams) == 0 {
		return nil, fmt.Errorf("start screencast: no stream in response: %v", err)
	}
	c.NodeID = streams[0].NodeID
	if v, found := streams[0].Props["size"]; found {
		var size struct{ W, H int32 }
		if err := v.Store(&size); err == nil {
			c.Width, c.Height = size.W, size.H
		}
	}
	ok = true
	return c, nil
}

// OpenPipeWireRemote returns a new PipeWire connection for this cast.
// pipewiresrc duplicates it, so the caller closes the file after the
// pipeline stops.
func (c *Capture) OpenPipeWireRemote(ctx context.Context) (*os.File, error) {
	var fd dbus.UnixFD
	obj := c.conn.Object(portalBus, portalPath)
	if err := obj.CallWithContext(ctx, ifaceScreenCast+".OpenPipeWireRemote", 0, c.session, map[string]dbus.Variant{}).Store(&fd); err != nil {
		return nil, fmt.Errorf("open PipeWire remote: %w", err)
	}
	return os.NewFile(uintptr(fd), "pipewire-remote"), nil
}

// Close ends the portal session.
func (c *Capture) Close() error {
	if err := c.conn.Object(portalBus, c.session).Call(ifacePortalSession+".Close", 0).Err; err != nil {
		return fmt.Errorf("close screencast session: %w", err)
	}
	return nil
}

// request calls a portal method that answers through a Request object and
// waits for its Response signal. The handle token makes the Request path
// predictable, so the signal match is in place before the call.
func (s *ScreenCast) request(ctx context.Context, method string, args func(handle string) []any) (map[string]dbus.Variant, error) {
	handle := newToken()
	names := s.conn.Names()
	if len(names) == 0 {
		return nil, errors.New("session bus connection has no unique name")
	}
	sender := strings.ReplaceAll(strings.TrimPrefix(names[0], ":"), ".", "_")
	path := dbus.ObjectPath(portalPath + "/request/" + sender + "/" + handle)

	match := []dbus.MatchOption{
		dbus.WithMatchObjectPath(path),
		dbus.WithMatchInterface(ifaceRequest),
		dbus.WithMatchMember("Response"),
	}
	if err := s.conn.AddMatchSignalContext(ctx, match...); err != nil {
		return nil, fmt.Errorf("subscribe to portal response: %w", err)
	}
	defer func() {
		if err := s.conn.RemoveMatchSignal(match...); err != nil {
			slog.Debug("unsubscribe portal response", "err", err)
		}
	}()
	signals := make(chan *dbus.Signal, 8)
	s.conn.Signal(signals)
	defer s.conn.RemoveSignal(signals)

	obj := s.conn.Object(portalBus, portalPath)
	if err := obj.CallWithContext(ctx, ifaceScreenCast+"."+method, 0, args(handle)...).Err; err != nil {
		return nil, fmt.Errorf("call %s: %w", method, err)
	}
	for {
		select {
		case <-ctx.Done():
			if err := s.conn.Object(portalBus, path).Call(ifaceRequest+".Close", 0).Err; err != nil {
				slog.Debug("close portal request", "err", err)
			}
			return nil, fmt.Errorf("%s: %w", method, ctx.Err())
		case sig := <-signals:
			if sig.Path != path || sig.Name != ifaceRequest+".Response" || len(sig.Body) < 2 {
				continue
			}
			code, _ := sig.Body[0].(uint32)
			results, _ := sig.Body[1].(map[string]dbus.Variant)
			if code != 0 {
				return nil, fmt.Errorf("%s: %w (portal response %d)", method, ErrCaptureDenied, code)
			}
			return results, nil
		}
	}
}

func newToken() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return "desklink_" + hex.EncodeToString(b)
}
