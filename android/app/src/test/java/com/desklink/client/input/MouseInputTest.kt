package com.desklink.client.input

import android.view.MotionEvent
import com.desklink.client.model.InputEvent
import com.desklink.client.model.InputKind
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class WheelAccumulatorTest {
    @Test
    fun aWholeNotchIsOneWheelUnitUpOrDown() {
        val w = WheelAccumulator()
        assertEquals(InputEvent.scroll(0, InputKind.WHEEL_UNIT), w.add(0f, 1f))
        assertEquals(InputEvent.scroll(0, -InputKind.WHEEL_UNIT), w.add(0f, -1f))
        assertEquals(InputEvent.scroll(InputKind.WHEEL_UNIT, 0), w.add(1f, 0f))
    }

    // A smooth-scrolling Bluetooth mouse reports small fractions of a notch;
    // each used to be truncated to nothing.
    @Test
    fun smallStepsAddUpInsteadOfBeingDropped() {
        val w = WheelAccumulator()
        var total = 0
        repeat(100) { w.add(0f, 0.004f)?.let { total += it.y } }
        assertEquals((100 * 0.004f * InputKind.WHEEL_UNIT).toInt(), total)
    }

    @Test
    fun underAUnitSendsNothing() {
        assertNull(WheelAccumulator().add(0f, 0.001f))
    }
}

class AbsoluteMouseTest {
    private val sent = mutableListOf<InputEvent>()
    private val mouse = AbsoluteMouse { sent += it }

    @Test
    fun followsThePointerAndSendsOnlyChanges() {
        mouse.update(100, 200, 0)
        mouse.update(100, 200, 0)
        mouse.update(150, 200, 0)
        assertEquals(listOf(InputEvent.pointer(100, 200), InputEvent.pointer(150, 200)), sent)
    }

    // An uncaptured mouse used to go through the finger gestures: left only,
    // a hold turned into a right click, the other buttons were lost.
    @Test
    fun everyButtonIsAClickNotATap() {
        mouse.update(10, 10, MotionEvent.BUTTON_SECONDARY)
        mouse.update(10, 10, MotionEvent.BUTTON_SECONDARY or MotionEvent.BUTTON_TERTIARY)
        mouse.update(10, 10, 0)
        mouse.update(10, 10, MotionEvent.BUTTON_FORWARD)
        assertEquals(
            listOf(
                InputEvent.pointer(10, 10), InputEvent.button(InputKind.BUTTON_RIGHT, true),
                InputEvent.button(InputKind.BUTTON_MIDDLE, true),
                InputEvent.button(InputKind.BUTTON_RIGHT, false), InputEvent.button(InputKind.BUTTON_MIDDLE, false),
                InputEvent.button(InputKind.BUTTON_EXTRA, true),
            ),
            sent,
        )
    }

    @Test
    fun aDragMovesWithTheButtonHeld() {
        mouse.update(10, 10, MotionEvent.BUTTON_PRIMARY)
        mouse.update(20, 10, MotionEvent.BUTTON_PRIMARY)
        mouse.update(20, 10, 0)
        assertEquals(
            listOf(
                InputEvent.pointer(10, 10), InputEvent.button(InputKind.BUTTON_LEFT, true),
                InputEvent.pointer(20, 10), InputEvent.button(InputKind.BUTTON_LEFT, false),
            ),
            sent,
        )
    }

    @Test
    fun backNeverReachesTheDesktop() {
        mouse.update(10, 10, MotionEvent.BUTTON_BACK)
        assertEquals(listOf(InputEvent.pointer(10, 10)), sent)
    }

    @Test
    fun leavingTheViewDoesNotMoveThePointer() {
        mouse.update(10, 10, 0)
        mouse.update(0, 0, 0, moved = false)
        assertEquals(listOf(InputEvent.pointer(10, 10)), sent)
    }
}

class MouseBackTest {
    private var toggles = 0
    private val back = MouseBack { toggles++ }

    @Test
    fun theButtonTogglesTheHud() {
        back.onButton(eventTime = 1_000)
        assertEquals(1, toggles)
    }

    // Android may also make a Back key from the same press: it must not
    // toggle the HUD a second time, whichever arrives first.
    @Test
    fun theKeyFromTheSamePressDoesNotToggleAgain() {
        back.onButton(eventTime = 1_000)
        back.onKeyUp(downTime = 1_000, canceled = false)
        back.onButton(eventTime = 5_002)
        back.onKeyUp(downTime = 5_000, canceled = false)
        assertEquals(2, toggles)
    }

    // With the HUD open the pointer may be over the HUD, where the button
    // never reaches the mapper; the key alone then closes it.
    @Test
    fun aKeyWithoutAButtonToggles() {
        back.onButton(eventTime = 1_000)
        back.onKeyUp(downTime = 9_000, canceled = false)
        assertEquals(2, toggles)
    }

    @Test
    fun aCanceledKeyDoesNothing() {
        back.onKeyUp(downTime = 9_000, canceled = true)
        assertEquals(0, toggles)
    }
}
