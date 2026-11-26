package daemon

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode"

	"desklink/internal/desktop"
)

const (
	// shareGap is how soon after a device's last saved file its next upload
	// must start to belong to the same share. The tablet sends a share's
	// files one after the other, each after a /health request.
	shareGap = 10 * time.Second
	// clipboardWriteTimeout bounds putting saved paths on the clipboard.
	clipboardWriteTimeout = 5 * time.Second
)

// savedPaths puts the paths of the files devices send on the desk's
// clipboard, so they can be pasted wherever the file is needed. The files of
// one share go together, separated by spaces, each quoted when a shell
// would split it, as dropping files on a terminal pastes them.
type savedPaths struct {
	write func(ctx context.Context, text string) error

	mu   sync.Mutex
	peer string
	// text holds the paths of the share's files so far, and files counts
	// them.
	text  string
	files int
	// saved is when the share's last file was saved.
	saved time.Time
	// gen counts add calls, so an older share's text never replaces a newer
	// one's.
	gen uint64

	// writeMu keeps one clipboard write at a time.
	writeMu sync.Mutex
}

// add records path, saved for peer by an upload that started at started,
// and returns the clipboard text for its share, how many files that text
// names, and its generation for copy. A share that would outgrow the
// clipboard starts afresh.
func (s *savedPaths) add(peer, path string, started, now time.Time) (text string, files int, gen uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	word := shellQuote(path)
	if peer != s.peer || started.Sub(s.saved) > shareGap || len(s.text)+1+len(word) > desktop.MaxClipboardBytes {
		s.text, s.files = "", 0
	}
	if s.text != "" {
		s.text += " "
	}
	s.text += word
	s.files++
	s.peer, s.saved = peer, now
	s.gen++
	return s.text, s.files, s.gen
}

// copy puts text, from add's generation gen, on the clipboard unless a
// later add has made it stale, and reports whether it did.
func (s *savedPaths) copy(ctx context.Context, text string, gen uint64) bool {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	stale := gen != s.gen
	s.mu.Unlock()
	if stale {
		return false
	}
	if err := s.write(ctx, text); err != nil {
		slog.Warn("put a saved file's path on the clipboard", "err", err)
		return false
	}
	return true
}

// shellQuote returns s unchanged when a shell reads it as one word as it is,
// and in single quotes otherwise.
func shellQuote(s string) string {
	plain := s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("@%+=:,./_-", r)
	}) < 0
	if plain {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
