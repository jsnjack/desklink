package daemon

import (
	"context"
	"log/slog"
	"os"
	"time"

	"desklink/internal/desktop"
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
	ctx, cancel := context.WithTimeout(ctx, autolockTimeout)
	defer cancel()
	t := time.NewTicker(autolockRetry)
	defer t.Stop()
	for {
		if locked, err := d.bus.SessionLocked(ctx); err == nil && locked {
			slog.Info("locked the session after automatic login")
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

// warnWithoutAutologin warns when GDM does not log this user in at boot:
// after a reboot or a wake from power-off desklinkd would then not run until
// someone logs in at the desk, and clients see a computer that refuses them.
func (d *Daemon) warnWithoutAutologin(ctx context.Context) {
	user, err := desktop.FindAccountsUser(ctx, d.bus.System, os.Getuid())
	if err != nil {
		slog.Debug("read automatic login", "err", err)
		return
	}
	on, err := user.AutomaticLogin()
	if err != nil {
		slog.Debug("read automatic login", "err", err)
		return
	}
	if !on {
		slog.Warn("automatic login is off: after a reboot desklinkd starts only once someone logs in at the desk; " +
			"turn it on with `desklinkd autologin on`")
	}
}

func shouldAutolock(service string, started, now time.Time) bool {
	return service == desktop.AutologinService && now.Sub(started) < autologinLockWindow
}
