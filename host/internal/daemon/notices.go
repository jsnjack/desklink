package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"desklink/internal/config"
	"desklink/internal/desktop"
)

const (
	// noticeTimeout bounds showing a notification at the desk.
	noticeTimeout = 5 * time.Second
	// maxNoticeName bounds a device or file name in a notice; the device
	// chose its name when it paired.
	maxNoticeName = 64
	sessionIcon   = "computer"
	fileIcon      = "folder-download"
	unnamedDevice = "A device"
)

// sessionNotice is the notification a starting session shows at the desk,
// saying whether the session unlocked it.
func sessionNotice(device string, unlocked bool) desktop.Notice {
	summary := noticeName(device, unnamedDevice) + " connected"
	if unlocked {
		summary += " and unlocked this computer"
	}
	return desktop.Notice{Summary: summary, Icon: sessionIcon}
}

// fileNotice is the notification for a file a device sent, saved at path;
// its body names the folder, with ~ for home, and says when the clipboard
// holds the paths of the last copied files of its share (0: none).
func fileNotice(device, path, home string, copied int) desktop.Notice {
	dir := filepath.Dir(path)
	if home != "" && (dir == home || strings.HasPrefix(dir, home+string(filepath.Separator))) {
		dir = "~" + strings.TrimPrefix(dir, home)
	}
	body := "In " + dir
	switch {
	case copied == 1:
		body += " · path copied"
	case copied > 1:
		body += fmt.Sprintf(" · paths of %d files copied", copied)
	}
	return desktop.Notice{
		Summary: "Saved " + noticeName(filepath.Base(path), "a file") + " from " + noticeName(device, unnamedDevice),
		Body:    body,
		Icon:    fileIcon,
		File:    path,
	}
}

// noticeName is a name fit for a notice: without control or invisible
// formatting characters, at most maxNoticeName characters, or fallback when
// nothing is left.
func noticeName(name, fallback string) string {
	name = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, name))
	if r := []rune(name); len(r) > maxNoticeName {
		name = string(r[:maxNoticeName-1]) + "…"
	}
	if name == "" {
		return fallback
	}
	return name
}

// notify shows note at the desk in the background, so a slow notification
// server delays nothing.
func (d *Daemon) notify(note desktop.Notice) {
	if d.notifier == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), noticeTimeout)
		defer cancel()
		if _, err := d.notifier.Notify(ctx, note); err != nil {
			slog.Warn("show a notice at the desk", "summary", note.Summary, "err", err)
		}
	}()
}

// fileReceived puts the path of a file a device sent, with the paths of the
// files sent before it in the same share, on the desk's clipboard, and tells
// the desk about it.
func (d *Daemon) fileReceived(peer config.Peer, path string, started time.Time) {
	home, err := os.UserHomeDir()
	if err != nil {
		slog.Debug("find the home directory for a notice", "err", err)
	}
	if d.saved == nil {
		d.notify(fileNotice(peer.Name, path, home, 0))
		return
	}
	// The next upload of the share may start right after this one's reply,
	// so nothing here may hold up the upload handler.
	text, files, gen := d.saved.add(peer.ID, path, started, time.Now())
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), clipboardWriteTimeout)
		defer cancel()
		if !d.saved.copy(ctx, text, gen) {
			files = 0
		}
		d.notify(fileNotice(peer.Name, path, home, files))
	}()
}
