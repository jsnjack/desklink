package client

import (
	"slices"
	"sync"

	dlnet "desklink/internal/net"
)

// AbsMax is the top of the absolute coordinate range on the wire.
const AbsMax = 65535

// Wire codes of mouse buttons.
const (
	ButtonLeft   = 1
	ButtonRight  = 2
	ButtonMiddle = 3
	ButtonSide   = 4
	ButtonExtra  = 5
)

// Held-input record flags; docs/protocol.md describes the snapshot.
const (
	heldNone   = 0
	heldKey    = 1
	heldButton = 2
)

// wheelUnit is one wheel notch in the scroll record's 1/120 units.
const wheelUnit = 120

// surfaceUnitsPerPixel converts touchpad scroll distance (pixels) to 1/120
// notches, so 40 pixels scroll one notch.
const surfaceUnitsPerPixel = 3

// Rect is a rectangle in widget pixels.
type Rect struct {
	X, Y, W, H float64
}

// ContentRect is where a video of videoW x videoH appears when scaled to fit,
// centred, inside a widget of widgetW x widgetH.
func ContentRect(widgetW, widgetH, videoW, videoH float64) Rect {
	if widgetW <= 0 || widgetH <= 0 || videoW <= 0 || videoH <= 0 {
		return Rect{W: widgetW, H: widgetH}
	}
	scale := min(widgetW/videoW, widgetH/videoH)
	w, h := videoW*scale, videoH*scale
	return Rect{X: (widgetW - w) / 2, Y: (widgetH - h) / 2, W: w, H: h}
}

// Normalize converts a widget position to 0..AbsMax within r, clamped to
// its edges.
func Normalize(x, y float64, r Rect) (int32, int32) {
	if r.W <= 0 || r.H <= 0 {
		return 0, 0
	}
	u := max(0, min((x-r.X)/r.W, 1))
	v := max(0, min((y-r.Y)/r.H, 1))
	return int32(u * AbsMax), int32(v * AbsMax)
}

// ButtonCode maps a GDK button number to the wire's button code.
func ButtonCode(gdkButton uint) (uint16, bool) {
	switch gdkButton {
	case 1:
		return ButtonLeft, true
	case 2:
		return ButtonMiddle, true
	case 3:
		return ButtonRight, true
	case 8:
		return ButtonSide, true
	case 9:
		return ButtonExtra, true
	}
	return 0, false
}

// EvdevFromKeycode converts a GDK hardware keycode to a Linux evdev code. On
// Linux, Wayland and X11 keycodes are the evdev code plus 8.
func EvdevFromKeycode(keycode uint) (uint16, bool) {
	if keycode < 9 || keycode > 248+8 {
		return 0, false
	}
	return uint16(keycode - 8), true
}

// ScrollAccumulator turns GTK scroll deltas into scroll records, carrying
// fractions between events.
type ScrollAccumulator struct {
	restX, restY float64
}

// Add converts one GTK scroll event. GTK reports positive dy for scrolling
// down and positive dx for right; the wire uses positive for up and right.
// Wheel deltas count notches; surface deltas (touchpads) count pixels.
func (a *ScrollAccumulator) Add(dx, dy float64, surface bool) (hwheel, wheel int32) {
	scale := float64(wheelUnit)
	if surface {
		scale = surfaceUnitsPerPixel
	}
	a.restX += dx * scale
	a.restY -= dy * scale
	hwheel, wheel = int32(a.restX), int32(a.restY)
	a.restX -= float64(hwheel)
	a.restY -= float64(wheel)
	return hwheel, wheel
}

// KeyTracker remembers which keys the window pressed, so they can be
// released when it loses focus mid-press.
type KeyTracker struct {
	mu   sync.Mutex
	down []uint16
}

// Press records a key down and reports whether it was already down.
func (k *KeyTracker) Press(code uint16) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if slices.Contains(k.down, code) {
		return true
	}
	k.down = append(k.down, code)
	return false
}

// Held reports whether code is pressed.
func (k *KeyTracker) Held(code uint16) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return slices.Contains(k.down, code)
}

// Release records a key up.
func (k *KeyTracker) Release(code uint16) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.down = slices.DeleteFunc(k.down, func(c uint16) bool { return c == code })
}

// ReleaseAll returns key-up events for every pressed key and forgets them.
func (k *KeyTracker) ReleaseAll() []dlnet.InputEvent {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make([]dlnet.InputEvent, 0, len(k.down))
	for _, c := range k.down {
		out = append(out, dlnet.InputEvent{Kind: dlnet.InputKey, Code: c})
	}
	k.down = nil
	return out
}

// heldInput tracks what the outgoing input holds down, for the snapshot
// that lets the host lift keys whose up packet was lost.
type heldInput struct {
	keys    []uint16
	buttons []uint16
}

func (h *heldInput) observe(e dlnet.InputEvent) {
	var set *[]uint16
	switch e.Kind {
	case dlnet.InputKey:
		set = &h.keys
	case dlnet.InputMouseButton:
		set = &h.buttons
	default:
		return
	}
	*set = slices.DeleteFunc(*set, func(c uint16) bool { return c == e.Code })
	if e.Flags == 1 {
		*set = append(*set, e.Code)
	}
}

// releaseAll returns the release of every held key and button and forgets
// them.
func (h *heldInput) releaseAll() []dlnet.InputEvent {
	var out []dlnet.InputEvent
	for _, k := range h.keys {
		out = append(out, dlnet.InputEvent{Kind: dlnet.InputKey, Code: k})
	}
	for _, b := range h.buttons {
		out = append(out, dlnet.InputEvent{Kind: dlnet.InputMouseButton, Code: b})
	}
	h.keys, h.buttons = nil, nil
	return out
}

func (h *heldInput) snapshot() []dlnet.InputEvent {
	var out []dlnet.InputEvent
	for _, k := range h.keys {
		out = append(out, dlnet.InputEvent{Kind: dlnet.InputHeld, Flags: heldKey, Code: k})
	}
	for _, b := range h.buttons {
		out = append(out, dlnet.InputEvent{Kind: dlnet.InputHeld, Flags: heldButton, Code: b})
	}
	if len(out) == 0 {
		out = append(out, dlnet.InputEvent{Kind: dlnet.InputHeld, Flags: heldNone})
	}
	return out
}
