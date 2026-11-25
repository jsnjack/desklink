package com.desklink.client.input

import com.desklink.client.input.TouchGestureEngine.Point
import com.desklink.client.model.InputEvent
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class SwipeTest {
    private val tracker = SwipeTracker()

    @Test
    fun aWorkspaceSwipeHoldsSuperAroundPageDown() {
        assertEquals(
            listOf(
                InputEvent.key(LinuxKeyCodes.KEY_LEFTMETA, true), InputEvent.key(LinuxKeyCodes.KEY_PAGEDOWN, true),
                InputEvent.key(LinuxKeyCodes.KEY_PAGEDOWN, false), InputEvent.key(LinuxKeyCodes.KEY_LEFTMETA, false),
            ),
            Swipe.LEFT.taps(),
        )
    }

    @Test
    fun travelAddsUpAcrossMoves() {
        assertNull(tracker.move(Point(0f, 0f), Point(0f, -60f)))
        assertEquals(Swipe.UP, tracker.move(Point(0f, -60f), Point(0f, -125f)))
    }

    @Test
    fun theLongerAxisPicksTheSwipe() {
        assertEquals(Swipe.RIGHT, tracker.move(Point(0f, 0f), Point(130f, -90f)))
    }

    @Test
    fun aSwipeFiresOnce() {
        assertEquals(Swipe.LEFT, tracker.move(Point(0f, 0f), Point(-130f, 0f)))
        assertNull(tracker.move(Point(-130f, 0f), Point(-400f, 0f)))
    }

    @Test
    fun aSwipeDownHasNoShortcutButIsNoTap() {
        assertNull(tracker.move(Point(0f, 0f), Point(0f, 130f)))
        assertTrue(tracker.fired)
    }
}
