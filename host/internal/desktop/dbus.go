// Package desktop controls the GNOME session for a remote session: screen
// capture through Mutter's screen-cast API, locking and unlocking, idle inhibition, monitor
// modes through Mutter, the Wayland clipboard and application launching.
package desktop

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// A layout restore retries this often while Mutter reports stale information.
const (
	restoreAttempts   = 5
	restoreRetryDelay = 300 * time.Millisecond
)

const (
	login1Bus       = "org.freedesktop.login1"
	login1Path      = "/org/freedesktop/login1"
	screenSaverBus  = "org.freedesktop.ScreenSaver"
	screenSaverPath = "/org/freedesktop/ScreenSaver"
	mutterBus       = "org.gnome.Mutter.DisplayConfig"
	mutterPath      = "/org/gnome/Mutter/DisplayConfig"

	// applyTemporary applies a monitor configuration without saving it to
	// monitors.xml and without GNOME's "keep these settings?" dialog.
	applyTemporary uint32 = 1
	powerSaveOn    int32  = 0
)

// Bus holds the system and session D-Bus connections.
type Bus struct {
	System  *dbus.Conn
	Session *dbus.Conn
}

// ConnectBus connects to both buses.
func ConnectBus() (*Bus, error) {
	sess, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("connect session bus: %w", err)
	}
	sys, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("connect system bus: %w", err), sess.Close())
	}
	return &Bus{System: sys, Session: sess}, nil
}

// Close closes both connections.
func (b *Bus) Close() error {
	return errors.Join(b.System.Close(), b.Session.Close())
}

// UnlockSession unlocks the user's graphical session and reports whether it
// was locked, so the caller can lock it again afterwards. It calls Unlock on
// the session object, which logind allows for the session's owner without
// polkit. It never falls back to Manager.UnlockSessions: that unlocks every
// session on the machine, other users' included, and the end of the session
// locks only this one again. A session it cannot find or unlock stays as it
// was, and the error says why.
//
// A session that was locked is reported as locked even when unlocking
// fails: an unlock whose reply was lost may have worked all the same, and
// locking a desk that stayed locked changes nothing.
func (b *Bus) UnlockSession(ctx context.Context) (bool, error) {
	session, err := b.displaySession(ctx)
	if err != nil {
		return false, fmt.Errorf("find the session to unlock: %w", err)
	}
	locked, err := b.sessionLocked(ctx, session)
	if err == nil && !locked {
		return false, nil
	}
	// When LockedHint cannot be read, the session counts as locked, so it is
	// locked again afterwards: wrongly locking an unlocked desk is safer than
	// leaving a locked one open.
	if err != nil {
		slog.Debug("LockedHint unreadable; treating the session as locked", "err", err)
	}
	if err := session.CallWithContext(ctx, login1Bus+".Session.Unlock", 0).Err; err != nil {
		return true, fmt.Errorf("unlock session: %w", err)
	}
	return true, nil
}

// LockSession locks the user's graphical session.
func (b *Bus) LockSession(ctx context.Context) error {
	session, err := b.displaySession(ctx)
	if err != nil {
		return err
	}
	if err := session.CallWithContext(ctx, login1Bus+".Session.Lock", 0).Err; err != nil {
		return fmt.Errorf("lock session: %w", err)
	}
	return nil
}

// AutologinService is the PAM service GDM uses for automatic login.
const AutologinService = "gdm-autologin"

// SessionLogin reports how the user's graphical session was opened: its PAM
// service (gdm-password, gdm-autologin, ...) and when it started.
func (b *Bus) SessionLogin(ctx context.Context) (service string, started time.Time, err error) {
	session, err := b.displaySession(ctx)
	if err != nil {
		return "", time.Time{}, err
	}
	v, err := getProperty(ctx, session, login1Bus+".Session", "Service")
	if err != nil {
		return "", time.Time{}, fmt.Errorf("read session service: %w", err)
	}
	if err := v.Store(&service); err != nil {
		return "", time.Time{}, fmt.Errorf("decode session service: %w", err)
	}
	v, err = getProperty(ctx, session, login1Bus+".Session", "Timestamp")
	if err != nil {
		return "", time.Time{}, fmt.Errorf("read session start: %w", err)
	}
	var usec uint64
	if err := v.Store(&usec); err != nil {
		return "", time.Time{}, fmt.Errorf("decode session start: %w", err)
	}
	return service, time.UnixMicro(int64(usec)), nil
}

// SessionLocked reports whether the user's graphical session is locked.
func (b *Bus) SessionLocked(ctx context.Context) (bool, error) {
	session, err := b.displaySession(ctx)
	if err != nil {
		return false, err
	}
	return b.sessionLocked(ctx, session)
}

// displaySession returns the logind object of the user's graphical session.
func (b *Bus) displaySession(ctx context.Context) (dbus.BusObject, error) {
	mgr := b.System.Object(login1Bus, login1Path)
	var userPath dbus.ObjectPath
	if err := mgr.CallWithContext(ctx, login1Bus+".Manager.GetUser", 0, uint32(os.Getuid())).Store(&userPath); err != nil {
		return nil, fmt.Errorf("get login1 user: %w", err)
	}
	v, err := getProperty(ctx, b.System.Object(login1Bus, userPath), login1Bus+".User", "Display")
	if err != nil {
		return nil, fmt.Errorf("get display session: %w", err)
	}
	var display struct {
		ID   string
		Path dbus.ObjectPath
	}
	if err := v.Store(&display); err != nil {
		return nil, fmt.Errorf("decode display session: %w", err)
	}
	if display.Path == "/" || display.Path == "" {
		return nil, errors.New("user has no graphical session")
	}
	return b.System.Object(login1Bus, display.Path), nil
}

func (b *Bus) sessionLocked(ctx context.Context, session dbus.BusObject) (bool, error) {
	v, err := getProperty(ctx, session, login1Bus+".Session", "LockedHint")
	if err != nil {
		return false, fmt.Errorf("read LockedHint: %w", err)
	}
	var locked bool
	if err := v.Store(&locked); err != nil {
		return false, fmt.Errorf("decode LockedHint: %w", err)
	}
	return locked, nil
}

// getProperty reads a property with Properties.Get under ctx. godbus's
// GetProperty takes no context, so a service that stops answering would hold
// the caller past every deadline it set.
func getProperty(ctx context.Context, obj dbus.BusObject, iface, name string) (dbus.Variant, error) {
	var v dbus.Variant
	err := obj.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, iface, name).Store(&v)
	return v, err
}

// InhibitIdle stops the screen from blanking or locking until the returned
// function is called.
func (b *Bus) InhibitIdle(ctx context.Context, app, reason string) (func() error, error) {
	obj := b.Session.Object(screenSaverBus, screenSaverPath)
	var cookie uint32
	if err := obj.CallWithContext(ctx, screenSaverBus+".Inhibit", 0, app, reason).Store(&cookie); err != nil {
		return nil, fmt.Errorf("inhibit screensaver: %w", err)
	}
	return func() error {
		if err := obj.Call(screenSaverBus+".UnInhibit", 0, cookie).Err; err != nil {
			return fmt.Errorf("uninhibit screensaver: %w", err)
		}
		return nil
	}, nil
}

// InhibitSuspend keeps GNOME from suspending the computer when it is idle,
// without keeping the screen on or unlocked: org.gnome.SessionManager's
// inhibitor with only the suspend flag. The returned function releases it;
// it also ends if the bus connection closes.
func (b *Bus) InhibitSuspend(ctx context.Context, app, reason string) (func() error, error) {
	obj := b.Session.Object(sessionManagerBus, sessionManagerPath)
	var cookie uint32
	if err := obj.CallWithContext(ctx, sessionManagerBus+".Inhibit", 0, app, uint32(0), reason, inhibitSuspend).Store(&cookie); err != nil {
		return nil, fmt.Errorf("inhibit suspend: %w", err)
	}
	return func() error {
		if err := obj.Call(sessionManagerBus+".Uninhibit", 0, cookie).Err; err != nil {
			return fmt.Errorf("uninhibit suspend: %w", err)
		}
		return nil
	}, nil
}

const (
	sessionManagerBus  = "org.gnome.SessionManager"
	sessionManagerPath = "/org/gnome/SessionManager"
	// inhibitSuspend is GsmInhibitorFlag's suspend bit.
	inhibitSuspend = uint32(4)
)

// WakeDisplay turns the monitors on and resets the idle timer, so GNOME
// renders frames for the screen cast.
func (b *Bus) WakeDisplay(ctx context.Context) error {
	errSim := b.Session.Object(screenSaverBus, screenSaverPath).CallWithContext(ctx, screenSaverBus+".SimulateUserActivity", 0).Err
	// Properties.Set rather than SetProperty, which takes no context.
	errPower := b.Session.Object(mutterBus, mutterPath).CallWithContext(ctx, "org.freedesktop.DBus.Properties.Set", 0,
		mutterBus, "PowerSaveMode", dbus.MakeVariant(powerSaveOn)).Err
	if errSim != nil && errPower != nil {
		return errors.Join(fmt.Errorf("simulate user activity: %w", errSim), fmt.Errorf("set power save mode: %w", errPower))
	}
	return nil
}

type mutterSpec struct {
	Connector, Vendor, Product, Serial string
}

type mutterMode struct {
	ID              string
	Width, Height   int32
	Refresh         float64
	PreferredScale  float64
	SupportedScales []float64
	Props           map[string]dbus.Variant
}

type mutterMonitor struct {
	Spec  mutterSpec
	Modes []mutterMode
	Props map[string]dbus.Variant
}

type mutterLogical struct {
	X, Y      int32
	Scale     float64
	Transform uint32
	Primary   bool
	Monitors  []mutterSpec
	Props     map[string]dbus.Variant
}

type mutterAssignment struct {
	Connector string
	ModeID    string
	Props     map[string]dbus.Variant
}

type mutterLogicalConfig struct {
	X, Y      int32
	Scale     float64
	Transform uint32
	Primary   bool
	Monitors  []mutterAssignment
}

// DisplayState reads Mutter's current monitor configuration.
func (b *Bus) DisplayState(ctx context.Context) (DisplayState, error) {
	var (
		serial   uint32
		monitors []mutterMonitor
		logical  []mutterLogical
		props    map[string]dbus.Variant
	)
	call := b.Session.Object(mutterBus, mutterPath).CallWithContext(ctx, mutterBus+".GetCurrentState", 0)
	if err := call.Store(&serial, &monitors, &logical, &props); err != nil {
		return DisplayState{}, fmt.Errorf("get monitor state: %w", err)
	}
	st := DisplayState{Serial: serial, LayoutMode: 1}
	if v, ok := props["layout-mode"]; ok {
		if err := v.Store(&st.LayoutMode); err != nil {
			slog.Debug("decode layout-mode", "err", err)
		}
	}
	for _, m := range monitors {
		mon := Monitor{Connector: m.Spec.Connector}
		for _, md := range m.Modes {
			mode := DisplayMode{
				ID: md.ID, Width: md.Width, Height: md.Height, Refresh: md.Refresh,
				PreferredScale: md.PreferredScale, SupportedScales: md.SupportedScales,
			}
			if v, ok := md.Props["is-current"]; ok {
				if err := v.Store(&mode.Current); err != nil {
					slog.Debug("decode is-current", "err", err)
				}
			}
			mon.Modes = append(mon.Modes, mode)
		}
		st.Monitors = append(st.Monitors, mon)
	}
	for _, l := range logical {
		lm := LogicalMonitor{X: l.X, Y: l.Y, Scale: l.Scale, Transform: l.Transform, Primary: l.Primary}
		for _, s := range l.Monitors {
			lm.Connectors = append(lm.Connectors, s.Connector)
		}
		st.Logical = append(st.Logical, lm)
	}
	return st, nil
}

// ApplyLayout applies a monitor configuration temporarily. serial must come
// from the DisplayState the layout was planned from.
func (b *Bus) ApplyLayout(ctx context.Context, serial uint32, layout DisplayLayout) error {
	cfg := make([]mutterLogicalConfig, 0, len(layout.Logical))
	for _, l := range layout.Logical {
		lc := mutterLogicalConfig{X: l.X, Y: l.Y, Scale: l.Scale, Transform: l.Transform, Primary: l.Primary}
		for _, m := range l.Monitors {
			lc.Monitors = append(lc.Monitors, mutterAssignment{Connector: m.Connector, ModeID: m.ModeID, Props: map[string]dbus.Variant{}})
		}
		cfg = append(cfg, lc)
	}
	call := b.Session.Object(mutterBus, mutterPath).CallWithContext(ctx, mutterBus+".ApplyMonitorsConfig", 0,
		serial, applyTemporary, cfg, map[string]dbus.Variant{})
	if call.Err != nil {
		return fmt.Errorf("apply monitor configuration: %w", call.Err)
	}
	return nil
}

// RestoreLayout reapplies a saved layout against the current serial. It
// returns ErrStaleLayout, without applying anything, when the monitors have
// changed since the layout was saved.
func (b *Bus) RestoreLayout(ctx context.Context, layout DisplayLayout) error {
	var err error
	for attempt := range restoreAttempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return errors.Join(err, ctx.Err())
			case <-time.After(restoreRetryDelay):
			}
		}
		st, serr := b.DisplayState(ctx)
		if serr != nil {
			return serr
		}
		if verr := layout.ValidFor(st); verr != nil {
			return verr
		}
		// Only change the monitors when they differ: Mutter returns to the
		// user's saved layout by itself when a virtual monitor goes away.
		if cur, cerr := st.CurrentLayout(); cerr == nil && cur.Equal(layout) {
			return nil
		}
		// Mutter rejects a configuration built on a serial it has replaced,
		// which happens while it still reacts to a monitor change such as a
		// virtual monitor going away; read the state again and retry.
		if err = b.ApplyLayout(ctx, st.Serial, layout); err == nil || !strings.Contains(err.Error(), "stale") {
			return err
		}
	}
	return err
}
