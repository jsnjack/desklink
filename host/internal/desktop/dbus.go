// Package desktop controls the GNOME session for a remote session: screen
// capture through the ScreenCast portal, unlocking, idle inhibition, monitor
// modes through Mutter, the Wayland clipboard and application launching.
package desktop

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/godbus/dbus/v5"
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
// polkit; Manager.UnlockSessions needs polkit approval that a user service
// lacks, so it is only the fallback.
func (b *Bus) UnlockSession(ctx context.Context) (bool, error) {
	session, err := b.displaySession(ctx)
	if err == nil {
		locked, lerr := b.sessionLocked(session)
		if lerr == nil && !locked {
			return false, nil
		}
		if err = session.CallWithContext(ctx, login1Bus+".Session.Unlock", 0).Err; err == nil {
			return true, nil
		}
	}
	slog.Debug("unlock own session failed, trying UnlockSessions", "err", err)
	mgr := b.System.Object(login1Bus, login1Path)
	if ferr := mgr.CallWithContext(ctx, login1Bus+".Manager.UnlockSessions", 0).Err; ferr != nil {
		return false, errors.Join(fmt.Errorf("unlock session: %w", err), fmt.Errorf("unlock all sessions: %w", ferr))
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

// displaySession returns the logind object of the user's graphical session.
func (b *Bus) displaySession(ctx context.Context) (dbus.BusObject, error) {
	mgr := b.System.Object(login1Bus, login1Path)
	var userPath dbus.ObjectPath
	if err := mgr.CallWithContext(ctx, login1Bus+".Manager.GetUser", 0, uint32(os.Getuid())).Store(&userPath); err != nil {
		return nil, fmt.Errorf("get login1 user: %w", err)
	}
	v, err := b.System.Object(login1Bus, userPath).GetProperty(login1Bus + ".User.Display")
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

func (b *Bus) sessionLocked(session dbus.BusObject) (bool, error) {
	v, err := session.GetProperty(login1Bus + ".Session.LockedHint")
	if err != nil {
		return false, fmt.Errorf("read LockedHint: %w", err)
	}
	var locked bool
	if err := v.Store(&locked); err != nil {
		return false, fmt.Errorf("decode LockedHint: %w", err)
	}
	return locked, nil
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

// WakeDisplay turns the monitors on and resets the idle timer, so GNOME
// renders frames for the screen cast.
func (b *Bus) WakeDisplay(ctx context.Context) error {
	errSim := b.Session.Object(screenSaverBus, screenSaverPath).CallWithContext(ctx, screenSaverBus+".SimulateUserActivity", 0).Err
	errPower := b.Session.Object(mutterBus, mutterPath).SetProperty(mutterBus+".PowerSaveMode", dbus.MakeVariant(powerSaveOn))
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
	st, err := b.DisplayState(ctx)
	if err != nil {
		return err
	}
	if err := layout.ValidFor(st); err != nil {
		return err
	}
	return b.ApplyLayout(ctx, st.Serial, layout)
}
