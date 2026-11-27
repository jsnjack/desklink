package daemon

import (
	"slices"
	"testing"

	dlnet "desklink/internal/net"
)

func absAt(x, y int32) dlnet.InputEvent {
	return dlnet.InputEvent{Kind: dlnet.InputAbsPointer, X: x, Y: y}
}

func TestConfine(t *testing.T) {
	scroll := dlnet.InputEvent{Kind: dlnet.InputScroll, Y: 120}
	press := dlnet.InputEvent{Kind: dlnet.InputMouseButton, Flags: 1, Code: 1}
	key := dlnet.InputEvent{Kind: dlnet.InputKey, Flags: 1, Code: 30}
	pxScroll := dlnet.InputEvent{Kind: dlnet.InputScrollPx, Y: 512}
	tests := []struct {
		name string
		in   []dlnet.InputEvent
		want []dlnet.InputEvent
	}{
		{"a scroll first puts the pointer on the client's screen", []dlnet.InputEvent{scroll}, []dlnet.InputEvent{absAt(32767, 32767), scroll}},
		{"relative motion becomes an absolute position", []dlnet.InputEvent{{Kind: dlnet.InputRelMove, X: 100, Y: -50}}, []dlnet.InputEvent{absAt(32767+2559, 30719)}},
		{"a click after motion is not placed twice", []dlnet.InputEvent{{Kind: dlnet.InputRelMove, X: 0, Y: 0}, press}, []dlnet.InputEvent{absAt(32767, 32767), press}},
		{"a pixel scroll first puts the pointer on the client's screen", []dlnet.InputEvent{pxScroll}, []dlnet.InputEvent{absAt(32767, 32767), pxScroll}},
		{"keys are left alone", []dlnet.InputEvent{key}, []dlnet.InputEvent{key}},
		{"motion stops at the frame's edge", []dlnet.InputEvent{{Kind: dlnet.InputRelMove, X: -99999, Y: 99999}}, []dlnet.InputEvent{absAt(0, 65535)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newClientCursor()
			got := confine(&c, tt.in, 2560, 1600)
			if !slices.Equal(got, tt.want) {
				t.Errorf("confine = %+v, want %+v", got, tt.want)
			}
		})
	}
	t.Run("a tap sets where the trackpad continues", func(t *testing.T) {
		c := newClientCursor()
		confine(&c, []dlnet.InputEvent{absAt(1000, 2000)}, 2560, 1600)
		got := confine(&c, []dlnet.InputEvent{scroll}, 2560, 1600)
		if !slices.Equal(got, []dlnet.InputEvent{absAt(1000, 2000), scroll}) {
			t.Errorf("after a tap, scroll = %+v", got)
		}
	})
}
