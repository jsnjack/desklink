package desktop

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	dlnet "desklink/internal/net"
)

// MaxClipboardBytes is the largest clipboard text that is synced.
const MaxClipboardBytes = 256 << 10

// ErrWatchUnsupported means the compositor lacks the data-control protocol
// that `wl-paste --watch` needs; Mutter does not implement it, so on GNOME the
// host reads the clipboard after copy shortcuts and on request instead.
var ErrWatchUnsupported = errors.New("compositor does not support clipboard watching")

// ErrClipboardTooLarge means the clipboard text exceeds MaxClipboardBytes, or
// its image the limit the caller set.
var ErrClipboardTooLarge = errors.New("clipboard content exceeds the sync limit")

// watchStartupGrace is how long wl-paste --watch must survive to count as
// supported; without data-control it exits at once.
const watchStartupGrace = 500 * time.Millisecond

// textType is the type clipboard text is written in.
const textType = "text/plain;charset=utf-8"

// Content is what the clipboard holds: text, or a PNG image.
type Content struct {
	Text string
	PNG  []byte
}

// key identifies the content for change detection; an image by its hash.
func (c Content) key() string {
	if c.PNG == nil {
		return c.Text
	}
	sum := sha256.Sum256(c.PNG)
	return "\x00png:" + hex.EncodeToString(sum[:])
}

// Clipboard reads and writes the Wayland clipboard with wl-clipboard.
type Clipboard struct {
	env []string

	mu   sync.Mutex
	last string
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

// Read returns the clipboard's content if it changed since the last Read or
// Write, and reports whether it changed. With imageLimit above zero, a
// clipboard that offers a PNG and no plain text (dlnet.ClipboardPrefersImage)
// is read as an image of at most imageLimit bytes; anything else as text.
// Asking for the offered types first costs a second wl-paste, which is why a
// client that takes no images is spared it.
func (c *Clipboard) Read(ctx context.Context, imageLimit int) (Content, bool, error) {
	if imageLimit > 0 {
		types, err := c.types(ctx)
		if err != nil {
			slog.Debug("list the clipboard's types; reading text", "err", err)
		} else if dlnet.ClipboardPrefersImage(types) {
			png, err := c.paste(ctx, dlnet.MIMETypePNG, imageLimit)
			if err != nil || len(png) == 0 {
				return Content{}, false, err
			}
			content := Content{PNG: png}
			return content, c.observe(content.key()), nil
		}
	}
	data, err := c.paste(ctx, "text", MaxClipboardBytes)
	if err != nil || data == nil {
		return Content{}, false, err
	}
	text := string(data)
	return Content{Text: text}, c.observe(text), nil
}

// types lists the MIME types the clipboard offers; none when it is empty.
func (c *Clipboard) types(ctx context.Context) ([]string, error) {
	data, err := c.paste(ctx, "", MaxClipboardBytes)
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(data)), nil
}

// paste runs wl-paste for the type and returns what it printed, at most
// limit bytes; nil when the clipboard is empty or does not offer the type.
// An empty type lists the offered types instead.
func (c *Clipboard) paste(ctx context.Context, mimeType string, limit int) ([]byte, error) {
	args := []string{"--list-types"}
	if mimeType != "" {
		args = []string{"--no-newline", "--type", mimeType}
	}
	cmd := exec.CommandContext(ctx, "wl-paste", args...)
	cmd.Env = c.env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("wl-paste stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start wl-paste: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(out, int64(limit)+1))
	if len(data) > limit {
		if err := cmd.Process.Kill(); err != nil {
			return nil, fmt.Errorf("stop wl-paste: %w", err)
		}
	}
	waitErr := cmd.Wait()
	switch {
	case len(data) > limit:
		return nil, ErrClipboardTooLarge
	case readErr != nil:
		return nil, fmt.Errorf("read wl-paste: %w", readErr)
	case waitErr != nil:
		msg := strings.TrimSpace(stderr.String())
		if strings.Contains(msg, "No selection") || strings.Contains(msg, "No suitable type") {
			return nil, nil
		}
		return nil, fmt.Errorf("wl-paste: %w: %s", waitErr, msg)
	}
	return data, nil
}

// observe records key as the clipboard's content and reports whether it
// differs from what was last read or written.
func (c *Clipboard) observe(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if key == c.last {
		return false
	}
	c.last = key
	return true
}

// wlCopyOutputLimit is how much of a failed wl-copy's output its error keeps.
const wlCopyOutputLimit = 4 << 10

// Write sets the clipboard text.
func (c *Clipboard) Write(ctx context.Context, text string) error {
	if len(text) > MaxClipboardBytes {
		return ErrClipboardTooLarge
	}
	if err := c.copy(ctx, textType, []byte(text)); err != nil {
		return err
	}
	c.observe(text)
	return nil
}

// WriteImage puts a PNG image on the clipboard.
func (c *Clipboard) WriteImage(ctx context.Context, png []byte) error {
	if !dlnet.IsPNG(png) {
		return errors.New("the clipboard image is not a PNG")
	}
	if err := c.copy(ctx, dlnet.MIMETypePNG, png); err != nil {
		return err
	}
	c.observe(Content{PNG: png}.key())
	return nil
}

// copy runs wl-copy with data as the clipboard's content in mimeType.
//
// wl-copy forks a child that serves the content until another program takes
// the clipboard, and the child keeps wl-copy's stdout and stderr open. With a
// pipe there, Wait would wait for the child too: on 2026-11-25 a session's
// control loop hung in here for minutes, so the session never ended. The
// output goes to an unlinked temporary file instead, which Wait does not wait
// for and the child can write to without dying of SIGPIPE.
func (c *Clipboard) copy(ctx context.Context, mimeType string, data []byte) error {
	out, err := os.CreateTemp("", "desklink-wl-copy-*")
	if err != nil {
		return fmt.Errorf("wl-copy output: %w", err)
	}
	defer func() { _ = out.Close() }()
	if err := os.Remove(out.Name()); err != nil {
		return fmt.Errorf("wl-copy output: %w", err)
	}
	cmd := exec.CommandContext(ctx, "wl-copy", "--type", mimeType)
	cmd.Env = c.env
	cmd.Stdin = bytes.NewReader(data)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		// ReadAt leaves the offset alone, which the child shares.
		msg, _ := io.ReadAll(io.NewSectionReader(out, 0, wlCopyOutputLimit))
		return fmt.Errorf("wl-copy: %w: %s", err, strings.TrimSpace(string(msg)))
	}
	return nil
}

// Watch calls onChange with new clipboard content, read as Read does with
// imageLimit, until ctx ends. It returns ErrWatchUnsupported at once on
// compositors without data-control.
func (c *Clipboard) Watch(ctx context.Context, imageLimit int, onChange func(Content)) error {
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
		content, changed, err := c.Read(ctx, imageLimit)
		if err != nil {
			slog.Debug("read clipboard after change", "err", err)
			continue
		}
		if changed {
			onChange(content)
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
