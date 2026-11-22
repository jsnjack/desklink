package desktop

import "testing"

func TestClipboardChangeDetection(t *testing.T) {
	c := NewClipboard("wayland-0")
	steps := []struct {
		text string
		want bool
	}{
		{"X", true},
		{"X", false},
		{"Y", true},
		{"X", true},
	}
	for i, s := range steps {
		if got := c.observe(s.text); got != s.want {
			t.Errorf("step %d (%q): changed=%v, want %v", i, s.text, got, s.want)
		}
	}
}
