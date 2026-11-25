package daemon

import (
	"context"
	"encoding/json"
	"errors"
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

// TestKeysWaitForTheTextSentBeforeThem: the text goes over the WebSocket and
// the keys over UDP, so a key that arrives while the text before it is still
// being typed waits for it, and the desktop gets both in the order the
// client sent them.
func TestKeysWaitForTheTextSentBeforeThem(t *testing.T) {
	typer := &fakeTyper{release: make(chan struct{})}
	f := typingFixture(t, typer)
	if !f.s.texts.add(typeMessage(t, "é")) {
		t.Fatal("the text queue refused a text")
	}
	injected := make(chan struct{})
	go func() {
		f.s.HandleInput(1, keyDown(keyCtrl))
		close(injected)
	}()
	select {
	case <-injected:
		t.Fatal("a key went ahead of the text sent before it")
	case <-time.After(50 * time.Millisecond):
	}
	if f.in.held(keyCtrl) {
		t.Fatal("a key reached the desktop before the text sent before it")
	}
	close(typer.release)
	select {
	case <-injected:
	case <-time.After(5 * time.Second):
		t.Fatal("the key never followed the typed text")
	}
	if !f.in.held(keyCtrl) {
		t.Error("the key was not injected after the text")
	}
	if got := typer.typed(); len(got) != 1 || got[0] != "é" {
		t.Errorf("typed %q, want [é]", got)
	}
}

// TestInputWithoutKeysDoesNotWaitForText: only keys keep their order with
// text; the pointer and the pen go on while text is typed.
func TestInputWithoutKeysDoesNotWaitForText(t *testing.T) {
	typer := &fakeTyper{release: make(chan struct{})}
	t.Cleanup(func() { close(typer.release) })
	f := typingFixture(t, typer)
	f.s.texts.add(typeMessage(t, "ü"))
	done := make(chan struct{})
	go func() {
		f.s.HandleInput(1, []dlnet.InputEvent{{Kind: dlnet.InputMouseButton, Flags: 1, Code: 1}})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a mouse button waited for text")
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
	if !f.s.texts.waitTyped(f.s.ctx, 5*time.Second) {
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
// and a key waiting for text is then dropped at once instead of holding the
// media session's close.
func TestKeysStopWaitingWhenTheSessionEnds(t *testing.T) {
	typer := &fakeTyper{release: make(chan struct{})}
	t.Cleanup(func() { close(typer.release) })
	f := typingFixture(t, typer)
	f.s.texts.add(typeMessage(t, "ß"))
	done := make(chan struct{})
	go func() {
		f.s.HandleInput(1, keyDown(keyCtrl))
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	f.s.cancel(errors.New("the client left"))
	select {
	case <-done:
	case <-time.After(keysBehindTextLimit / 2):
		t.Fatal("a key kept waiting for text after the session ended")
	}
	if f.in.held(keyCtrl) {
		t.Error("an ended session's key was injected")
	}
}
