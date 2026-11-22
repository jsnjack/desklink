package com.desklink.client.net

import com.desklink.client.model.InputEvent
import com.desklink.client.model.InputKind
import org.junit.Assert.assertEquals
import org.junit.Test

class HeldInputTest {
    private fun held(flags: Int, code: Int) = InputEvent(InputKind.HELD, flags, code)

    @Test
    fun nothingHeldIsOneEmptyRecord() =
        assertEquals(listOf(held(InputKind.HELD_NONE, 0)), HeldInput().snapshot())

    @Test
    fun pressedKeysAndButtonsAreListedUntilReleased() {
        val h = HeldInput()
        h.observe(InputEvent.key(29, true))
        h.observe(InputEvent.key(46, true))
        h.observe(InputEvent.button(InputKind.BUTTON_LEFT, true))
        h.observe(InputEvent.key(46, false))
        assertEquals(
            listOf(held(InputKind.HELD_KEY, 29), held(InputKind.HELD_BUTTON, InputKind.BUTTON_LEFT)),
            h.snapshot(),
        )
    }

    @Test
    fun penStateFollowsTheLastPenEvent() {
        val h = HeldInput()
        h.observe(InputEvent.pen(InputKind.PEN_TOUCH, 1, 1))
        assertEquals(listOf(held(InputKind.HELD_PEN, InputKind.PEN_HELD_TOUCH or InputKind.PEN_HELD_IN_RANGE)), h.snapshot())
        h.observe(InputEvent.pen(InputKind.PEN_HOVER, 1, 1))
        assertEquals(listOf(held(InputKind.HELD_PEN, InputKind.PEN_HELD_IN_RANGE)), h.snapshot())
        h.observe(InputEvent.pen(InputKind.PEN_OUT, 1, 1))
        assertEquals(listOf(held(InputKind.HELD_NONE, 0)), h.snapshot())
    }
}
