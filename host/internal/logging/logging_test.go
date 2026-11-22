package logging

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestFanoutHonoursEachLevel(t *testing.T) {
	var warnOnly, everything bytes.Buffer
	log := slog.New(fanout{
		slog.NewTextHandler(&warnOnly, &slog.HandlerOptions{Level: slog.LevelWarn}),
		slog.NewTextHandler(&everything, &slog.HandlerOptions{Level: LevelTrace}),
	}).With("session", 7)
	log.Log(context.Background(), LevelTrace, "wire bytes")
	log.Warn("lost frame")
	cases := []struct {
		name, out string
		want      []string
		notWant   []string
	}{
		{"stderr gets warnings only", warnOnly.String(), []string{"lost frame", "session=7"}, []string{"wire bytes"}},
		{"trace file gets everything", everything.String(), []string{"wire bytes", "lost frame", "session=7"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, w := range c.want {
				if !strings.Contains(c.out, w) {
					t.Errorf("missing %q in %q", w, c.out)
				}
			}
			for _, w := range c.notWant {
				if strings.Contains(c.out, w) {
					t.Errorf("unexpected %q in %q", w, c.out)
				}
			}
		})
	}
}
