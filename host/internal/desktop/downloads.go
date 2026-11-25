package desktop

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// userDirTimeout bounds asking xdg-user-dir for a directory.
const userDirTimeout = 2 * time.Second

// DownloadDir is the user's download directory as xdg-user-dir reports it,
// or ~/Downloads when it reports none: xdg-user-dir prints the home
// directory for a directory that is not set, which the XDG spec counts as
// disabled, and files sent to the desk should not land in the home itself.
func DownloadDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find the home directory: %w", err)
	}
	fallback := filepath.Join(home, "Downloads")
	ctx, cancel := context.WithTimeout(context.Background(), userDirTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "xdg-user-dir", "DOWNLOAD").Output()
	if err != nil {
		slog.Debug("xdg-user-dir DOWNLOAD failed; using ~/Downloads", "err", err)
		return fallback, nil
	}
	dir := filepath.Clean(strings.TrimSpace(string(out)))
	if !filepath.IsAbs(dir) || dir == filepath.Clean(home) {
		return fallback, nil
	}
	return dir, nil
}
