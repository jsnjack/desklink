package desktop

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	mutterRemoteBus    = "org.gnome.Mutter.RemoteDesktop"
	mutterRemotePath   = "/org/gnome/Mutter/RemoteDesktop"
	ifaceMutterRemote  = "org.gnome.Mutter.RemoteDesktop"
	ifaceRemoteSession = "org.gnome.Mutter.RemoteDesktop.Session"

	// remoteSessionIDProp ties a new screen-cast session to a remote desktop
	// session, which then starts and stops it.
	remoteSessionIDProp = "remote-desktop-session-id"
)

// ErrNoKeysyms means a screen cast has no remote desktop session to type
// text through.
var ErrNoKeysyms = errors.New("this computer cannot type the client's text: Mutter gave the session no remote desktop session")

// createRemoteDesktop creates a Mutter remote desktop session, the one a
// screen-cast session can belong to, and returns its object path and ID.
// Only Start creates its virtual input devices and the remote-access handle
// GNOME Shell shows, so an unstarted session holds nothing.
func (s *ScreenCast) createRemoteDesktop(ctx context.Context) (dbus.ObjectPath, string, error) {
	var path dbus.ObjectPath
	err := s.conn.Object(mutterRemoteBus, mutterRemotePath).
		CallWithContext(ctx, ifaceMutterRemote+".CreateSession", 0).Store(&path)
	if err != nil {
		return "", "", fmt.Errorf("create remote desktop session: %w", err)
	}
	v, err := getProperty(ctx, s.conn.Object(mutterRemoteBus, path), ifaceRemoteSession, "SessionId")
	if err != nil {
		return "", "", fmt.Errorf("read the remote desktop session's ID: %w", err)
	}
	id, _ := v.Value().(string)
	if id == "" {
		return "", "", errors.New("the remote desktop session has no ID")
	}
	return path, id, nil
}

// CanType reports whether Type works: the cast belongs to a remote desktop
// session.
func (c *Capture) CanType() bool { return c.remote != "" }

// Type types text through the remote desktop session's virtual keyboard,
// pressing and releasing each character's keysym (Keysym) in order;
// characters Keysym rejects are skipped and "\r\n" is one Return. Mutter
// looks for a key with the keysym in the active layout only, at its first
// three levels (plain, Shift, AltGr), and drops a keysym the layout lacks
// without an error (meta-virtual-input-device-native.c, notify_keyval).
//
// The calls go out without waiting for each reply, since Mutter handles one
// connection's calls in order, and Type then waits for every reply. A
// press is always followed by its release, even when ctx ends; stopping the
// remote desktop session also releases whatever its keyboard holds.
func (c *Capture) Type(ctx context.Context, text string) error {
	if c.remote == "" {
		return ErrNoKeysyms
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var keysyms []uint32
	for _, r := range text {
		if k, ok := Keysym(r); ok {
			keysyms = append(keysyms, k)
		}
	}
	obj := c.conn.Object(mutterRemoteBus, c.remote)
	// Room for every reply, so godbus never waits to deliver one.
	replies := make(chan *dbus.Call, 2*len(keysyms))
	sent := 0
	for _, k := range keysyms {
		if ctx.Err() != nil {
			break
		}
		obj.Go(ifaceRemoteSession+".NotifyKeyboardKeysym", 0, replies, k, true)
		obj.Go(ifaceRemoteSession+".NotifyKeyboardKeysym", 0, replies, k, false)
		sent += 2
	}
	var err error
	for range sent {
		select {
		case call := <-replies:
			if call.Err != nil && err == nil {
				err = fmt.Errorf("type a keysym: %w", call.Err)
			}
		case <-ctx.Done():
			return fmt.Errorf("type text: %w", ctx.Err())
		}
	}
	if err == nil && sent < 2*len(keysyms) {
		err = fmt.Errorf("type text: %w", ctx.Err())
	}
	return err
}

// Flags of NotifyPointerAxis (org.gnome.Mutter.RemoteDesktop.xml).
const (
	axisFinish       uint32 = 1
	axisSourceFinger uint32 = 4
)

// Scroll scrolls where the pointer is as a touchpad's fingers do, by dx, dy
// logical pixels (positive scrolls right and down, so the content moves left
// and up), through the remote desktop session's virtual pointer. Apps then
// scroll by exactly that, without the wheel's steps and animation, and
// finish, sent when the fingers lift, lets them carry a fast scroll on
// (kinetic scrolling). Mutter takes these as libinput's touchpad scroll
// deltas (meta_seat_impl_notify_scroll_continuous_in_impl).
//
// The cast's first scroll waits for Mutter's answer (at most
// scrollCheckTimeout), so a Mutter that refuses the call is noticed; later
// ones go out without waiting, so a scroll never holds up the client's input.
// Mutter handles one connection's calls in order.
func (c *Capture) Scroll(dx, dy float64, finish bool) error {
	if c.remote == "" {
		return ErrNoKeysyms
	}
	flags := axisSourceFinger
	if finish {
		flags |= axisFinish
	}
	obj := c.conn.Object(mutterRemoteBus, c.remote)
	method := ifaceRemoteSession + ".NotifyPointerAxis"
	if c.scrollChecked.Load() {
		if call := obj.Go(method, dbus.FlagNoReplyExpected, nil, dx, dy, flags); call.Err != nil {
			return fmt.Errorf("scroll: %w", call.Err)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), scrollCheckTimeout)
	defer cancel()
	if err := obj.CallWithContext(ctx, method, 0, dx, dy, flags).Err; err != nil {
		return fmt.Errorf("scroll: %w", err)
	}
	c.scrollChecked.Store(true)
	return nil
}

// scrollCheckTimeout bounds the wait for Mutter's answer to a cast's first
// scroll.
const scrollCheckTimeout = 500 * time.Millisecond
