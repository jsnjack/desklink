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

// suspendGrace is how long dlnet.KeepAwakeSuspend waits before it suspends
// the computer: a client that lost its network and reconnects within it
// starts a session, which cancels the suspend.
const suspendGrace = 20 * time.Second

// keepAwake keeps the computer from suspending for a while after a session
// ends, so a job started from the client (a long build, an agent in a
// terminal) keeps running while nobody is connected. The desk still locks
// and the screen still blanks; only idle suspend waits. The next session
// ends it, since a running session keeps the computer awake by itself, and
// so does unpairing the device that asked for it.
type keepAwake struct {
	mu      sync.Mutex
	release func() error
	stopAt  func() bool
	// owner is the device whose session asked for the hold.
	owner string
	// suspendWanted is set while a suspend is scheduled (suspendSoon): what
	// tells whether its device is still paired. gen counts the changes to
	// the hold, so a suspend deferred for a starting session is scheduled
	// again only if nothing changed meanwhile (deferSuspend). deferredOwner
	// and deferredWanted are that deferred suspend's, which unpairing its
	// device ends.
	suspendWanted  func() bool
	gen            uint64
	deferredOwner  string
	deferredWanted func() bool
	// inhibit takes GNOME's suspend inhibitor; suspend suspends the
	// computer; after runs f after d and returns a function that cancels it
	// (time.AfterFunc's Stop).
	inhibit func(context.Context) (func() error, error)
	suspend func(context.Context) error
	after   func(d time.Duration, f func()) func() bool
}

func newKeepAwake(inhibit func(context.Context) (func() error, error)) *keepAwake {
	return &keepAwake{
		inhibit: inhibit,
		after:   func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop },
	}
}

// hold keeps the computer awake for minutes from now, or until the next
// session for dlnet.KeepAwakeUntilReturn, as the device owner asked;
// dlnet.KeepAwakeOff ends any hold. wanted is asked once the inhibitor is
// taken: if the device was unpaired meanwhile, the hold ends at once, since
// forget may have run before there was anything to end.
func (k *keepAwake) hold(ctx context.Context, minutes int, owner string, wanted func() bool) {
	k.stop()
	if minutes == dlnet.KeepAwakeOff {
		return
	}
	if minutes == dlnet.KeepAwakeSuspend {
		k.suspendSoon(owner, wanted)
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
	if !wanted() {
		if err := release(); err != nil {
			slog.Debug("release the keep-awake inhibitor", "err", err)
		}
		return
	}
	k.release, k.owner = release, owner
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
	release, stopAt := k.take()
	k.mu.Unlock()
	k.end(release, stopAt)
}

// forget ends the hold if the device owner asked for it: what an unpaired
// device asked for does not outlast its pairing.
func (k *keepAwake) forget(owner string) {
	k.mu.Lock()
	var release func() error
	var stopAt func() bool
	if owner != "" && (k.owner == owner || k.deferredOwner == owner) {
		release, stopAt = k.take()
	}
	k.mu.Unlock()
	if release != nil {
		slog.Info("no longer keeping the computer awake: the device that asked was unpaired")
	}
	k.end(release, stopAt)
}

// take clears the hold and returns what ends it. The caller holds mu.
func (k *keepAwake) take() (release func() error, stopAt func() bool) {
	release, stopAt = k.release, k.stopAt
	k.release, k.stopAt, k.owner, k.suspendWanted = nil, nil, "", nil
	k.deferredOwner, k.deferredWanted = "", nil
	k.gen++
	return release, stopAt
}

// end cancels the timer and releases the inhibitor taken from a hold.
func (k *keepAwake) end(release func() error, stopAt func() bool) {
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
	return minutes == dlnet.KeepAwakeOff || minutes == dlnet.KeepAwakeUntilReturn || minutes == dlnet.KeepAwakeSuspend ||
		(minutes > 0 && minutes <= dlnet.KeepAwakeMaxMinutes)
}

// suspendSoon suspends the computer after suspendGrace, unless a session
// starts first (stop cancels it) or the device is unpaired meanwhile.
func (k *keepAwake) suspendSoon(owner string, wanted func() bool) {
	if k.suspend == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.scheduleSuspendLocked(owner, wanted)
}

// scheduleSuspendLocked is suspendSoon; the caller holds mu.
func (k *keepAwake) scheduleSuspendLocked(owner string, wanted func() bool) {
	k.gen++
	gen := k.gen
	k.owner, k.suspendWanted = owner, wanted
	k.stopAt = k.after(suspendGrace, func() {
		// A suspend that fires is over: a connection after the computer
		// wakes up must not schedule it again.
		k.mu.Lock()
		current := k.gen == gen
		if current {
			k.take()
		}
		k.mu.Unlock()
		if !current || !wanted() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), relockTimeout)
		defer cancel()
		slog.Info("suspending the computer after the session, as the client asked")
		if err := k.suspend(ctx); err != nil {
			slog.Warn("suspend the computer after the session", "err", err)
		}
	})
	slog.Info("suspending the computer after the session unless a client connects", "in", suspendGrace)
}

// deferSuspend stops a scheduled suspend while an admitted connection starts
// its session: waiting for a replaced session, the hello and unlocking the
// desk can take longer than suspendGrace. It returns what schedules the
// suspend again, with a fresh grace period, for when that session never
// starts; once the session has started (stop), or anything else changed the
// hold, that does nothing. A connection admitted while an earlier one waits
// takes the deferred suspend over, since it replaces that one.
func (k *keepAwake) deferSuspend() (rearm func()) {
	k.mu.Lock()
	owner, wanted := k.owner, k.suspendWanted
	var stopAt func() bool
	switch {
	case wanted != nil:
		_, stopAt = k.take()
	case k.deferredWanted != nil:
		owner, wanted = k.deferredOwner, k.deferredWanted
		k.gen++ // the earlier connection's rearm does nothing now
	default:
		k.mu.Unlock()
		return func() {}
	}
	k.deferredOwner, k.deferredWanted = owner, wanted
	gen := k.gen
	k.mu.Unlock()
	k.end(nil, stopAt)
	slog.Debug("the suspend after the last session waits while a connection starts its session")
	return func() {
		k.mu.Lock()
		defer k.mu.Unlock()
		if k.gen == gen {
			k.scheduleSuspendLocked(owner, wanted)
		}
	}
}
