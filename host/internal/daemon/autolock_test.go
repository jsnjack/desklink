package daemon

import (
	"strings"
	"testing"
	"time"

	"desklink/internal/desktop"
)

func TestShouldAutolock(t *testing.T) {
	now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		service string
		age     time.Duration
		want    bool
	}{
		{"automatic login that just started", "gdm-autologin", 20 * time.Second, true},
		{"automatic login hours ago, daemon restarted", "gdm-autologin", 3 * time.Hour, false},
		{"password login", "gdm-password", 20 * time.Second, false},
		{"fingerprint login", "gdm-fingerprint", 5 * time.Second, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldAutolock(tt.service, now.Add(-tt.age), now); got != tt.want {
				t.Errorf("shouldAutolock(%q, %v ago) = %v, want %v", tt.service, tt.age, got, tt.want)
			}
		})
	}
}

// desklinkd warns at startup unless GDM logs the user back in whenever the
// session ends, and says how to turn that on.
func TestAutologinWarning(t *testing.T) {
	if got := autologinWarning(desktop.GDMLogin{Automatic: true, Timed: true, TimedDelay: 10}); got != "" {
		t.Errorf("on: warned %q", got)
	}
	if got := autologinWarning(desktop.GDMLogin{Timed: true, TimedDelay: 10}); got != "" {
		t.Errorf("timed login also covers boot: warned %q", got)
	}
	for name, l := range map[string]desktop.GDMLogin{"off": {}, "at boot only": {Automatic: true}} {
		if got := autologinWarning(l); !strings.Contains(got, "desklinkd autologin on") {
			t.Errorf("%s: %q does not say how to fix it", name, got)
		}
	}
}
