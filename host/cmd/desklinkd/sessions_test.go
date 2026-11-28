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
	if err := writeSessions(&out, palette{}, recs, 20); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[1], "Laptop") || !strings.Contains(lines[2], "1920x1200@60") || !strings.Contains(lines[2], "23m0s") {
		t.Errorf("table, newest first, = %q", out.String())
	}
	if strings.Contains(out.String(), "\x1b[") {
		t.Errorf("colour without a terminal: %q", out.String())
	}
	out.Reset()
	if err := writeSessions(&out, palette{}, recs, 1); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Tablet  10.0.0.5") || strings.Count(out.String(), "\n") != 2 {
		t.Errorf("-n 1 listed more than the newest session: %q", out.String())
	}
	out.Reset()
	if err := writeSessions(&out, palette{}, nil, 20); err != nil || !strings.Contains(out.String(), "No sessions") {
		t.Errorf("an empty history printed %q", out.String())
	}
}

func TestSessionsColourOnlyWhyTheyEnded(t *testing.T) {
	start := time.Date(2026, 11, 28, 9, 12, 0, 0, time.Local)
	ended := []struct {
		reason string
		sgr    string // "" for no colour
	}{
		{"client left", sgrGreen},
		{"taken over by ThinkPad", sgrGreen},
		{"reconnected", sgrYellow},
		{"failed: the media connection was lost: nothing arrived from the client over UDP for 10s", sgrYellow},
		{"failed: start the pipeline: no VA-API device", sgrRed},
		{"a reason from a newer version", ""},
	}
	var recs []config.SessionRecord
	for i, e := range ended {
		s := start.Add(time.Duration(i) * time.Hour)
		recs = append(recs, config.SessionRecord{Device: "Galaxy Tab S9", From: "192.168.178.42", Start: s, End: s.Add(time.Minute), Ended: e.reason})
	}
	var plain, coloured strings.Builder
	if err := writeSessions(&plain, palette{}, recs, len(recs)); err != nil {
		t.Fatal(err)
	}
	if err := writeSessions(&coloured, palette{on: true}, recs, len(recs)); err != nil {
		t.Fatal(err)
	}
	if got := stripSGR(coloured.String()); got != plain.String() {
		t.Errorf("colour changed the table:\n%s\nwant\n%s", got, plain.String())
	}
	lines := strings.Split(strings.TrimSpace(coloured.String()), "\n")
	if strings.Contains(lines[0], "\x1b[") {
		t.Errorf("the header is coloured: %q", lines[0])
	}
	for i, e := range ended {
		line := lines[len(lines)-1-i]
		want := e.reason
		if e.sgr != "" {
			want = e.sgr + e.reason + sgrReset
		}
		if !strings.HasSuffix(line, want) || strings.Count(line, "\x1b[") > 2 {
			t.Errorf("%q: line %q, want it to end in %q and nothing else coloured", e.reason, line, want)
		}
	}
}
