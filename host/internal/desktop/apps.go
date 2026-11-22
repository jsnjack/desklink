package desktop

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"desklink/internal/config"
)

// App is an entry of the client's app drawer.
type App struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Exec string `json:"exec"`
	// path is the .desktop file, empty for a pinned app with no entry.
	path string
}

// ErrUnknownApp means a launch request named an app that is not in the list.
var ErrUnknownApp = errors.New("app is not in the launcher list")

// DefaultAppDirs lists the .desktop directories in increasing priority, so a
// user entry overrides a system entry with the same ID.
func DefaultAppDirs() []string {
	dirs := []string{"/usr/share/applications", "/var/lib/flatpak/exports/share/applications"}
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		if home, err := os.UserHomeDir(); err == nil {
			data = filepath.Join(home, ".local", "share")
		}
	}
	if data != "" {
		dirs = append(dirs, filepath.Join(data, "flatpak", "exports", "share", "applications"), filepath.Join(data, "applications"))
	}
	return dirs
}

// ScanApps reads the .desktop files in dirs, skipping hidden entries, sorts
// them by name and puts the pinned apps first in their configured order. A
// pinned app takes over the matching entry (same ID or executable) and keeps
// its configured ID and name; without a match it is launched by its Exec.
func ScanApps(dirs []string, pinned []config.PinnedApp) []App {
	byID := map[string]App{}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				slog.Debug("read applications directory", "dir", dir, "err", err)
			}
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".desktop") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			app, ok, err := readDesktopFile(path)
			if err != nil {
				slog.Debug("parse desktop file", "path", path, "err", err)
				continue
			}
			id := strings.TrimSuffix(e.Name(), ".desktop")
			if !ok {
				delete(byID, id)
				continue
			}
			app.ID = id
			byID[id] = app
		}
	}

	var pinnedApps []App
	for _, p := range pinned {
		app := App{ID: p.ID, Name: p.Name, Exec: p.Exec}
		if id, ok := matchPinned(byID, p); ok {
			app.path = byID[id].path
			delete(byID, id)
		}
		pinnedApps = append(pinnedApps, app)
	}
	rest := make([]App, 0, len(byID))
	for _, a := range byID {
		rest = append(rest, a)
	}
	sort.Slice(rest, func(i, j int) bool {
		ni, nj := strings.ToLower(rest[i].Name), strings.ToLower(rest[j].Name)
		if ni != nj {
			return ni < nj
		}
		return rest[i].ID < rest[j].ID
	})
	return append(pinnedApps, rest...)
}

// matchPinned finds the entry a pinned app takes over: the one with its ID,
// otherwise the only entry with exactly its command line. Entries such as
// Flatpak exports share an executable, so a partial match is never used.
func matchPinned(byID map[string]App, p config.PinnedApp) (string, bool) {
	if _, ok := byID[p.ID]; ok {
		return p.ID, true
	}
	var match string
	n := 0
	for id, a := range byID {
		if a.Exec == stripFieldCodes(p.Exec) {
			match = id
			n++
		}
	}
	return match, n == 1
}

func readDesktopFile(path string) (App, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return App{}, false, fmt.Errorf("open: %w", err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			slog.Debug("close desktop file", "path", path, "err", err)
		}
	}()
	app, ok, err := parseDesktopEntry(f)
	app.path = path
	return app, ok, err
}

// parseDesktopEntry reads the [Desktop Entry] group. It reports ok=false for
// entries that should not be listed: non-applications, NoDisplay and Hidden.
func parseDesktopEntry(r io.Reader) (App, bool, error) {
	var app App
	inEntry := false
	visible := true
	isApp := false
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			inEntry = line == "[Desktop Entry]"
			continue
		}
		if !inEntry {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		switch strings.TrimSpace(key) {
		case "Type":
			isApp = strings.TrimSpace(value) == "Application"
		case "Name":
			app.Name = strings.TrimSpace(value)
		case "Exec":
			app.Exec = stripFieldCodes(strings.TrimSpace(value))
		case "NoDisplay", "Hidden":
			if strings.EqualFold(strings.TrimSpace(value), "true") {
				visible = false
			}
		}
	}
	if err := sc.Err(); err != nil {
		return App{}, false, fmt.Errorf("scan desktop entry: %w", err)
	}
	return app, isApp && visible && app.Name != "" && app.Exec != "", nil
}

var fieldCode = regexp.MustCompile(`\s*%[fFuUdDnNickvm]`)

func stripFieldCodes(execLine string) string {
	return strings.TrimSpace(strings.ReplaceAll(fieldCode.ReplaceAllString(execLine, ""), "%%", "%"))
}

var unitUnsafe = regexp.MustCompile(`[^A-Za-z0-9_.]`)

// Launcher starts apps from the list in their own systemd scope, so they keep
// running when desklinkd restarts.
type Launcher struct {
	env []string
}

// NewLauncher returns a Launcher for the Wayland display.
func NewLauncher(waylandDisplay string) *Launcher {
	env := os.Environ()
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		env = append(env, "WAYLAND_DISPLAY="+waylandDisplay)
	}
	return &Launcher{env: env}
}

// Find returns the app with the ID or Exec line from a launch request.
func Find(apps []App, id, execLine string) (App, error) {
	i := slices.IndexFunc(apps, func(a App) bool {
		return (id != "" && a.ID == id) || (id == "" && a.Exec == execLine)
	})
	if i < 0 {
		return App{}, fmt.Errorf("%w: id=%q exec=%q", ErrUnknownApp, id, execLine)
	}
	return apps[i], nil
}

// Launch starts the app. Entries with a .desktop file go through `gio launch`,
// which handles Terminal=true and D-Bus activation.
func (l *Launcher) Launch(app App) error {
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return fmt.Errorf("random unit suffix: %w", err)
	}
	unit := "app-desklink-" + unitUnsafe.ReplaceAllString(app.ID, "_") + "-" + hex.EncodeToString(suffix) + ".scope"
	args := []string{"--user", "--scope", "--collect", "--quiet", "--unit=" + unit, "--"}
	if app.path != "" {
		args = append(args, "gio", "launch", app.path)
	} else {
		fields := strings.Fields(app.Exec)
		if len(fields) == 0 {
			return fmt.Errorf("app %s has an empty Exec line", app.ID)
		}
		args = append(args, fields...)
	}
	cmd := exec.Command("systemd-run", args...)
	cmd.Env = l.env
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launch %s: %w", app.ID, err)
	}
	go func() {
		if err := cmd.Wait(); err != nil {
			slog.Debug("launched app exited with error", "app", app.ID, "err", err)
		}
	}()
	return nil
}
