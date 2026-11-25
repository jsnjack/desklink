package desktop

//go:generate go run keysyms_gen.go

// X keysyms that text needs besides characters.
const (
	keysymBackSpace uint32 = 0xff08
	keysymTab       uint32 = 0xff09
	keysymReturn    uint32 = 0xff0d
	// keysymUnicode is added to a code point that has no keysym of its own.
	keysymUnicode uint32 = 0x01000000
)

// Keysym is the X keysym that types r, as libxkbcommon's xkb_utf32_to_keysym
// (and so gnome-remote-desktop) picks it: the code point itself for
// printable ASCII and Latin-1, a legacy keysym where one exists, such as
// Cyrillic_a for "а" or EuroSign for "€", and 0x01000000 + code point
// otherwise. Mutter types a keysym only if the active layout has it, and
// layouts name their symbols by these legacy keysyms. A newline, carriage
// return, tab and backspace become Return, Tab and BackSpace; ok is false for
// every other control character and for code points that are not
// characters, which nothing types.
func Keysym(r rune) (keysym uint32, ok bool) {
	switch {
	case r == '\n' || r == '\r':
		return keysymReturn, true
	case r == '\t':
		return keysymTab, true
	case r == '\b':
		return keysymBackSpace, true
	case r < 0x20 || (r >= 0x7f && r < 0xa0):
		return 0, false
	case r <= 0xff:
		return uint32(r), true
	case r > 0x10ffff || (r >= 0xd800 && r <= 0xdfff) || (r >= 0xfdd0 && r <= 0xfdef) || r&0xfffe == 0xfffe:
		return 0, false
	}
	if k, found := legacyKeysyms[r]; found {
		return k, true
	}
	return keysymUnicode + uint32(r), true
}
