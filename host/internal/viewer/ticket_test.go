package viewer

import (
	"errors"
	"testing"

	"desklink/internal/client"
)

func TestClipboardPushBindsToItsSession(t *testing.T) {
	first, second := new(client.Session), new(client.Session)
	started := sessionTicket{gen: 3, session: first}
	cases := []struct {
		name          string
		ticket        sessionTicket
		onlyIfChanged bool
		text          string
		err           error
		gen           uint64
		current       *client.Session
		last          string
		want          bool
	}{
		{"the session that started the read gets the text", started, false, "hi", nil, 3, first, "", true},
		{"a disconnect during the read drops the text", started, false, "hi", nil, 4, nil, "", false},
		{"a new session to another host during the read drops the text", started, false, "hi", nil, 4, second, "", false},
		{"a replaced session drops the text even in the same attempt", started, false, "hi", nil, 3, second, "", false},
		{"a read started without a session sends nothing", sessionTicket{gen: 3}, false, "hi", nil, 3, nil, "", false},
		{"a failed read sends nothing", started, false, "", errors.New("no text"), 3, first, "", false},
		{"an empty clipboard sends nothing", started, false, "", nil, 3, first, "", false},
		{"text the host already has is skipped on focus", started, true, "hi", nil, 3, first, "hi", false},
		{"changed text is sent on focus", started, true, "new", nil, 3, first, "hi", true},
		{"the button sends the text even when unchanged", started, false, "hi", nil, 3, first, "hi", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := clipboardPush{ticket: c.ticket, onlyIfChanged: c.onlyIfChanged}
			if got := p.accepts(c.text, c.err, c.gen, c.current, c.last); got != c.want {
				t.Errorf("accepts = %v, want %v", got, c.want)
			}
		})
	}
}
