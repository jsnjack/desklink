package desktop

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestClipboardChangeDetection(t *testing.T) {
	c := NewClipboard("wayland-0")
	steps := []struct {
		text string
		want bool
	}{
		{"X", true},
		{"X", false},
		{"Y", true},
		{"X", true},
	}
	for i, s := range steps {
		if got := c.observe(s.text); got != s.want {
			t.Errorf("step %d (%q): changed=%v, want %v", i, s.text, got, s.want)
		}
	}
}

// fakeWlCopy puts a wl-copy that runs script on PATH.
func fakeWlCopy(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wl-copy"), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestClipboardWriteDoesNotWaitForTheServingChild is the hang of
// 2026-11-25: wl-copy forks a child that keeps its output open while it
// serves the clipboard, and Write waited for that output to end.
func TestClipboardWriteDoesNotWaitForTheServingChild(t *testing.T) {
	dir := t.TempDir()
	fakeWlCopy(t, `cat >"`+dir+`/stdin"
sleep 60 &
echo $! >"`+dir+`/child"
`)
	t.Cleanup(func() {
		raw, err := os.ReadFile(filepath.Join(dir, "child"))
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil {
			return
		}
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
	})
	c := NewClipboard("wayland-0")
	done := make(chan error, 1)
	go func() { done <- c.Write(context.Background(), "copied") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Write: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Write waited for the child that serves the clipboard")
	}
	got, err := os.ReadFile(filepath.Join(dir, "stdin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "copied" {
		t.Errorf("wl-copy got %q on stdin, want %q", got, "copied")
	}
	if c.observe("copied") {
		t.Error("Write did not record the text it wrote")
	}
}

func TestClipboardWriteReportsWlCopyError(t *testing.T) {
	fakeWlCopy(t, "echo 'Failed to connect to a Wayland server' >&2\nexit 1\n")
	err := NewClipboard("wayland-0").Write(context.Background(), "copied")
	if err == nil || !strings.Contains(err.Error(), "Failed to connect to a Wayland server") {
		t.Fatalf("Write error = %v, want wl-copy's message", err)
	}
}
