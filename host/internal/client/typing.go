package client

import (
	"errors"
	"log/slog"
	"slices"
	"time"

	dlnet "desklink/internal/net"
)

// textQueueSize bounds the messages of text waiting to go out, as the host
// bounds those waiting to be typed.
const textQueueSize = 64

// textLead is how long input after a text waits once the text is written to
// the WebSocket, as on the tablet (UdpTransport.TEXT_LEAD_MS): enough for a
// TCP segment held back until the previous one is acknowledged over a VPN.
// The host holds keys back only behind text it has received, so keys that
// overtook the text would come out before it.
const textLead = 20 * time.Millisecond

// ErrTextQueueFull means text was typed faster than the link carries it.
var ErrTextQueueFull = errors.New("too much text is waiting to be sent to the host")

// outItem is a text to type or input sent after one.
type outItem struct {
	text   string
	input  []dlnet.InputEvent
	isText bool
}

// TypeText asks the host to type text as keysyms (dlnet.MsgTypeText), in
// messages of at most dlnet.MaxTypeChars characters. It returns at once: one
// goroutine of the session sends the messages in order, so a stalled link
// never blocks the GTK thread, and input sent after the text goes through the
// same goroutine until the text is written and textLead has passed
// (SendInput), so the host types both in the order they were sent. One whose
// SessionInfo has no Keysyms answers with an error, which Events.Notice
// reports.
func (s *Session) TypeText(text string) error {
	parts := splitText(text, dlnet.MaxTypeChars)
	if len(parts) == 0 {
		return nil
	}
	s.outMu.Lock()
	if s.outTexts+len(parts) > textQueueSize {
		s.outMu.Unlock()
		return ErrTextQueueFull
	}
	for _, part := range parts {
		s.out = append(s.out, outItem{text: part, isText: true})
	}
	s.outTexts += len(parts)
	s.outBusy = true
	s.outMu.Unlock()
	s.wakeOut()
	return nil
}

// queueAfterText queues input behind text still to be sent, or still within
// its lead, and reports whether it did; otherwise the caller sends it at once.
func (s *Session) queueAfterText(events []dlnet.InputEvent) bool {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	if !s.outBusy {
		return false
	}
	s.out = append(s.out, outItem{input: slices.Clone(events)})
	s.wakeOut()
	return true
}

func (s *Session) wakeOut() {
	select {
	case s.outWake <- struct{}{}:
	default:
	}
}

// sendOut sends the queued text and the input after it, in order, until the
// session ends. After each text, the input behind it waits textLead. A
// failed write means the connection failed, which the read loop reports.
func (s *Session) sendOut() {
	for {
		s.outMu.Lock()
		if len(s.out) == 0 {
			s.outBusy = false
			s.outMu.Unlock()
			select {
			case <-s.ctx.Done():
				return
			case <-s.outWake:
			}
			continue
		}
		item := s.out[0]
		s.out = s.out[1:]
		if item.isText {
			s.outTexts--
		}
		s.outMu.Unlock()
		if !item.isText {
			s.withTransport(func(t *transport) { t.sendInput(item.input) })
			continue
		}
		if err := s.send(dlnet.MsgTypeText, dlnet.TypeText{Text: item.text}); err != nil {
			slog.Debug("send text to type", "err", err)
		}
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(textLead):
		}
	}
}

// splitText cuts text into pieces of at most limit characters, never inside
// a character.
func splitText(text string, limit int) []string {
	var parts []string
	n, start := 0, 0
	for i := range text {
		if n == limit {
			parts = append(parts, text[start:i])
			n, start = 0, i
		}
		n++
	}
	if start < len(text) {
		parts = append(parts, text[start:])
	}
	return parts
}
