// Package logging wires log/slog from the --debug and --trace flags.
package logging

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
)

// LevelTrace is below Debug and only enabled by --trace.
const LevelTrace = slog.Level(-8)

// Logger is the configured logger plus whether it writes to the trace file.
type Logger struct {
	Tracing bool
	close   func() error
}

// Setup installs the default slog logger. Stderr gets warnings and errors,
// or debug output with --debug. --trace additionally writes everything to
// tracePath, truncated on every start; the two flags combine freely.
func Setup(debug, trace bool, tracePath string) (*Logger, error) {
	stderrLevel := slog.LevelWarn
	if debug {
		stderrLevel = slog.LevelDebug
	}
	handlers := []slog.Handler{slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: stderrLevel})}
	l := &Logger{close: func() error { return nil }}
	if trace {
		f, err := os.OpenFile(tracePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return nil, fmt.Errorf("open trace log: %w", err)
		}
		handlers = append(handlers, slog.NewTextHandler(f, &slog.HandlerOptions{Level: LevelTrace}))
		l.Tracing, l.close = true, f.Close
	}
	slog.SetDefault(slog.New(fanout(handlers)))
	return l, nil
}

// Announce prints a line that must always be visible, such as the listening
// address: to stderr, and also to the trace log when tracing.
func (l *Logger) Announce(msg string, args ...any) {
	line := fmt.Sprintf(msg, args...)
	fmt.Fprintln(os.Stderr, line)
	if l.Tracing {
		slog.Log(context.Background(), LevelTrace, line)
	}
}

// Close closes the trace file.
func (l *Logger) Close() {
	if err := l.close(); err != nil {
		fmt.Fprintln(os.Stderr, "close trace log:", err)
	}
}

// fanout sends each record to every handler that accepts its level.
type fanout []slog.Handler

func (f fanout) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range f {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (f fanout) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range f {
		if h.Enabled(ctx, r.Level) {
			errs = append(errs, h.Handle(ctx, r.Clone()))
		}
	}
	return errors.Join(errs...)
}

func (f fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (f fanout) WithGroup(name string) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithGroup(name)
	}
	return out
}
