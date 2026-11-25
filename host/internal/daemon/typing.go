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
// and the text over the WebSocket, so from the moment a text is read until it
// has been typed, the client's keys wait for it (waitTyped).
type textQueue struct {
	texts chan dlnet.Message

	mu      sync.Mutex
	pending int
	// typed is closed while no text is pending.
	typed chan struct{}
}

func newTextQueue() *textQueue {
	q := &textQueue{texts: make(chan dlnet.Message, textQueueSize), typed: make(chan struct{})}
	close(q.typed)
	return q
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
	if q.pending == 0 {
		q.typed = make(chan struct{})
	}
	q.pending++
	return true
}

// done marks one queued text as typed or dropped.
func (q *textQueue) done() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pending--
	if q.pending == 0 {
		close(q.typed)
	}
}

// waitTyped waits until every text queued so far has been typed, for at most
// limit or until ctx ends, and reports whether they were.
func (q *textQueue) waitTyped(ctx context.Context, limit time.Duration) bool {
	q.mu.Lock()
	typed := q.typed
	q.mu.Unlock()
	select {
	case <-typed:
		return true
	default:
	}
	t := time.NewTimer(limit)
	defer t.Stop()
	select {
	case <-typed:
		return true
	case <-t.C:
		return false
	case <-ctx.Done():
		return false
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
	typer := s.currentTyper()
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

// keysWaitForText holds the client's keys back until the text it sent
// before them has been typed (textQueue).
func (s *session) keysWaitForText(events []dlnet.InputEvent) {
	if s.texts == nil {
		return
	}
	for _, e := range events {
		if e.Kind != dlnet.InputKey {
			continue
		}
		if !s.texts.waitTyped(s.ctx, keysBehindTextLimit) && s.ctx.Err() == nil {
			slog.Debug("keys go ahead of text still being typed", "waited", keysBehindTextLimit)
		}
		return
	}
}
