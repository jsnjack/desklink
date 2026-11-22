// Package logging wires log/slog from the --debug and --trace flags.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
)

// LevelTrace is below Debug and only enabled by --trace.
const LevelTrace = slog.Level(-8)

// TracePath is where --trace writes, truncated on every start.
const TracePath = "/tmp/desklinkd.log"

// Logger is the configured logger plus whether it writes to the trace file.
type Logger struct {
	Tracing bool
	close   func() error
}

// Setup installs the default slog logger. Without flags it logs warnings and
// errors to stderr; --debug lowers that to debug; --trace writes everything
// to TracePath and leaves stderr for startup lines.
func Setup(debug, trace bool) (*Logger, error) {
	w := io.Writer(os.Stderr)
	level := slog.LevelWarn
	l := &Logger{close: func() error { return nil }}
	switch {
	case trace:
		f, err := os.OpenFile(TracePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return nil, fmt.Errorf("open trace log: %w", err)
		}
		w, level, l.Tracing, l.close = f, LevelTrace, true, f.Close
	case debug:
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})))
	return l, nil
}

// Announce prints a line that must always be visible, such as the listening
// address: to stderr, and also to the trace log when tracing.
func (l *Logger) Announce(msg string, args ...any) {
	fmt.Fprintln(os.Stderr, fmt.Sprintf(msg, args...))
	if l.Tracing {
		slog.Log(context.Background(), slog.LevelInfo, fmt.Sprintf(msg, args...))
	}
}

// Close closes the trace file.
func (l *Logger) Close() {
	if err := l.close(); err != nil {
		fmt.Fprintln(os.Stderr, "close trace log:", err)
	}
}
