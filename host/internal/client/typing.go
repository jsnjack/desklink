package client

import (
	"errors"
	"log/slog"

	dlnet "desklink/internal/net"
)

// textQueueSize bounds the messages of text waiting to go out, as the host
// bounds those waiting to be typed.
const textQueueSize = 64

// ErrTextQueueFull means text was typed faster than the link carries it.
var ErrTextQueueFull = errors.New("too much text is waiting to be sent to the host")

// TypeText asks the host to type text as keysyms (dlnet.MsgTypeText), in
// messages of at most dlnet.MaxTypeChars characters. It returns at once: one
// goroutine of the session sends the messages in order, so a stalled link
// never blocks the GTK thread. The host types them in order with the keys
// sent before and after; one whose SessionInfo has no Keysyms answers with an
// error, which Events.Notice reports.
func (s *Session) TypeText(text string) error {
	for _, part := range splitText(text, dlnet.MaxTypeChars) {
		select {
		case s.texts <- part:
		default:
			return ErrTextQueueFull
		}
	}
	return nil
}

// sendTexts sends the queued text until the session ends. A failed write
// means the connection failed, which the read loop reports.
func (s *Session) sendTexts() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case text := <-s.texts:
			if err := s.send(dlnet.MsgTypeText, dlnet.TypeText{Text: text}); err != nil {
				slog.Debug("send text to type", "err", err)
			}
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
