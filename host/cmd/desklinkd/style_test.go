package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"desklink/internal/doctor"
)

func TestPaletteIsOffWithoutATerminal(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
		if err := w.Close(); err != nil {
			t.Error(err)
		}
	}()
	cases := []struct {
		name string
		out  any
	}{
		{"a buffer", &bytes.Buffer{}},
		{"a pipe", w},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := c.out.(interface{ Write([]byte) (int, error) })
			if p := paletteFor(out); p.on {
				t.Error("colour is on")
			}
			if n := terminalWidth(out); n != 0 {
				t.Errorf("width %d, want 0", n)
			}
		})
	}
	t.Run("NO_COLOR turns it off on a terminal too", func(t *testing.T) {
		t.Setenv("NO_COLOR", "1")
		if p := paletteFor(os.Stdout); p.on {
			t.Error("colour is on")
		}
	})
}

func TestCheckLine(t *testing.T) {
	checks := []doctor.Check{
		{Status: doctor.OK, Name: "Input", Detail: "/dev/uinput is writable"},
		{Status: doctor.Warning, Name: "Wake-on-LAN", Detail: "off on enp7s0", Fix: "sudo ethtool -s enp7s0 wol g"},
		{Status: doctor.Problem, Name: "HEVC 10-bit", Detail: "not available"},
	}
	width := nameWidth(checks)
	t.Run("plain", func(t *testing.T) {
		var b strings.Builder
		if err := writeChecks(&b, palette{}, "  ", checks, width); err != nil {
			t.Fatal(err)
		}
		want := "  ✓ Input        /dev/uinput is writable\n" +
			"  ! Wake-on-LAN  off on enp7s0 → sudo ethtool -s enp7s0 wol g\n" +
			"  ✗ HEVC 10-bit  not available\n"
		if b.String() != want {
			t.Errorf("got\n%s\nwant\n%s", b.String(), want)
		}
	})
	t.Run("colour paints the symbol and dims the fix", func(t *testing.T) {
		p := palette{on: true}
		cases := []struct {
			check doctor.Check
			want  string
		}{
			{checks[0], sgrGreen + "✓" + sgrReset + " Input        /dev/uinput is writable"},
			{checks[1], sgrYellow + "!" + sgrReset + " Wake-on-LAN  off on enp7s0 " + sgrDim + "→ sudo ethtool -s enp7s0 wol g" + sgrReset},
			{checks[2], sgrRed + "✗" + sgrReset + " HEVC 10-bit  not available"},
		}
		for _, c := range cases {
			if got := checkLine(p, c.check, width); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		}
	})
}

func TestVisibleWidth(t *testing.T) {
	p := palette{on: true}
	cases := []struct {
		s    string
		want int
	}{
		{"plain", 5},
		{"✓ ok", 4},
		{p.bold("bold") + " and " + p.dim("dim"), 12},
		{"█▀▄ ", 4},
	}
	for _, c := range cases {
		if got := visibleWidth(c.s); got != c.want {
			t.Errorf("visibleWidth(%q) = %d, want %d", c.s, got, c.want)
		}
	}
}
