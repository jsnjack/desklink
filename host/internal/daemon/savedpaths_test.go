package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"desklink/internal/desktop"
)

// clipboardLog records what savedPaths writes to the clipboard.
type clipboardLog struct {
	writes []string
	err    error
}

func (l *clipboardLog) write(_ context.Context, text string) error {
	if l.err != nil {
		return l.err
	}
	l.writes = append(l.writes, text)
	return nil
}

func TestSavedPathsPutTheFilesOfOneShareOnTheClipboard(t *testing.T) {
	t0 := time.Date(2026, 11, 26, 14, 0, 0, 0, time.UTC)
	type upload struct {
		peer, path     string
		started, saved time.Duration
	}
	cases := []struct {
		name    string
		uploads []upload
		want    string
		files   int
	}{
		{"one file", []upload{{"tab", "/home/me/Downloads/shot.png", 0, time.Second}},
			"/home/me/Downloads/shot.png", 1},
		{"three files of one share", []upload{
			{"tab", "/d/a.png", 0, time.Second},
			{"tab", "/d/b.png", 2 * time.Second, 3 * time.Second},
			{"tab", "/d/c.png", 3 * time.Second, time.Minute},
		}, "/d/a.png /d/b.png /d/c.png", 3},
		{"a file that took longer than the gap to arrive still joins its share", []upload{
			{"tab", "/d/a.png", 0, time.Second},
			{"tab", "/d/big.mp4", 2 * time.Second, 5 * time.Minute},
			{"tab", "/d/c.png", 5*time.Minute + time.Second, 5*time.Minute + 2*time.Second},
		}, "/d/a.png /d/big.mp4 /d/c.png", 3},
		{"a later share starts afresh", []upload{
			{"tab", "/d/a.png", 0, time.Second},
			{"tab", "/d/b.png", time.Minute, time.Minute + time.Second},
		}, "/d/b.png", 1},
		{"another device's file starts afresh", []upload{
			{"tab", "/d/a.png", 0, time.Second},
			{"laptop", "/d/b.png", 2 * time.Second, 3 * time.Second},
		}, "/d/b.png", 1},
		{"names a shell would split are quoted", []upload{
			{"tab", "/d/My shot.png", 0, time.Second},
			{"tab", "/d/it's.txt", 2 * time.Second, 3 * time.Second},
			{"tab", "/d/Снимок_1.png", 4 * time.Second, 5 * time.Second},
		}, `'/d/My shot.png' '/d/it'\''s.txt' /d/Снимок_1.png`, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			log := &clipboardLog{}
			s := &savedPaths{write: log.write}
			var copied bool
			var files int
			for _, u := range c.uploads {
				var text string
				var gen uint64
				text, files, gen = s.add(u.peer, u.path, t0.Add(u.started), t0.Add(u.saved))
				copied = s.copy(context.Background(), text, gen)
			}
			if !copied || files != c.files || len(log.writes) == 0 || log.writes[len(log.writes)-1] != c.want {
				t.Errorf("clipboard %q naming %d files (copied %v), want %q naming %d", log.writes, files, copied, c.want, c.files)
			}
		})
	}
}

func TestSavedPathsNeverReplaceANewerFilesPaths(t *testing.T) {
	log := &clipboardLog{}
	s := &savedPaths{write: log.write}
	t0 := time.Now()
	oldText, _, oldGen := s.add("tab", "/d/a.png", t0, t0)
	newText, _, newGen := s.add("laptop", "/d/b.png", t0, t0)
	if !s.copy(context.Background(), newText, newGen) {
		t.Fatal("the newer file's path was not copied")
	}
	if s.copy(context.Background(), oldText, oldGen) {
		t.Error("the older file's path replaced the newer one's")
	}
	if len(log.writes) != 1 || log.writes[0] != "/d/b.png" {
		t.Errorf("clipboard writes %q, want only /d/b.png", log.writes)
	}
}

func TestSavedPathsReportAClipboardThatFails(t *testing.T) {
	s := &savedPaths{write: (&clipboardLog{err: errors.New("wl-copy: no display")}).write}
	text, _, gen := s.add("tab", "/d/a.png", time.Now(), time.Now())
	if s.copy(context.Background(), text, gen) {
		t.Error("copy reported a failed write as done")
	}
}

func TestSavedPathsStartAfreshBeforeTheClipboardOverflows(t *testing.T) {
	s := &savedPaths{write: (&clipboardLog{}).write}
	long := "/d/" + strings.Repeat("x", 200)
	t0 := time.Now()
	var text string
	var files int
	for i := range desktop.MaxClipboardBytes / 200 {
		text, files, _ = s.add("tab", long, t0.Add(time.Duration(i)*time.Second), t0.Add(time.Duration(i)*time.Second))
	}
	if len(text) > desktop.MaxClipboardBytes || files >= desktop.MaxClipboardBytes/200 {
		t.Errorf("clipboard text of %d bytes naming %d files, want at most %d bytes", len(text), files, desktop.MaxClipboardBytes)
	}
}
