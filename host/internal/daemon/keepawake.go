package daemon

import (
	"context"
	"log/slog"
	"sync"
	"time"

	dlnet "desklink/internal/net"
)

// keepAwakeReason is what GNOME shows for the suspend inhibitor.
const keepAwakeReason = "Keeping a remote job running after the session"

// keepAwake keeps the computer from suspending for a while after a session
// ends, so a job started from the client (a long build, an agent in a
// terminal) keeps running while nobody is connected. The desk still locks
// and the screen still blanks; only idle suspend waits. The next session
// ends it, since a running session keeps the computer awake by itself.
type keepAwake struct {
	mu      sync.Mutex
	release func() error
	stopAt  func() bool
	// inhibit takes GNOME's suspend inhibitor; after runs f after d and
	// returns a function that cancels it (time.AfterFunc's Stop).
	inhibit func(context.Context) (func() error, error)
	after   func(d time.Duration, f func()) func() bool
}

func newKeepAwake(inhibit func(context.Context) (func() error, error)) *keepAwake {
	return &keepAwake{
		inhibit: inhibit,
		after:   func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop },
	}
}

// hold keeps the computer awake for minutes from now, or until the next
// session for dlnet.KeepAwakeUntilReturn; dlnet.KeepAwakeOff ends any hold.
func (k *keepAwake) hold(ctx context.Context, minutes int) {
	k.stop()
	if minutes == dlnet.KeepAwakeOff {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), relockTimeout)
	defer cancel()
	release, err := k.inhibit(ctx)
	if err != nil {
		slog.Warn("keep the computer awake after the session", "err", err)
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.release = release
	if minutes > 0 {
		k.stopAt = k.after(time.Duration(minutes)*time.Minute, k.stop)
		slog.Info("keeping the computer awake after the session", "minutes", minutes)
	} else {
		slog.Info("keeping the computer awake until the next session")
	}
}

// stop lets the computer suspend again.
func (k *keepAwake) stop() {
	k.mu.Lock()
	release, stopAt := k.release, k.stopAt
	k.release, k.stopAt = nil, nil
	k.mu.Unlock()
	if stopAt != nil {
		stopAt()
	}
	if release != nil {
		if err := release(); err != nil {
			slog.Debug("release the keep-awake inhibitor", "err", err)
		}
	}
}

// validKeepAwake reports whether a client's keep-awake value is allowed.
func validKeepAwake(minutes int) bool {
	return minutes == dlnet.KeepAwakeOff || minutes == dlnet.KeepAwakeUntilReturn ||
		(minutes > 0 && minutes <= dlnet.KeepAwakeMaxMinutes)
}
