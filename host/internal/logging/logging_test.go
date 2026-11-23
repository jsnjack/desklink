package logging

import (
	"bytes"
	"context"
	"log/slog"
	"os"
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

func TestSetupWritesTraceFile(t *testing.T) {
	path := t.TempDir() + "/trace.log"
	l, err := Setup(false, true, path)
	if err != nil {
		t.Fatal(err)
	}
	slog.Log(context.Background(), LevelTrace, "deep detail")
	l.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "deep detail") {
		t.Errorf("trace file lacks the record: %q", data)
	}
	if !l.Tracing {
		t.Error("Tracing is false")
	}
}
