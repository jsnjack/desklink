package desktop

import (
	"bytes"
	"context"
	"errors"
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

// fakeClipboard puts a wl-copy and a wl-paste on PATH that keep the
// clipboard in a directory: its offered types, one per line, in "types" and
// its content in "clip". It returns a function that sets both, as another
// program copying would.
func fakeClipboard(t *testing.T) (dir string, set func(types string, content []byte)) {
	t.Helper()
	dir = t.TempDir()
	fakeWlCopy(t, `[ "$1" = --type ] && printf '%s\n' "$2" >"`+dir+`/types"
cat >"`+dir+`/clip"
`)
	bin := t.TempDir()
	paste := `#!/bin/sh
d="` + dir + `"
[ -f "$d/types" ] || { echo "No selection" >&2; exit 1; }
if [ "$1" = --list-types ]; then cat "$d/types"; exit 0; fi
want="$3"
if [ "$want" = text ]; then pattern='^text/'; else pattern="^$want\$"; fi
grep -q "$pattern" "$d/types" || { echo "No suitable type of content copied" >&2; exit 1; }
cat "$d/clip"
`
	if err := os.WriteFile(filepath.Join(bin, "wl-paste"), []byte(paste), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir, func(types string, content []byte) {
		if err := os.WriteFile(filepath.Join(dir, "types"), []byte(types), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "clip"), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// pngOf is a PNG signature followed by body.
func pngOf(body string) []byte {
	return append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, body...)
}

func TestClipboardImageRoundTrip(t *testing.T) {
	dir, set := fakeClipboard(t)
	c := NewClipboard("wayland-0")
	ctx := context.Background()
	png := pngOf("first")
	if err := c.WriteImage(ctx, png); err != nil {
		t.Fatal(err)
	}
	types, err := os.ReadFile(filepath.Join(dir, "types"))
	if err != nil || strings.TrimSpace(string(types)) != "image/png" {
		t.Fatalf("wl-copy was given type %q, %v; want image/png", types, err)
	}
	got, changed, err := c.Read(ctx, 1<<20)
	if err != nil || !bytes.Equal(got.PNG, png) || changed {
		t.Errorf("Read after WriteImage = %d bytes, changed %v, %v; want the image, unchanged", len(got.PNG), changed, err)
	}
	other := pngOf("second")
	set("text/html\nimage/png\n", other)
	got, changed, err = c.Read(ctx, 1<<20)
	if err != nil || !bytes.Equal(got.PNG, other) || !changed {
		t.Errorf("Read after another app copied an image = %d bytes, changed %v, %v; want the new image, changed", len(got.PNG), changed, err)
	}
	if _, changed, _ := c.Read(ctx, 1<<20); changed {
		t.Error("reading the same image again counts as a change")
	}
}

func TestClipboardReadChoosesTextOrImage(t *testing.T) {
	png := pngOf("image")
	cases := []struct {
		name       string
		types      string
		content    []byte
		imageLimit int
		want       Content
		wantErr    error
	}{
		{"an image and no text is read as the image", "image/png\n", png, 1 << 20, Content{PNG: png}, nil},
		{"text offered beside an image wins", "text/plain;charset=utf-8\nimage/png\n", []byte("cell"), 1 << 20, Content{Text: "cell"}, nil},
		{"a client without images gets no image", "image/png\n", png, 0, Content{}, nil},
		{"text reads as before without images", "text/plain;charset=utf-8\n", []byte("hello"), 0, Content{Text: "hello"}, nil},
		{"an image over the limit is refused", "image/png\n", png, len(png) - 1, Content{}, ErrClipboardTooLarge},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, set := fakeClipboard(t)
			set(c.types, c.content)
			got, _, err := NewClipboard("wayland-0").Read(context.Background(), c.imageLimit)
			if !errors.Is(err, c.wantErr) || (c.wantErr == nil && err != nil) {
				t.Fatalf("Read error %v, want %v", err, c.wantErr)
			}
			if got.Text != c.want.Text || !bytes.Equal(got.PNG, c.want.PNG) {
				t.Errorf("Read = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestClipboardWriteImageRefusesWhatIsNotAPNG(t *testing.T) {
	dir, _ := fakeClipboard(t)
	if err := NewClipboard("wayland-0").WriteImage(context.Background(), []byte("GIF89a")); err == nil {
		t.Error("WriteImage took a GIF")
	}
	if _, err := os.Stat(filepath.Join(dir, "clip")); err == nil {
		t.Error("wl-copy ran for an image that is not a PNG")
	}
}
