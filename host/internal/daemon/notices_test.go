package daemon

import (
	"strings"
	"testing"

	"desklink/internal/desktop"
)

func TestSessionNoticeSaysWhetherTheSessionUnlockedTheDesk(t *testing.T) {
	cases := []struct {
		name     string
		device   string
		unlocked bool
		want     string
	}{
		{"a session that unlocked the desk", "Tab S9", true, "Tab S9 connected and unlocked this computer"},
		{"a session on an open desk", "Tab S9", false, "Tab S9 connected"},
		{"a device without a name", "  ", false, "A device connected"},
		{"a name with control characters", "Tab\nS9\u202e", true, "TabS9 connected and unlocked this computer"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sessionNotice(c.device, c.unlocked); got.Summary != c.want || got.File != "" {
				t.Errorf("notice %+v, want summary %q and no file", got, c.want)
			}
		})
	}
}

func TestFileNoticeNamesTheFileTheDeviceAndTheFolder(t *testing.T) {
	cases := []struct {
		name, path, home string
		want             desktop.Notice
	}{
		{"a file in the home's Downloads", "/home/me/Downloads/report.pdf", "/home/me",
			desktop.Notice{Summary: "Saved report.pdf from Tab", Body: "In ~/Downloads", File: "/home/me/Downloads/report.pdf"}},
		{"a file outside the home", "/data/in/report.pdf", "/home/me",
			desktop.Notice{Summary: "Saved report.pdf from Tab", Body: "In /data/in", File: "/data/in/report.pdf"}},
		{"a folder that only starts like the home", "/home/meg/report.pdf", "/home/me",
			desktop.Notice{Summary: "Saved report.pdf from Tab", Body: "In /home/meg", File: "/home/meg/report.pdf"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := fileNotice("Tab", c.path, c.home)
			if got.Summary != c.want.Summary || got.Body != c.want.Body || got.File != c.want.File {
				t.Errorf("notice %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestNoticeNameShortensLongNames(t *testing.T) {
	got := noticeName(strings.Repeat("ж", 100), "x")
	if r := []rune(got); len(r) != maxNoticeName || r[len(r)-1] != '…' {
		t.Errorf("noticeName kept %d characters ending in %q, want %d ending in …", len(r), r[len(r)-1], maxNoticeName)
	}
}
