package desktop

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// MaxClipboardBytes is the largest clipboard text that is synced.
const MaxClipboardBytes = 256 << 10

// ErrWatchUnsupported means the compositor lacks the data-control protocol
// that `wl-paste --watch` needs; Mutter does not implement it, so on GNOME the
// host reads the clipboard after copy shortcuts and on request instead.
var ErrWatchUnsupported = errors.New("compositor does not support clipboard watching")

// ErrClipboardTooLarge means the clipboard text exceeds MaxClipboardBytes.
var ErrClipboardTooLarge = errors.New("clipboard text exceeds the sync limit")

// watchStartupGrace is how long wl-paste --watch must survive to count as
// supported; without data-control it exits at once.
const watchStartupGrace = 500 * time.Millisecond

// Clipboard reads and writes the Wayland clipboard with wl-clipboard.
type Clipboard struct {
	env []string

	mu          sync.Mutex
	lastWritten string
	lastRead    string
}

// NewClipboard returns a Clipboard for the Wayland display. If the daemon's
// environment already names a display, that one is used.
func NewClipboard(waylandDisplay string) *Clipboard {
	env := os.Environ()
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		env = append(env, "WAYLAND_DISPLAY="+waylandDisplay)
	}
	return &Clipboard{env: env}
}

// Read returns the clipboard text if it changed since the last Read or Write,
// and reports whether it changed.
func (c *Clipboard) Read(ctx context.Context) (string, bool, error) {
	cmd := exec.CommandContext(ctx, "wl-paste", "--no-newline", "--type", "text")
	cmd.Env = c.env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return "", false, fmt.Errorf("wl-paste stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", false, fmt.Errorf("start wl-paste: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(out, MaxClipboardBytes+1))
	if len(data) > MaxClipboardBytes {
		if err := cmd.Process.Kill(); err != nil {
			return "", false, fmt.Errorf("stop wl-paste: %w", err)
		}
	}
	waitErr := cmd.Wait()
	switch {
	case len(data) > MaxClipboardBytes:
		return "", false, ErrClipboardTooLarge
	case readErr != nil:
		return "", false, fmt.Errorf("read wl-paste: %w", readErr)
	case waitErr != nil:
		msg := strings.TrimSpace(stderr.String())
		if strings.Contains(msg, "No selection") || strings.Contains(msg, "No suitable type") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("wl-paste: %w: %s", waitErr, msg)
	}
	text := string(data)
	c.mu.Lock()
	defer c.mu.Unlock()
	if text == c.lastRead || text == c.lastWritten {
		return text, false, nil
	}
	c.lastRead = text
	return text, true, nil
}

// Write sets the clipboard text.
func (c *Clipboard) Write(ctx context.Context, text string) error {
	if len(text) > MaxClipboardBytes {
		return ErrClipboardTooLarge
	}
	cmd := exec.CommandContext(ctx, "wl-copy", "--type", "text/plain;charset=utf-8")
	cmd.Env = c.env
	cmd.Stdin = strings.NewReader(text)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("wl-copy: %w: %s", err, strings.TrimSpace(string(out)))
	}
	c.mu.Lock()
	c.lastWritten = text
	c.lastRead = text
	c.mu.Unlock()
	return nil
}

// Watch calls onChange with new clipboard text until ctx ends. It returns
// ErrWatchUnsupported at once on compositors without data-control.
func (c *Clipboard) Watch(ctx context.Context, onChange func(text string)) error {
	cmd := exec.CommandContext(ctx, "wl-paste", "--watch", "echo")
	cmd.Env = c.env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("wl-paste stdout: %w", err)
	}
	started := time.Now()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start wl-paste --watch: %w", err)
	}
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		text, changed, err := c.Read(ctx)
		if err != nil {
			slog.Debug("read clipboard after change", "err", err)
			continue
		}
		if changed {
			onChange(text)
		}
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		return nil
	}
	if time.Since(started) < watchStartupGrace {
		return fmt.Errorf("%w: %s", ErrWatchUnsupported, strings.TrimSpace(stderr.String()))
	}
	return fmt.Errorf("wl-paste --watch exited: %w: %s", err, strings.TrimSpace(stderr.String()))
}
