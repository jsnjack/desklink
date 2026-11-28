package main

import (
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"desklink/internal/doctor"
)

// The SGR sequences of the few styles the output uses.
const (
	sgrReset  = "\x1b[0m"
	sgrBold   = "\x1b[1m"
	sgrDim    = "\x1b[2m"
	sgrRed    = "\x1b[31m"
	sgrGreen  = "\x1b[32m"
	sgrYellow = "\x1b[33m"
)

// palette paints terminal output. Colour only repeats what a line's symbol
// or words already say, so the zero palette, plain text, loses nothing.
type palette struct{ on bool }

// paletteFor paints output to w only when w is a terminal and neither
// NO_COLOR (no-color.org) nor TERM=dumb asks for plain text.
func paletteFor(w io.Writer) palette {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return palette{}
	}
	_, ok := terminal(w)
	return palette{on: ok}
}

func (p palette) paint(sgr, s string) string {
	if !p.on || s == "" {
		return s
	}
	return sgr + s + sgrReset
}

func (p palette) bold(s string) string { return p.paint(sgrBold, s) }

// dim is for hints: the command that fixes something, and what to do next.
func (p palette) dim(s string) string { return p.paint(sgrDim, s) }

func (p palette) green(s string) string { return p.paint(sgrGreen, s) }

func (p palette) yellow(s string) string { return p.paint(sgrYellow, s) }

func (p palette) red(s string) string { return p.paint(sgrRed, s) }

// symbol is st's mark in st's colour: green ✓, yellow ! or red ✗.
func (p palette) symbol(st doctor.Status) string {
	switch st {
	case doctor.Warning:
		return p.yellow(st.Symbol())
	case doctor.Problem:
		return p.red(st.Symbol())
	}
	return p.green(st.Symbol())
}

// terminal returns the terminal behind w; false when w is not one.
func terminal(w io.Writer) (*os.File, bool) {
	f, ok := w.(*os.File)
	if !ok {
		return nil, false
	}
	if _, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS); err != nil {
		return nil, false
	}
	return f, true
}

// terminalWidth is the width of the terminal behind w in columns; 0 when w
// is not a terminal.
func terminalWidth(w io.Writer) int {
	f, ok := terminal(w)
	if !ok {
		return 0
	}
	ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		return 0
	}
	return int(ws.Col)
}

// visibleWidth is how many columns s takes on a terminal: its characters,
// without the SGR sequences a palette adds.
func visibleWidth(s string) int {
	n := 0
	for len(s) > 0 {
		if strings.HasPrefix(s, "\x1b[") {
			if end := strings.IndexByte(s, 'm'); end >= 0 {
				s = s[end+1:]
				continue
			}
		}
		_, size := utf8.DecodeRuneInString(s)
		s = s[size:]
		n++
	}
	return n
}

// padRight pads s with spaces to width columns.
func padRight(s string, width int) string {
	return s + strings.Repeat(" ", max(0, width-visibleWidth(s)))
}

// nameWidth is the width of the longest check name, for the name column.
func nameWidth(checks []doctor.Check) int {
	w := 0
	for _, c := range checks {
		w = max(w, visibleWidth(c.Name))
	}
	return w
}

// checkLine is one check: its symbol, its name padded to width, what it
// found and, dimmed after an arrow, the command that fixes it.
func checkLine(p palette, c doctor.Check, width int) string {
	line := p.symbol(c.Status) + " " + padRight(c.Name, width) + "  " + c.Detail
	if c.Fix != "" {
		line += " " + p.dim("→ "+c.Fix)
	}
	return line
}

// writeChecks prints checks one per line after indent, their details in one
// column width wide.
func writeChecks(w io.Writer, p palette, indent string, checks []doctor.Check, width int) error {
	for _, c := range checks {
		if _, err := io.WriteString(w, indent+checkLine(p, c, width)+"\n"); err != nil {
			return err
		}
	}
	return nil
}
