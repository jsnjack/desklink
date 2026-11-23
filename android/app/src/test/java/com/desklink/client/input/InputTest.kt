package com.desklink.client.input

import android.view.KeyEvent
import com.desklink.client.model.InputEvent
import com.desklink.client.model.InputKind
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class LinuxKeyCodesTest {
    @Test
    fun hardwareScanCodesPassThrough() {
        assertEquals(30, LinuxKeyCodes.fromKeyEvent(scanCode = 30, keyCode = KeyEvent.KEYCODE_Q))
    }

    @Test
    fun keyCodesMapWhenThereIsNoScanCode() {
        val cases = mapOf(
            KeyEvent.KEYCODE_A to 30, KeyEvent.KEYCODE_Z to 44, KeyEvent.KEYCODE_0 to 11, KeyEvent.KEYCODE_1 to 2,
            KeyEvent.KEYCODE_ESCAPE to 1, KeyEvent.KEYCODE_TAB to 15, KeyEvent.KEYCODE_ENTER to 28, KeyEvent.KEYCODE_DEL to 14,
            KeyEvent.KEYCODE_FORWARD_DEL to 111, KeyEvent.KEYCODE_CTRL_LEFT to 29, KeyEvent.KEYCODE_ALT_LEFT to 56,
            KeyEvent.KEYCODE_META_LEFT to 125, KeyEvent.KEYCODE_DPAD_UP to 103, KeyEvent.KEYCODE_DPAD_LEFT to 105,
            KeyEvent.KEYCODE_F1 to 59, KeyEvent.KEYCODE_F10 to 68, KeyEvent.KEYCODE_F11 to 87, KeyEvent.KEYCODE_F12 to 88,
            KeyEvent.KEYCODE_GRAVE to 41, KeyEvent.KEYCODE_PAGE_DOWN to 109,
        )
        for ((android, linux) in cases) {
            assertEquals("Android key code $android", linux, LinuxKeyCodes.fromKeyEvent(0, android))
        }
    }

    @Test
    fun aKeyTheLayoutMadeEscapeSendsEscape() {
        // A layout that makes the scan code of ` Escape keeps Escape.
        assertEquals(1, LinuxKeyCodes.fromKeyEvent(scanCode = 41, keyCode = KeyEvent.KEYCODE_ESCAPE))
        // A real ` still types `, through the host's layout.
        assertEquals(41, LinuxKeyCodes.fromKeyEvent(scanCode = 41, keyCode = KeyEvent.KEYCODE_GRAVE))
        // Character keys keep the scan code: an AZERTY "A" sits where US has Q.
        assertEquals(16, LinuxKeyCodes.fromKeyEvent(scanCode = 16, keyCode = KeyEvent.KEYCODE_A))
    }

    @Test
    fun theSlimCoverKeyboardSendsEscFromItsTopLeftKey() {
        // Plain top-left key: scan code of `, which Samsung's layout types as `.
        assertEquals(1, LinuxKeyCodes.fromKeyEvent(scanCode = 41, keyCode = KeyEvent.KEYCODE_GRAVE, escFirst = true))
        // Fn with it: KEY_FN_ESC, which Samsung's layout makes Escape.
        assertEquals(41, LinuxKeyCodes.fromKeyEvent(scanCode = 465, keyCode = KeyEvent.KEYCODE_ESCAPE, escFirst = true))
        // Other keys are unchanged.
        assertEquals(16, LinuxKeyCodes.fromKeyEvent(scanCode = 16, keyCode = KeyEvent.KEYCODE_Q, escFirst = true))
        // Without the swap, the key types ` and Fn+key is Esc, as in Android.
        assertEquals(41, LinuxKeyCodes.fromKeyEvent(scanCode = 41, keyCode = KeyEvent.KEYCODE_GRAVE))
        assertEquals(1, LinuxKeyCodes.fromKeyEvent(scanCode = 465, keyCode = KeyEvent.KEYCODE_ESCAPE))
    }

    @Test
    fun onlyTheSlimCoverKeyboardSwapsEsc() {
        assertTrue(LinuxKeyCodes.escFirstKeyboard("Book Cover Keyboard Slim (EF-DX720)", 0x04E8))
        assertFalse(LinuxKeyCodes.escFirstKeyboard("Book Cover Keyboard (EF-DX715)", 0x04E8))
        assertFalse(LinuxKeyCodes.escFirstKeyboard("Book Cover Keyboard Slim (EF-DX720)", 0x046D))
        assertFalse(LinuxKeyCodes.escFirstKeyboard(null, 0x04E8))
    }

    @Test
    fun unknownKeysHaveNoCode() = assertNull(LinuxKeyCodes.fromKeyEvent(0, KeyEvent.KEYCODE_CAMERA))

    @Test
    fun scanCodesOutsideTheRegisteredRangeFallBack() =
        assertEquals(1, LinuxKeyCodes.fromKeyEvent(scanCode = 600, keyCode = KeyEvent.KEYCODE_ESCAPE))

    @Test
    fun charactersMapToUsLayout() {
        assertEquals(30 to false, LinuxKeyCodes.forChar('a'))
        assertEquals(30 to true, LinuxKeyCodes.forChar('A'))
        assertEquals(2 to true, LinuxKeyCodes.forChar('!'))
        assertEquals(53 to false, LinuxKeyCodes.forChar('/'))
        assertEquals(57 to false, LinuxKeyCodes.forChar(' '))
        assertNull(LinuxKeyCodes.forChar('€'))
    }
}

class ModifierLatchTest {
    @Test
    fun latchedModifierWrapsTheNextKeyOnly() {
        val l = ModifierLatch()
        assertTrue(l.toggle(LinuxKeyCodes.KEY_LEFTCTRL))
        assertEquals(listOf(InputEvent.key(29, true), InputEvent.key(46, true)), l.keyDown(46))
        assertEquals(listOf(InputEvent.key(46, false), InputEvent.key(29, false)), l.keyUp(46))
        assertEquals(listOf(InputEvent.key(46, true)), l.keyDown(46))
    }

    @Test
    fun togglingTwiceUnlatches() {
        val l = ModifierLatch()
        l.toggle(LinuxKeyCodes.KEY_LEFTALT)
        l.toggle(LinuxKeyCodes.KEY_LEFTALT)
        assertTrue(l.active.isEmpty())
    }

    @Test
    fun packKeepsEachGroupInOnePacket() {
        val tap = { code: Int -> listOf(InputEvent.key(code, true), InputEvent.key(code, false)) }
        val shifted = { code: Int -> ModifierLatch().tap(code, shift = true) }
        val groups = List(60) { if (it % 3 == 0) shifted(30) else tap(30) }
        val packets = ModifierLatch.pack(groups, 104)
        assertTrue(packets.all { it.size <= 104 })
        assertEquals(groups.flatten(), packets.flatten())
        var pos = 0
        for (p in packets) {
            var consumed = 0
            while (consumed < p.size) consumed += groups[pos++].size
            assertEquals("a group was split across packets", p.size, consumed)
        }
    }

    @Test
    fun shiftedTapWrapsWithShift() {
        val shift = LinuxKeyCodes.KEY_LEFTSHIFT
        assertEquals(
            listOf(InputEvent.key(shift, true), InputEvent.key(30, true), InputEvent.key(30, false), InputEvent.key(shift, false)),
            ModifierLatch().tap(30, shift = true),
        )
    }
}

class TouchGestureEngineTest {
    private val out = mutableListOf<InputEvent>()
    private var huds = 0
    private val engine = TouchGestureEngine({ out += it }, { huds++ }, sensitivity = 2f, slop = 10f)

    private fun p(x: Float, y: Float) = TouchGestureEngine.Point(x, y)
    private fun touch(a: TouchGestureEngine.Action, t: Long, vararg pts: TouchGestureEngine.Point) = engine.onTouch(a, pts.toList(), t)

    private val leftClick = listOf(InputEvent.button(InputKind.BUTTON_LEFT, true), InputEvent.button(InputKind.BUTTON_LEFT, false))

    @Test
    fun tapClicks() {
        touch(TouchGestureEngine.Action.DOWN, 0, p(100f, 100f))
        touch(TouchGestureEngine.Action.UP, 80, p(101f, 100f))
        assertEquals(leftClick, out)
    }

    @Test
    fun longPressWithoutMovementDoesNotClick() {
        touch(TouchGestureEngine.Action.DOWN, 0, p(100f, 100f))
        touch(TouchGestureEngine.Action.UP, 900, p(100f, 100f))
        assertTrue(out.isEmpty())
    }

    @Test
    fun dragMovesTheCursorScaledBySensitivity() {
        touch(TouchGestureEngine.Action.DOWN, 0, p(0f, 0f))
        touch(TouchGestureEngine.Action.MOVE, 10, p(20f, 0f))
        touch(TouchGestureEngine.Action.MOVE, 20, p(25f, 5f))
        touch(TouchGestureEngine.Action.UP, 30, p(25f, 5f))
        assertEquals(listOf(InputEvent.move(40, 0), InputEvent.move(10, 10)), out)
    }

    @Test
    fun doubleTapAndHoldDragsWithTheLeftButton() {
        touch(TouchGestureEngine.Action.DOWN, 0, p(100f, 100f))
        touch(TouchGestureEngine.Action.UP, 50, p(100f, 100f))
        out.clear()
        touch(TouchGestureEngine.Action.DOWN, 200, p(102f, 101f))
        touch(TouchGestureEngine.Action.MOVE, 300, p(132f, 101f))
        touch(TouchGestureEngine.Action.UP, 400, p(132f, 101f))
        assertEquals(
            listOf(InputEvent.button(InputKind.BUTTON_LEFT, true), InputEvent.move(60, 0), InputEvent.button(InputKind.BUTTON_LEFT, false)),
            out,
        )
    }

    @Test
    fun twoFingerTapRightClicks() {
        touch(TouchGestureEngine.Action.DOWN, 0, p(100f, 100f))
        touch(TouchGestureEngine.Action.POINTER_DOWN, 10, p(100f, 100f), p(200f, 100f))
        touch(TouchGestureEngine.Action.POINTER_UP, 80, p(100f, 100f))
        touch(TouchGestureEngine.Action.UP, 90, p(100f, 100f))
        assertEquals(listOf(InputEvent.button(InputKind.BUTTON_RIGHT, true), InputEvent.button(InputKind.BUTTON_RIGHT, false)), out)
    }

    @Test
    fun twoFingerDragScrollsNaturally() {
        touch(TouchGestureEngine.Action.DOWN, 0, p(100f, 100f))
        touch(TouchGestureEngine.Action.POINTER_DOWN, 10, p(100f, 100f), p(200f, 100f))
        touch(TouchGestureEngine.Action.MOVE, 20, p(100f, 120f), p(200f, 120f))
        touch(TouchGestureEngine.Action.MOVE, 30, p(100f, 160f), p(200f, 160f))
        touch(TouchGestureEngine.Action.UP, 40, p(100f, 160f))
        assertEquals("fingers moving down scroll up", listOf(InputEvent.scroll(0, 60), InputEvent.scroll(0, 120)), out)
    }

    @Test
    fun threeFingerTapTogglesTheHud() {
        touch(TouchGestureEngine.Action.DOWN, 0, p(100f, 100f))
        touch(TouchGestureEngine.Action.POINTER_DOWN, 5, p(100f, 100f), p(150f, 100f))
        touch(TouchGestureEngine.Action.POINTER_DOWN, 10, p(100f, 100f), p(150f, 100f), p(200f, 100f))
        touch(TouchGestureEngine.Action.UP, 120, p(100f, 100f))
        assertEquals(1, huds)
        assertTrue(out.isEmpty())
    }

    @Test
    fun suppressedTapDoesNotClick() {
        touch(TouchGestureEngine.Action.DOWN, 0, p(100f, 100f))
        engine.suppressTap()
        touch(TouchGestureEngine.Action.UP, 50, p(100f, 100f))
        assertTrue(out.isEmpty())
    }

    @Test
    fun cancelReleasesADrag() {
        touch(TouchGestureEngine.Action.DOWN, 0, p(10f, 10f))
        touch(TouchGestureEngine.Action.UP, 20, p(10f, 10f))
        out.clear()
        touch(TouchGestureEngine.Action.DOWN, 100, p(10f, 10f))
        touch(TouchGestureEngine.Action.CANCEL, 150, p(10f, 10f))
        assertEquals(listOf(InputEvent.button(InputKind.BUTTON_LEFT, true), InputEvent.button(InputKind.BUTTON_LEFT, false)), out)
    }
}
