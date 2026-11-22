package input

import (
	"fmt"
	"sync"
)

// The client translates Android key events to Linux evdev codes before
// sending them (LinuxKeyCodes.kt); hardware keyboards report the evdev code
// as the Android scan code. The host registers and accepts this range.
const (
	MinKey = 1
	MaxKey = 248
)

// Linux evdev key codes the host treats specially.
const (
	KeyC         = 46
	KeyX         = 45
	KeyInsert    = 110
	KeyLeftCtrl  = 29
	KeyRightCtrl = 97
)

// ValidKey reports whether code is a registered keyboard key.
func ValidKey(code uint16) bool { return code >= MinKey && code <= MaxKey }

var keyNames = map[uint16]string{
	1: "ESC", 2: "1", 3: "2", 4: "3", 5: "4", 6: "5", 7: "6", 8: "7", 9: "8", 10: "9", 11: "0",
	12: "MINUS", 13: "EQUAL", 14: "BACKSPACE", 15: "TAB",
	16: "Q", 17: "W", 18: "E", 19: "R", 20: "T", 21: "Y", 22: "U", 23: "I", 24: "O", 25: "P",
	26: "LEFTBRACE", 27: "RIGHTBRACE", 28: "ENTER", 29: "LEFTCTRL",
	30: "A", 31: "S", 32: "D", 33: "F", 34: "G", 35: "H", 36: "J", 37: "K", 38: "L",
	39: "SEMICOLON", 40: "APOSTROPHE", 41: "GRAVE", 42: "LEFTSHIFT", 43: "BACKSLASH",
	44: "Z", 45: "X", 46: "C", 47: "V", 48: "B", 49: "N", 50: "M",
	51: "COMMA", 52: "DOT", 53: "SLASH", 54: "RIGHTSHIFT", 55: "KPASTERISK", 56: "LEFTALT",
	57: "SPACE", 58: "CAPSLOCK",
	59: "F1", 60: "F2", 61: "F3", 62: "F4", 63: "F5", 64: "F6", 65: "F7", 66: "F8", 67: "F9", 68: "F10",
	69: "NUMLOCK", 70: "SCROLLLOCK",
	71: "KP7", 72: "KP8", 73: "KP9", 74: "KPMINUS", 75: "KP4", 76: "KP5", 77: "KP6", 78: "KPPLUS",
	79: "KP1", 80: "KP2", 81: "KP3", 82: "KP0", 83: "KPDOT",
	86: "102ND", 87: "F11", 88: "F12",
	96: "KPENTER", 97: "RIGHTCTRL", 98: "KPSLASH", 99: "SYSRQ", 100: "RIGHTALT",
	102: "HOME", 103: "UP", 104: "PAGEUP", 105: "LEFT", 106: "RIGHT", 107: "END", 108: "DOWN",
	109: "PAGEDOWN", 110: "INSERT", 111: "DELETE",
	113: "MUTE", 114: "VOLUMEDOWN", 115: "VOLUMEUP", 116: "POWER", 117: "KPEQUAL", 119: "PAUSE",
	121: "KPCOMMA", 125: "LEFTMETA", 126: "RIGHTMETA", 127: "COMPOSE",
	142: "SLEEP", 158: "BACK", 159: "FORWARD", 163: "NEXTSONG", 164: "PLAYPAUSE", 165: "PREVIOUSSONG",
	166: "STOPCD", 172: "HOMEPAGE", 173: "REFRESH",
	183: "F13", 184: "F14", 185: "F15", 186: "F16", 187: "F17", 188: "F18", 189: "F19", 190: "F20",
	191: "F21", 192: "F22", 193: "F23", 194: "F24",
	217: "SEARCH", 224: "BRIGHTNESSDOWN", 225: "BRIGHTNESSUP",
}

// KeyName returns "KEY_<NAME>" for logs.
func KeyName(code uint16) string {
	if n, ok := keyNames[code]; ok {
		return "KEY_" + n
	}
	return fmt.Sprintf("KEY_%d", code)
}

// CopyDetector watches key events for copy shortcuts (Ctrl+C, Ctrl+X,
// Ctrl+Shift+C in terminals, Ctrl+Insert), after which the host reads the
// clipboard and sends it to the client.
type CopyDetector struct {
	mu   sync.Mutex
	ctrl map[uint16]bool
}

// Observe records a key event and reports whether it completed a copy shortcut.
func (d *CopyDetector) Observe(code uint16, down bool) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ctrl == nil {
		d.ctrl = map[uint16]bool{}
	}
	switch code {
	case KeyLeftCtrl, KeyRightCtrl:
		d.ctrl[code] = down
		return false
	case KeyC, KeyX, KeyInsert:
		return down && (d.ctrl[KeyLeftCtrl] || d.ctrl[KeyRightCtrl])
	}
	return false
}
