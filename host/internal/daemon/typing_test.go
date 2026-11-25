package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"desklink/internal/desktop"
	dlnet "desklink/internal/net"
)

// fakeTyper records the texts it types; each waits for release when that is
// set.
type fakeTyper struct {
	mu      sync.Mutex
	texts   []string
	release chan struct{}
}

func (f *fakeTyper) Type(ctx context.Context, text string) error {
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.texts = append(f.texts, text)
	return nil
}

func (f *fakeTyper) typed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.texts...)
}

func typeMessage(t *testing.T, text string) dlnet.Message {
	t.Helper()
	raw, err := json.Marshal(dlnet.TypeText{Text: text})
	if err != nil {
		t.Fatal(err)
	}
	return dlnet.Message{Type: dlnet.MsgTypeText, Payload: raw}
}

// typingFixture is a session whose texts typer types, its typist running.
func typingFixture(t *testing.T, typer *fakeTyper) sessionFixture {
	t.Helper()
	f := newSessionFixture(t)
	f.s.typer = typer
	f.s.texts = newTextQueue()
	go f.s.typeTexts(f.s.ctx, f.s.texts)
	return f
}

// returnsAtOnce fails the test when fn blocks: HandleInput runs on the UDP
// goroutine, which every datagram shares.
func returnsAtOnce(t *testing.T, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("HandleInput blocked the UDP goroutine")
	}
}

// waitInjected waits until the injector has seen want, in that order.
func waitInjected(t *testing.T, in *fakeInjector, want ...string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Equal(in.injected(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("injected %q, want %q", in.injected(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func buttonDown(code uint16) []dlnet.InputEvent {
	return []dlnet.InputEvent{{Kind: dlnet.InputMouseButton, Flags: 1, Code: code}}
}

// TestKeysWaitForTheTextSentBeforeThem: the text goes over the WebSocket and
// the keys over UDP, so a key that arrives while the text before it is still
// being typed waits for it, and the desktop gets both in the order the
// client sent them. The key waits in a worker, not on the UDP goroutine.
func TestKeysWaitForTheTextSentBeforeThem(t *testing.T) {
	typer := &fakeTyper{release: make(chan struct{})}
	f := typingFixture(t, typer)
	if !f.s.texts.add(typeMessage(t, "é")) {
		t.Fatal("the text queue refused a text")
	}
	returnsAtOnce(t, func() { f.s.HandleInput(1, keyDown(keyCtrl)) })
	time.Sleep(50 * time.Millisecond)
	if f.in.held(keyCtrl) {
		t.Fatal("a key reached the desktop before the text sent before it")
	}
	close(typer.release)
	waitInjected(t, f.in, "key 29 down")
	if got := typer.typed(); len(got) != 1 || got[0] != "é" {
		t.Errorf("typed %q, want [é]", got)
	}
}

// TestInputAfterAWaitingKeyKeepsItsOrder: once a key waits for text, every
// input after it waits behind it, keys or not, until the worker has caught
// up; then input is injected at once again.
func TestInputAfterAWaitingKeyKeepsItsOrder(t *testing.T) {
	typer := &fakeTyper{release: make(chan struct{})}
	f := typingFixture(t, typer)
	f.s.texts.add(typeMessage(t, "ñ"))
	returnsAtOnce(t, func() {
		f.s.HandleInput(1, keyDown(keyCtrl))
		f.s.HandleInput(2, buttonDown(1))
	})
	time.Sleep(50 * time.Millisecond)
	if got := f.in.injected(); len(got) != 0 {
		t.Fatalf("injected %q while the key before it waited for text", got)
	}
	close(typer.release)
	waitInjected(t, f.in, "key 29 down", "button 1 down")
	f.s.inputWorkers.Wait()
	f.s.HandleInput(3, buttonDown(2))
	if got := f.in.injected(); len(got) != 3 {
		t.Errorf("injected %q: input after the worker caught up did not go at once", got)
	}
}

// TestKeysDoNotWaitForTextSentAfterThem: a key waits only for text received
// before it, never for text the client sent later.
func TestKeysDoNotWaitForTextSentAfterThem(t *testing.T) {
	first := &fakeTyper{release: make(chan struct{})}
	f := typingFixture(t, first)
	f.s.texts.add(typeMessage(t, "ä"))
	f.s.HandleInput(1, keyDown(keyCtrl))
	// More text arrives while the key waits; typing the first releases it.
	f.s.texts.add(typeMessage(t, "ö"))
	first.release <- struct{}{}
	waitInjected(t, f.in, "key 29 down")
	close(first.release)
}

// TestInputWithoutKeysDoesNotWaitForText: only keys keep their order with
// text; the pointer and the pen go on while text is typed.
func TestInputWithoutKeysDoesNotWaitForText(t *testing.T) {
	typer := &fakeTyper{release: make(chan struct{})}
	t.Cleanup(func() { close(typer.release) })
	f := typingFixture(t, typer)
	f.s.texts.add(typeMessage(t, "ü"))
	returnsAtOnce(t, func() { f.s.HandleInput(1, buttonDown(1)) })
	if got := f.in.injected(); !slices.Equal(got, []string{"button 1 down"}) {
		t.Errorf("injected %q, want the button at once", got)
	}
}

// TestTextsAreTypedInOrder: several messages, as clients split long text,
// come out in the order they were sent.
func TestTextsAreTypedInOrder(t *testing.T) {
	typer := &fakeTyper{}
	f := typingFixture(t, typer)
	want := []string{"Grüße, ", "Jürgen ", "😀"}
	for _, text := range want {
		f.s.texts.add(typeMessage(t, text))
	}
	if mark, _ := f.s.texts.mark(); !f.s.texts.waitTyped(f.s.ctx, mark, 5*time.Second) {
		t.Fatal("the texts were not typed")
	}
	if got := typer.typed(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("typed %q, want %q", got, want)
	}
}

func TestTypeTextRefuses(t *testing.T) {
	cases := []struct {
		name    string
		typer   bool
		msg     dlnet.Message
		wantErr string
	}{
		{"text longer than one message may carry", true, typeMessage(t, strings.Repeat("ä", dlnet.MaxTypeChars+1)), "more than the 1000"},
		{"a payload that is not text", true, dlnet.Message{Type: dlnet.MsgTypeText, Payload: json.RawMessage(`{"text":5}`)}, "decode type"},
		{"text for a cast without a remote desktop session", false, typeMessage(t, "é"), desktop.ErrNoKeysyms.Error()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newSessionFixture(t)
			typer := &fakeTyper{}
			if c.typer {
				f.s.typer = typer
			}
			err := f.s.typeText(f.s.ctx, c.msg)
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("typeText = %v, want an error containing %q", err, c.wantErr)
			}
			if len(typer.typed()) != 0 {
				t.Errorf("typed %q despite the error", typer.typed())
			}
		})
	}
	t.Run("a full message is typed", func(t *testing.T) {
		f := newSessionFixture(t)
		typer := &fakeTyper{}
		f.s.typer = typer
		if err := f.s.typeText(f.s.ctx, typeMessage(t, strings.Repeat("ä", dlnet.MaxTypeChars))); err != nil {
			t.Fatal(err)
		}
	})
}

// TestTextHasAQueueOfItsOwn: type messages bypass the control queue, so text
// cannot wait behind a slow control message while later keys go ahead; a
// client that floods the text queue is disconnected like one that floods the
// control queue.
func TestTextHasAQueueOfItsOwn(t *testing.T) {
	logs := recordLogs(t)
	d, prev, ws := waitingConnection(t)
	for range textQueueSize + 1 {
		sendControl(t, ws, dlnet.MsgTypeText)
	}
	logs.waitLog(t, "closing a control connection that sent more text than its session typed")
	if logs.has("closing a control connection that sent more messages than its session took") {
		t.Error("text filled the control queue")
	}
	startsNoSession(t, logs, d, prev)
}

// TestKeysStopWaitingWhenTheSessionEnds: cleanup cancels the session first,
// and a key waiting for text is then dropped at once.
func TestKeysStopWaitingWhenTheSessionEnds(t *testing.T) {
	typer := &fakeTyper{release: make(chan struct{})}
	t.Cleanup(func() { close(typer.release) })
	f := typingFixture(t, typer)
	f.s.texts.add(typeMessage(t, "ß"))
	f.s.HandleInput(1, keyDown(keyCtrl))
	time.Sleep(20 * time.Millisecond)
	f.s.cancel(errors.New("the client left"))
	done := make(chan struct{})
	go func() {
		f.s.inputWorkers.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(keysBehindTextLimit / 2):
		t.Fatal("a key kept waiting for text after the session ended")
	}
	if f.in.held(keyCtrl) {
		t.Error("an ended session's key was injected")
	}
}

// TestCleanupLiftsKeysOnlyOnceTheInputWorkerStopped: a key the worker
// injects after cleanup lifted every held key would stay down.
func TestCleanupLiftsKeysOnlyOnceTheInputWorkerStopped(t *testing.T) {
	typer := &fakeTyper{release: make(chan struct{})}
	f := typingFixture(t, typer)
	f.s.texts.add(typeMessage(t, "ç"))
	f.s.HandleInput(1, keyDown(keyCtrl))
	// The text is typed, and the worker is injecting the key when the
	// session ends.
	f.in.mu.Lock()
	f.in.gate, f.in.entered = make(chan struct{}), make(chan struct{}, 1)
	gate := f.in.gate
	f.in.mu.Unlock()
	close(typer.release)
	<-f.in.entered
	cleaned := make(chan struct{})
	go func() {
		f.s.cleanup()
		close(cleaned)
	}()
	time.Sleep(50 * time.Millisecond)
	f.in.mu.Lock()
	released := f.in.released
	f.in.mu.Unlock()
	if released != 0 {
		t.Fatal("cleanup lifted held keys while the input worker was injecting one")
	}
	close(gate)
	<-cleaned
	if f.in.held(keyCtrl) {
		t.Error("the key the worker injected during cleanup is still down")
	}
}

// TestTextWaitsWhileTheScreenIsReplaced: text that comes during a resize or
// mirror switch is typed by the new cast once it runs, instead of failing
// for want of one.
func TestTextWaitsWhileTheScreenIsReplaced(t *testing.T) {
	f := newSessionFixture(t)
	old, replacement := &fakeTyper{}, &fakeTyper{}
	f.s.typer = old
	f.s.holdTyping()
	typed := make(chan error, 1)
	go func() { typed <- f.s.typeText(f.s.ctx, typeMessage(t, "ø")) }()
	select {
	case err := <-typed:
		t.Fatalf("the text did not wait for the new screen: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	f.s.typerMu.Lock()
	f.s.typer = replacement
	f.s.typerMu.Unlock()
	f.s.releaseTyping()
	if err := <-typed; err != nil {
		t.Fatal(err)
	}
	if len(old.typed()) != 0 || !slices.Equal(replacement.typed(), []string{"ø"}) {
		t.Errorf("the old cast typed %q and the new one %q, want only the new one", old.typed(), replacement.typed())
	}
}

// TestTheScreenWaitsForTextBeingTyped: a resize or mirror switch does not
// stop the cast under a text it is typing.
func TestTheScreenWaitsForTextBeingTyped(t *testing.T) {
	f := newSessionFixture(t)
	typer := &fakeTyper{release: make(chan struct{})}
	f.s.typer = typer
	typed := make(chan error, 1)
	go func() { typed <- f.s.typeText(f.s.ctx, typeMessage(t, "å")) }()
	waitFor(t, func() bool { f.s.typerMu.Lock(); defer f.s.typerMu.Unlock(); return f.s.typing })
	held := make(chan struct{})
	go func() {
		f.s.holdTyping()
		close(held)
	}()
	select {
	case <-held:
		t.Fatal("the screen was replaced under a text being typed")
	case <-time.After(50 * time.Millisecond):
	}
	close(typer.release)
	<-held
	if err := <-typed; err != nil {
		t.Fatal(err)
	}
	f.s.releaseTyping()
}
