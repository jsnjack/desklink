package viewer

import (
	"testing"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
)

// Hardware keycodes of a US keyboard, for the tests.
const (
	keycodeE          = 26
	keycodeApostrophe = 48
	keycodeX          = 53
	keycodeLeft       = 113
)

func TestComposer(t *testing.T) {
	t.Run("plain keys go to the host as keys", func(t *testing.T) {
		c := newComposer()
		if c.press(gdk.KEY_e, keycodeE, true) {
			t.Error("a plain key went to the input method")
		}
	})
	t.Run("a dead key and the key after it go to the input method", func(t *testing.T) {
		c := newComposer()
		if !c.press(gdk.KEY_dead_acute, keycodeApostrophe, true) {
			t.Fatal("a dead key went to the host")
		}
		c.accepted()
		if !c.release(keycodeApostrophe) {
			t.Error("the dead key's release went to the host without its press")
		}
		if !c.press(gdk.KEY_e, keycodeE, true) {
			t.Fatal("the key completing the composition went to the host")
		}
		c.accepted()
		c.committed()
		if !c.release(keycodeE) {
			t.Error("the completing key's release went to the host without its press")
		}
		if c.press(gdk.KEY_x, keycodeX, true) {
			t.Error("a key after the commit went to the input method")
		}
	})
	t.Run("Compose starts a composition", func(t *testing.T) {
		c := newComposer()
		if !c.press(gdk.KEY_Multi_key, 135, true) {
			t.Error("Compose went to the host")
		}
	})
	t.Run("a host that cannot type text gets the raw keys", func(t *testing.T) {
		c := newComposer()
		if c.press(gdk.KEY_dead_acute, keycodeApostrophe, false) {
			t.Error("a dead key went to the input method for a host without keysyms")
		}
		if c.release(keycodeApostrophe) {
			t.Error("the dead key's release went to the input method")
		}
	})
	t.Run("a key the input method declines ends the composition", func(t *testing.T) {
		c := newComposer()
		c.press(gdk.KEY_dead_grave, keycodeApostrophe, true)
		c.accepted()
		if !c.press(gdk.KEY_Left, keycodeLeft, true) {
			t.Fatal("a key during the composition went to the host")
		}
		c.declined(keycodeLeft)
		if c.release(keycodeLeft) {
			t.Error("a declined key's release went to the input method")
		}
		if c.press(gdk.KEY_e, keycodeE, true) {
			t.Error("a key after a declined one went to the input method")
		}
	})
	t.Run("losing the focus ends the composition", func(t *testing.T) {
		c := newComposer()
		c.press(gdk.KEY_dead_acute, keycodeApostrophe, true)
		c.accepted()
		c.reset()
		if c.press(gdk.KEY_e, keycodeE, true) || c.release(keycodeApostrophe) {
			t.Error("the composition outlived the focus")
		}
	})
}
