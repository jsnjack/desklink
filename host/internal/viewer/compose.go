package viewer

import "github.com/diamondburned/gotk4/pkg/gdk/v4"

// composer decides which keys the laptop's input method sees. Keys go to the
// host as evdev codes, so the host's layout decides what they type. A dead
// key or the Compose key starts a composition instead: from then on keys go
// to GTK's input method, which composes a character such as "é" from ´ and e
// and commits it as text that the host types as keysyms (Session.TypeText),
// whether or not the host's layout has the dead key. Only hosts that type
// text (SessionInfo.Keysyms) get this; others receive the raw keys as before.
type composer struct {
	composing bool
	// imKeys are the keycodes whose press went to the input method, so that
	// their release goes there too and never reaches the host on its own.
	imKeys map[uint]bool
}

func newComposer() *composer { return &composer{imKeys: map[uint]bool{}} }

// startsComposition reports whether keyval begins a composition: a dead key
// or Compose.
func startsComposition(keyval uint) bool {
	return keyval == gdk.KEY_Multi_key || (keyval >= gdk.KEY_dead_grave && keyval <= gdk.KEY_dead_longsolidusoverlay)
}

// press reports whether a key press goes to the input method: while a
// composition runs or to start one, and only for a host that types text.
// The caller then reports whether the input method took it (accepted or
// declined).
func (c *composer) press(keyval, keycode uint, typesText bool) bool {
	if !typesText || (!c.composing && !startsComposition(keyval)) {
		return false
	}
	c.imKeys[keycode] = true
	return true
}

// accepted notes that the input method took the key press: the composition
// goes on until it commits.
func (c *composer) accepted() { c.composing = true }

// declined notes that the input method left the key press alone, which ends
// the composition: the key then goes to the host as usual, and so does its
// release.
func (c *composer) declined(keycode uint) {
	delete(c.imKeys, keycode)
	c.composing = false
}

// release reports whether a key release goes to the input method, which got
// the key's press.
func (c *composer) release(keycode uint) bool {
	if !c.imKeys[keycode] {
		return false
	}
	delete(c.imKeys, keycode)
	return true
}

// committed ends the composition: the input method committed its text. A
// composition cancelled without a commit ends at the next key, which the
// input method either commits as text or declines.
func (c *composer) committed() { c.composing = false }

// reset forgets the composition, when the window loses focus or a session
// starts.
func (c *composer) reset() {
	c.composing = false
	clear(c.imKeys)
}
