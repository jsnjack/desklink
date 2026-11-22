package daemon

import (
	"testing"

	"desklink/internal/desktop"
	"desklink/internal/input"
)

func TestPointerMap(t *testing.T) {
	const max = input.AbsMax
	single := desktop.Rect{W: 2560, H: 1600}
	cases := []struct {
		name         string
		m            pointerMap
		x, y         int32
		wantX, wantY int32
	}{
		{"matching aspect passes through", newPointerMap(2560, 1600, single, single), max / 2, max / 4, max / 2, max / 4},
		{
			"letterbox borders are removed",
			newPointerMap(2560, 1600, desktop.Rect{W: 2560, H: 1440}, desktop.Rect{W: 2560, H: 1440}),
			0, 80 * max / 1600, 0, 0,
		},
		{
			"a tap inside the bottom border sticks to the edge",
			newPointerMap(2560, 1600, desktop.Rect{W: 2560, H: 1440}, desktop.Rect{W: 2560, H: 1440}),
			0, max, 0, max,
		},
		{
			"the right-hand monitor of two",
			newPointerMap(1920, 1080, desktop.Rect{X: 1920, W: 1920, H: 1080}, desktop.Rect{W: 3840, H: 1080}),
			0, 0, max / 2, 0,
		},
		{"unknown geometry passes through", newPointerMap(0, 0, desktop.Rect{}, desktop.Rect{}), 123, 456, 123, 456},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			x, y := c.m.apply(c.x, c.y)
			if abs(x-c.wantX) > 2 || abs(y-c.wantY) > 2 {
				t.Errorf("(%d,%d) → (%d,%d), want (%d,%d)", c.x, c.y, x, y, c.wantX, c.wantY)
			}
		})
	}
}

func abs(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}
