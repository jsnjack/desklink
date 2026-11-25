package main

import (
	"strings"
	"testing"
	"time"

	"desklink/internal/config"
)

func TestWriteSessions(t *testing.T) {
	start := time.Date(2026, 11, 25, 10, 0, 0, 0, time.Local)
	recs := []config.SessionRecord{
		{Device: "Tablet", From: "10.0.0.5", Start: start, End: start.Add(23 * time.Minute), Display: "own 2560x1600 screen at 200%", WorstLevel: "1920x1200@60", Ended: "client left"},
		{Device: "Laptop", From: "10.0.0.7", Start: start.Add(time.Hour), End: start.Add(time.Hour + time.Minute), Ended: "taken over by Tablet"},
	}
	var out strings.Builder
	if err := writeSessions(&out, recs, 20); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[1], "Laptop") || !strings.Contains(lines[2], "1920x1200@60") || !strings.Contains(lines[2], "23m0s") {
		t.Errorf("table, newest first, = %q", out.String())
	}
	out.Reset()
	if err := writeSessions(&out, recs, 1); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Tablet  10.0.0.5") || strings.Count(out.String(), "\n") != 2 {
		t.Errorf("-n 1 listed more than the newest session: %q", out.String())
	}
	out.Reset()
	if err := writeSessions(&out, nil, 20); err != nil || !strings.Contains(out.String(), "No sessions") {
		t.Errorf("an empty history printed %q", out.String())
	}
}
