package viewer

import "desklink/internal/client"

// sessionTicket names the session an asynchronous request was started for,
// such as a clipboard read, so its result goes to that session or nowhere.
// It holds no GTK state, so the rules below are testable without a display.
type sessionTicket struct {
	gen     uint64
	session *client.Session
}

// holds reports whether the ticket still names the view's current session:
// the same connection attempt with the same session installed. Disconnect,
// a closed session and a new connection all bump the generation.
func (t sessionTicket) holds(gen uint64, current *client.Session) bool {
	return t.session != nil && t.gen == gen && t.session == current
}

// clipboardPush is one read of the laptop clipboard, started for the session
// in its ticket.
type clipboardPush struct {
	ticket sessionTicket
	// onlyIfChanged skips text the host already has (last).
	onlyIfChanged bool
}

// accepts reports whether the text a finished read returned should be sent:
// the read worked and found text, the session that started it is still the
// current one, and, with onlyIfChanged, the text differs from last. A read
// that finishes after its session ended or was replaced is dropped, so its
// text never reaches another host.
func (p clipboardPush) accepts(text string, err error, gen uint64, current *client.Session, last string) bool {
	if err != nil || text == "" || !p.ticket.holds(gen, current) {
		return false
	}
	return !p.onlyIfChanged || text != last
}
