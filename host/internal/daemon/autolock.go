package daemon

import (
	"context"
	"log/slog"
	"os/user"
	"time"

	"desklink/internal/desktop"
	"desklink/internal/input"
)

const (
	// autologinLockWindow limits locking to a session that just started, so
	// a daemon restarted later in the day never locks the desk.
	autologinLockWindow = 2 * time.Minute
	// GNOME Shell may not listen for the lock request the moment the session
	// starts, so the request is repeated until the session reports it locked.
	autolockRetry   = 2 * time.Second
	autolockTimeout = 30 * time.Second
)

// lockAfterAutologin locks a session that GDM opened by automatic login a
// moment ago. The tablet unlocks it on connect and locks it again when the
// session ends, so the desk is never left open.
func (d *Daemon) lockAfterAutologin(ctx context.Context) {
	if !d.opts.Config.LockAfterAutologin {
		return
	}
	service, started, err := d.bus.SessionLogin(ctx)
	if err != nil {
		slog.Debug("read how the session started", "err", err)
		return
	}
	if !shouldAutolock(service, started, time.Now()) {
		return
	}
	daemonCtx := ctx
	ctx, cancel := context.WithTimeout(ctx, autolockTimeout)
	defer cancel()
	t := time.NewTicker(autolockRetry)
	defer t.Stop()
	for {
		if locked, err := d.bus.SessionLocked(ctx); err == nil && locked {
			slog.Info("locked the session after automatic login")
			d.showLockScreen(daemonCtx)
			return
		}
		if err := d.bus.LockSession(ctx); err != nil {
			slog.Debug("lock after automatic login", "err", err)
		}
		select {
		case <-ctx.Done():
			slog.Warn("could not lock the session after automatic login", "err", ctx.Err())
			return
		case <-t.C:
		}
	}
}

// warnWithoutAutologin warns when GDM does not log this user in by itself:
// after a reboot, or after the session ended (a crash or a logout),
// desklinkd would then not run until someone logs in at the desk, and
// clients see a computer that refuses them.
func (d *Daemon) warnWithoutAutologin() {
	me, err := user.Current()
	if err != nil {
		slog.Debug("read GDM's login settings", "err", err)
		return
	}
	l, err := desktop.ReadGDMLogin(desktop.GDMConf, me.Username)
	if err != nil {
		slog.Debug("read GDM's login settings", "err", err)
		return
	}
	if msg := autologinWarning(l); msg != "" {
		slog.Warn(msg)
	}
}

// autologinWarning is what desklinkd warns about GDM's login settings at
// startup; empty when GDM logs the user back in whenever the session ends.
func autologinWarning(l desktop.GDMLogin) string {
	switch {
	case l.Timed:
		return ""
	case l.Automatic:
		return "automatic login works at boot only: if the session ends (a crash or a logout) desklinkd starts only " +
			"once someone logs in at the desk; run `desklinkd autologin on` to have GDM log you back in too"
	default:
		return "automatic login is off: after a reboot desklinkd starts only once someone logs in at the desk; " +
			"turn it on with `desklinkd autologin on`"
	}
}

// lockScreenNudges are when, after its lock took, desklinkd nudges the
// pointer. GNOME fades every new lock to black, and gsd-power then turns
// the monitors off a moment later, so a second nudge undoes a blank that
// came after the first.
var lockScreenNudges = []time.Duration{time.Second, 2 * time.Second}

// showLockScreen brings back the lock screen that GNOME blanked as
// desklinkd locked a session GDM had just opened, so that someone at the
// desk sees the lock screen as after Super+L, not a dark screen. It moves
// the pointer one pixel and back, which GNOME counts as user activity, like
// a key press: the shield lifts its black lightbox without unlocking,
// gsd-power turns the monitors on, and GNOME blanks them again only after
// its usual idle time. GNOME 51 offers no call for this: org.gnome.ScreenSaver
// has no SimulateUserActivity, and Mutter's IdleMonitor.ResetIdletime
// answers only with MUTTER_DEBUG_RESET_IDLETIME set.
func (d *Daemon) showLockScreen(ctx context.Context) {
	for _, wait := range lockScreenNudges {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		for _, x := range []int32{1, -1} {
			if err := d.injector.Handle(input.Event{Kind: input.KindRelMove, X: x}); err != nil {
				slog.Debug("nudge the pointer to show the lock screen", "err", err)
				return
			}
		}
	}
}

func shouldAutolock(service string, started, now time.Time) bool {
	return service == desktop.AutologinService && now.Sub(started) < autologinLockWindow
}
