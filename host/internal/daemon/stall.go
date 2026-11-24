package daemon

import (
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// stallLimit is how long a tracked operation, such as a session's cleanup
// or a pipeline's teardown, may run before the watchdog stops telling
// systemd desklinkd is alive. systemd then kills desklinkd, with a SIGABRT
// that makes Go print every goroutine's stack to the journal, and starts it
// again; the dead daemon's screen cast closes with its D-Bus connection, and
// Mutter turns the desk's monitors back on.
const stallLimit = 30 * time.Second

// stallTracker notes the operations that must end in bounded time.
type stallTracker struct {
	mu   sync.Mutex
	next int
	ops  map[int]trackedOp
}

type trackedOp struct {
	name  string
	since time.Time
}

// begin notes that the operation name starts now; call end once it ends.
func (t *stallTracker) begin(name string) (end func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ops == nil {
		t.ops = map[int]trackedOp{}
	}
	id := t.next
	t.next++
	t.ops[id] = trackedOp{name: name, since: time.Now()}
	return func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		delete(t.ops, id)
	}
}

// check returns an error naming the oldest operation that has run longer
// than limit at now, or nil.
func (t *stallTracker) check(now time.Time, limit time.Duration) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	var oldest *trackedOp
	for _, op := range t.ops {
		if now.Sub(op.since) > limit && (oldest == nil || op.since.Before(oldest.since)) {
			oldest = &op
		}
	}
	if oldest == nil {
		return nil
	}
	return fmt.Errorf("%s has not ended after %s", oldest.name, now.Sub(oldest.since).Round(time.Second))
}

// finishWithin runs stop and waits for it at most wait. A stop that takes
// longer goes on in the background, still tracked as name, and finishWithin
// returns false so the caller can go on: a GStreamer or PipeWire teardown
// that hangs must not keep the desk from getting its monitors and lock back.
func (t *stallTracker) finishWithin(name string, wait time.Duration, stop func()) bool {
	end := t.begin(name)
	done := make(chan struct{})
	go func() {
		defer end()
		defer close(done)
		stop()
	}()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		slog.Warn(name+" did not end in time; going on without it", "waited", wait)
		return false
	}
}
