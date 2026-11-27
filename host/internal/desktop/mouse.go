package desktop

import (
	"context"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// gsettingsTimeout bounds one gsettings call.
const gsettingsTimeout = 2 * time.Second

// mouseSchema holds GNOME's settings for mice.
const mouseSchema = "org.gnome.desktop.peripherals.mouse"

// MouseSettings are GNOME's settings for mice, which a client applies to a
// mouse it captures so that it moves the desk's pointer as a mouse at the
// desk would.
type MouseSettings struct {
	// Speed is the pointer speed, -1..1; 0 is libinput's default.
	Speed float64
	// Flat is set for the flat profile; otherwise libinput's adaptive
	// acceleration applies, as for "default" and for a custom curve, which
	// clients do not reproduce.
	Flat bool
}

// ReadMouseSettings reads GNOME's mouse speed and acceleration profile;
// whatever it cannot read keeps its default (speed 0, adaptive).
func ReadMouseSettings(ctx context.Context) MouseSettings {
	return parseMouseSettings(gsettingsGet(ctx, "speed"), gsettingsGet(ctx, "accel-profile"))
}

// parseMouseSettings reads gsettings' output for speed ("-0.25") and
// accel-profile ("'flat'"); an empty or unreadable value keeps its default.
func parseMouseSettings(speed, profile string) MouseSettings {
	var m MouseSettings
	if v, err := strconv.ParseFloat(speed, 64); err == nil {
		m.Speed = max(-1, min(v, 1))
	}
	m.Flat = strings.Trim(profile, "'") == "flat"
	return m
}

// gsettingsGet returns one key of mouseSchema as gsettings prints it, or ""
// when it cannot be read.
func gsettingsGet(ctx context.Context, key string) string {
	ctx, cancel := context.WithTimeout(ctx, gsettingsTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gsettings", "get", mouseSchema, key).Output()
	if err != nil {
		slog.Debug("read GNOME's mouse setting", "key", key, "err", err)
		return ""
	}
	return strings.TrimSpace(string(out))
}
