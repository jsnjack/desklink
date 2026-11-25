package desktop

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDownloadDir(t *testing.T) {
	cases := []struct {
		name string
		// script is the fake xdg-user-dir's body; empty leaves none on PATH.
		script string
		want   func(home string) string
	}{
		{"the directory xdg-user-dir names", `echo /data/Incoming`, func(string) string { return "/data/Incoming" }},
		{"~/Downloads when it names the home directory", `echo "$HOME"`, func(home string) string { return filepath.Join(home, "Downloads") }},
		{"~/Downloads when it prints nothing", `exit 0`, func(home string) string { return filepath.Join(home, "Downloads") }},
		{"~/Downloads when it fails", `exit 1`, func(home string) string { return filepath.Join(home, "Downloads") }},
		{"~/Downloads without xdg-user-dir", "", func(home string) string { return filepath.Join(home, "Downloads") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			bin := t.TempDir()
			if c.script != "" {
				if err := os.WriteFile(filepath.Join(bin, "xdg-user-dir"), []byte("#!/bin/sh\n"+c.script+"\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", bin)
			got, err := DownloadDir()
			if err != nil {
				t.Fatal(err)
			}
			if want := c.want(home); got != want {
				t.Errorf("DownloadDir() = %q, want %q", got, want)
			}
		})
	}
}
