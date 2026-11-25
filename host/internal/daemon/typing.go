package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"
	"unicode/utf8"

	"desklink/internal/desktop"
	dlnet "desklink/internal/net"
)

const (
	// textQueueSize bounds the texts waiting to be typed; a client that sends
	// more is disconnected, like one that floods the control queue.
	textQueueSize = 64
	// typeTimeout bounds typing one text, so a Mutter that stops answering
	// holds the client's keys back for no longer than keysBehindTextLimit.
	typeTimeout = 10 * time.Second
	// keysBehindTextLimit bounds how long the client's keys wait for text it
	// sent before them. Typing a full message of MaxTypeChars takes well
	// under a second.
	keysBehindTextLimit = time.Second
	// textSettle is how long keys still wait after a text's last keysym:
	// Mutter answers NotifyKeyboardKeysym once it has queued the key for its
	// input thread, and a uinput key written at once could get there first.
	textSettle = 5 * time.Millisecond
)

// textTyper types text as keysyms: *desktop.Capture with a remote desktop
// session, or a fake in tests.
type textTyper interface {
	Type(ctx context.Context, text string) error
}

// textQueue carries the client's type messages from the control reader to
// the session's typist, apart from the other control messages: one of those
// can take seconds (a resize restarts the pipeline), and text waiting behind
// it would come out after keys the client typed later. The keys go over UDP
// and the text over the WebSocket, so keys that arrive while text received
// before them is still to be typed wait for that text (HandleInput).
type textQueue struct {
	texts chan dlnet.Message

	mu sync.Mutex
	// received counts the texts queued so far and typed those typed or
	// dropped; progress is closed, and replaced, whenever typed grows.
	received, typed uint64
	progress        chan struct{}
}

func newTextQueue() *textQueue {
	return &textQueue{texts: make(chan dlnet.Message, textQueueSize), progress: make(chan struct{})}
}

// add queues a type message; false when the queue is full.
func (q *textQueue) add(m dlnet.Message) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	select {
	case q.texts <- m:
	default:
		return false
	}
	q.received++
	return true
}

// done marks one queued text as typed or dropped.
func (q *textQueue) done() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.typed++
	close(q.progress)
	q.progress = make(chan struct{})
}

// mark is how many texts have been received so far, and whether some of
// them are still to be typed: keys that arrive now wait until that many have
// been typed (waitTyped), and no longer for text that comes after them.
func (q *textQueue) mark() (received uint64, pending bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.received, q.typed < q.received
}

// waitTyped waits until the first mark texts have been typed, for at most
// limit or until ctx ends, and reports whether they were.
func (q *textQueue) waitTyped(ctx context.Context, mark uint64, limit time.Duration) bool {
	t := time.NewTimer(limit)
	defer t.Stop()
	for {
		q.mu.Lock()
		typed, progress := q.typed, q.progress
		q.mu.Unlock()
		if typed >= mark {
			return true
		}
		select {
		case <-progress:
		case <-t.C:
			return false
		case <-ctx.Done():
			return false
		}
	}
}

// typeTexts types the client's texts in order until the session ends.
func (s *session) typeTexts(ctx context.Context, q *textQueue) {
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-q.texts:
			if err := s.typeText(ctx, m); err != nil && ctx.Err() == nil {
				s.reportError(ctx, err)
			}
			settle := time.NewTimer(textSettle)
			select {
			case <-settle.C:
			case <-ctx.Done():
				settle.Stop()
			}
			q.done()
		}
	}
}

// typeText types the text of one type message. The text itself is never
// logged: it may be a password.
func (s *session) typeText(ctx context.Context, m dlnet.Message) error {
	var t dlnet.TypeText
	if err := json.Unmarshal(m.Payload, &t); err != nil {
		return fmt.Errorf("decode type: %w", err)
	}
	n := utf8.RuneCountInString(t.Text)
	if n > dlnet.MaxTypeChars {
		return fmt.Errorf("type: %d characters are more than the %d one message may carry", n, dlnet.MaxTypeChars)
	}
	typer, err := s.typerFor(ctx)
	if err != nil {
		return err
	}
	defer s.typedText()
	if typer == nil {
		return desktop.ErrNoKeysyms
	}
	end := s.d.stalls.begin("typing the client's text")
	defer end()
	typeCtx, cancel := context.WithTimeout(ctx, typeTimeout)
	defer cancel()
	if err := typer.Type(typeCtx, t.Text); err != nil {
		return err
	}
	slog.Log(ctx, dlnet.LevelTrace, "typed text", "chars", n)
	return nil
}

// errScreenReplaced refuses text that waited for the client's screen to be
// replaced longer than a screen cast may take to start.
var errScreenReplaced = fmt.Errorf("type: the screen was still being replaced after %v", captureTimeout)

// typerFor returns the cast that types now, nil when it cannot type, and
// marks it as typing until typedText. While a resize or mirror switch
// replaces the capture (holdTyping), text waits for the new cast, for at most
// captureTimeout, instead of failing for want of one.
func (s *session) typerFor(ctx context.Context) (textTyper, error) {
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		typer, replacing := s.claimTyper()
		if replacing == nil {
			return typer, nil
		}
		if timer == nil {
			timer = time.NewTimer(captureTimeout)
		}
		select {
		case <-replacing:
		case <-timer.C:
			return nil, errScreenReplaced
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// claimTyper marks the current typer as typing and returns it, or, while
// the capture is being replaced, what closes once it has been.
func (s *session) claimTyper() (textTyper, <-chan struct{}) {
	s.typerMu.Lock()
	defer s.typerMu.Unlock()
	if s.replacing != nil {
		return nil, s.replacing
	}
	s.typing = true
	return s.typer, nil
}

// typedText ends what claimTyper started.
func (s *session) typedText() {
	s.typerMu.Lock()
	defer s.typerMu.Unlock()
	s.typing = false
	if s.typingDone != nil {
		close(s.typingDone)
		s.typingDone = nil
	}
}

// holdTyping prepares replacing the capture: text that arrives from now on
// waits for the new cast (typerFor), and a text being typed is typed to the
// end first, since stopping its cast would cut it off. Typing one text takes
// at most typeTimeout. releaseTyping lets the text go on to whatever cast the
// session has then.
func (s *session) holdTyping() {
	s.typerMu.Lock()
	s.replacing = make(chan struct{})
	var done chan struct{}
	if s.typing {
		s.typingDone = make(chan struct{})
		done = s.typingDone
	}
	s.typerMu.Unlock()
	if done == nil {
		return
	}
	t := time.NewTimer(typeTimeout + time.Second)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
		slog.Warn("replacing the screen while the client's text is still being typed", "waited", typeTimeout+time.Second)
	}
}

// releaseTyping ends holdTyping.
func (s *session) releaseTyping() {
	s.typerMu.Lock()
	defer s.typerMu.Unlock()
	if s.replacing != nil {
		close(s.replacing)
		s.replacing = nil
	}
}
