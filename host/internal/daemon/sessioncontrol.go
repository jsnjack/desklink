package daemon

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"desklink/internal/config"
)

// relockTimeout bounds locking the desk when a session ends.
const relockTimeout = 5 * time.Second

// errRevoked refuses a session whose device was unpaired after it
// authenticated.
var errRevoked = errors.New("this device is no longer paired")

type activeSession struct {
	// peerID is the device the session belongs to.
	peerID string
	cancel context.CancelFunc
	// done closes once the session has cleaned up and unregistered.
	done chan struct{}
	// revoked is set, under sessionControl.mu, when the device is unpaired
	// while the session is registered: nothing it asked for outlasts it.
	revoked bool
	// replacedBy is the device whose connection replaced this session, set
	// under sessionControl.mu by admit; the zero Peer while none did.
	replacedBy config.Peer
}

// sessionControl decides which session controls the desk. Admission,
// replacement, revocation and the duty to lock the desk again all happen
// under one mutex, so a device unpaired between authenticating and
// registering cannot become active, and a desk a session unlocked is always
// locked again unless another admitted session takes that duty over.
type sessionControl struct {
	mu     sync.Mutex
	active *activeSession
	// relockOwed is set when a session that unlocked a locked desk ended
	// while another session was in control: that one owes the relock, and the
	// last session to end pays it if nobody took it.
	relockOwed bool
	// paired reports whether the device is still paired with this key.
	paired func(peerID string, key []byte) bool
	// lock locks the desk.
	lock func(context.Context) error
}

// admit registers a session for peer as the one in control, if the device is
// still paired, and returns the session it replaces. The caller cancels that
// one and waits for its done before touching the desk.
func (c *sessionControl) admit(peer config.Peer, cancel context.CancelFunc) (mine, prev *activeSession, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.paired(peer.ID, peer.Key) {
		return nil, nil, errRevoked
	}
	mine = &activeSession{peerID: peer.ID, cancel: cancel, done: make(chan struct{})}
	prev, c.active = c.active, mine
	if prev != nil {
		prev.replacedBy = peer
	}
	return mine, prev, nil
}

// takenOverBy returns the other device that took session mine over, if one
// did. A reconnect of the same device, after a network change say, is no
// takeover.
func (c *sessionControl) takenOverBy(mine *activeSession) (config.Peer, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	by := mine.replacedBy
	return by, by.ID != "" && by.ID != mine.peerID
}

// current is the session in control, or nil.
func (c *sessionControl) current() *activeSession {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active
}

// endsInControl reports whether session mine, as it ends, is still the one
// in control and its device was not unpaired meanwhile. Only then may a
// setting it chose, such as keeping the computer awake, outlast it.
func (c *sessionControl) endsInControl(mine *activeSession) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active == mine && !mine.revoked
}

// stillPaired reports whether session mine's device was not unpaired while
// the session was registered.
func (c *sessionControl) stillPaired(mine *activeSession) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !mine.revoked
}

// takeRelock gives the session in control the relock a replaced session
// left it: the desk it finds unlocked was locked before.
func (c *sessionControl) takeRelock() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	owed := c.relockOwed
	c.relockOwed = false
	return owed
}

// leave settles the relock when session mine ends having unlocked a locked
// desk: a session now in control takes the duty over, so no lock screen
// flashes between the two; otherwise the desk is locked now.
func (c *sessionControl) leave(ctx context.Context, mine *activeSession, unlocked bool) {
	if !unlocked {
		return
	}
	c.mu.Lock()
	handOver := c.active != nil && c.active != mine
	if handOver {
		c.relockOwed = true
	}
	c.mu.Unlock()
	if !handOver {
		c.lockDesk(ctx)
	}
}

// finish unregisters session mine once it has ended. If it was the last one
// and a replaced session's relock was never taken, because mine failed before
// it got that far, it locks the desk now. mine stays registered until that
// lock is done, so a connection that comes meanwhile replaces mine and waits
// for it instead of unlocking the desk under the lock, and a stopping daemon
// waits for the lock too. done closes last, so whoever waits for mine finds
// the desk settled.
func (c *sessionControl) finish(ctx context.Context, mine *activeSession) {
	c.mu.Lock()
	owed := false
	if c.active == mine {
		owed, c.relockOwed = c.relockOwed, false
	}
	c.mu.Unlock()
	if owed {
		c.lockDesk(ctx)
	}
	c.mu.Lock()
	if c.active == mine {
		c.active = nil
	}
	c.mu.Unlock()
	close(mine.done)
}

// revoke runs remove, which unpairs the device, under the same mutex as
// admit, and ends the device's session if it is in control, marking it
// revoked. A session that authenticated earlier but has not registered yet
// is refused by admit. The session ends even when remove fails part-way.
func (c *sessionControl) revoke(peerID string, remove func() error) (ended bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	err = remove()
	if c.active != nil && c.active.peerID == peerID {
		c.active.revoked = true
		c.active.cancel()
		ended = true
	}
	return ended, err
}

func (c *sessionControl) lockDesk(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), relockTimeout)
	defer cancel()
	if err := c.lock(ctx); err != nil {
		slog.Warn("lock the session again", "err", err)
	}
}
