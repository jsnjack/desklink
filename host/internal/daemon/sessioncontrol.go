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
	return mine, prev, nil
}

// current is the session in control, or nil.
func (c *sessionControl) current() *activeSession {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active
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
// it got that far, it locks the desk now. done closes last, so whoever waits
// for mine finds the desk settled.
func (c *sessionControl) finish(ctx context.Context, mine *activeSession) {
	c.mu.Lock()
	owed := false
	if c.active == mine {
		c.active = nil
		owed, c.relockOwed = c.relockOwed, false
	}
	c.mu.Unlock()
	if owed {
		c.lockDesk(ctx)
	}
	close(mine.done)
}

// revoke runs remove, which unpairs the device, under the same mutex as
// admit, and ends the device's session if it is in control. A session that
// authenticated earlier but has not registered yet is refused by admit. The
// session ends even when remove fails part-way.
func (c *sessionControl) revoke(peerID string, remove func() error) (ended bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	err = remove()
	if c.active != nil && c.active.peerID == peerID {
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
