package daemon

import (
	"testing"
	"time"
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
