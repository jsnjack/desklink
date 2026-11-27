package com.desklink.client.input

import com.desklink.client.model.CursorShape
import com.desklink.client.model.InputEvent
import com.desklink.client.model.InputKind
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class LocalPointerTest {
    private var t = 10_000L
    private var changes = 0
    private val p = LocalPointer { t }.also {
        it.listener = { changes++ }
        it.start(2560, 1600)
    }

    @Test
    fun startsAtTheCentreLikeTheHostsCopy() {
        assertEquals(InputKind.ABS_MAX / 2f, p.x)
        assertEquals(InputKind.ABS_MAX / 2f, p.y)
        assertNull(p.shape)
    }

    // The host turns relative motion into a position the same way
    // (host/internal/daemon/cursor_test.go: 100 px right on a 2560-wide screen).
    @Test
    fun relativeMotionMovesAsTheHostMovesTheRealPointer() {
        p.sent(listOf(InputEvent.move(100, -50)))
        assertEquals(32767.5f + 100f * 65535 / 2560, p.x, 0.01f)
        assertEquals(32767.5f - 50f * 65535 / 1600, p.y, 0.01f)
        p.sent(listOf(InputEvent.move(-99999, 99999)))
        assertEquals(0f, p.x)
        assertEquals(65535f, p.y)
    }

    @Test
    fun touchesAndThePenPlaceIt() {
        p.sent(listOf(InputEvent.pointer(1000, 2000)))
        assertEquals(1000f, p.x)
        p.sent(listOf(InputEvent.pen(InputKind.PEN_HOVER, 3000, 4000)))
        assertEquals(3000f, p.x)
        assertEquals(4000f, p.y)
    }

    @Test
    fun keysAndButtonsLeaveItAlone() {
        val before = changes
        p.sent(listOf(InputEvent.key(30, true), InputEvent.button(InputKind.BUTTON_LEFT, true)))
        assertEquals(before, changes)
    }

    @Test
    fun theDesksReportsCountOnlyOnceTheTabletLeftThePointerAlone() {
        p.sent(listOf(InputEvent.pointer(1000, 1000)))
        t += LocalPointer.IDLE_MS - 1
        p.hostAt(5000, 5000)
        assertEquals("a report behind the tablet's own moves", 1000f, p.x)
        t += 1
        p.hostAt(5000, 5000)
        assertEquals("someone at the desk moved it", 5000f, p.x)
        // The next move starts from there, as on the host.
        p.sent(listOf(InputEvent.move(0, 0)))
        assertEquals(5000f, p.x)
    }

    @Test
    fun aShapeAndANewSessionAreNoticed() {
        val shape = CursorShape(1, 1)
        p.shape(shape)
        assertEquals(shape, p.shape)
        val before = changes
        p.start(1600, 2560)
        assertNull(p.shape)
        assertEquals(before + 1, changes)
        p.sent(listOf(InputEvent.move(1600, 0)))
        assertEquals("the new screen's width", 65535f, p.x)
    }
}
