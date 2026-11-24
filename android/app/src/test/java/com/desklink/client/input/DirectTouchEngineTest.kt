package com.desklink.client.input

import com.desklink.client.input.TouchGestureEngine.Action
import com.desklink.client.input.TouchGestureEngine.Point
import com.desklink.client.model.InputEvent
import com.desklink.client.model.InputKind
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class DirectTouchEngineTest {
    private val out = mutableListOf<InputEvent>()
    private var huds = 0
    private val engine = DirectTouchEngine({ out += it }, { huds++ }, slop = 10f)

    // A 1000 x 500 view, so view pixels map to ABS_MAX/1000 and ABS_MAX/500 units.
    private fun touch(a: Action, t: Long, vararg pts: Point) = engine.onTouch(a, pts.toList(), t, 1000, 500)
    private fun p(x: Float, y: Float) = Point(x, y)
    private fun at(x: Float, y: Float) =
        InputEvent.pointer((x / 1000 * InputKind.ABS_MAX).toInt(), (y / 500 * InputKind.ABS_MAX).toInt())
    private fun click(button: Int, x: Float, y: Float) =
        listOf(at(x, y), InputEvent.button(button, true), InputEvent.button(button, false))

    @Test
    fun tapClicksWhereTheFingerTouched() {
        touch(Action.DOWN, 0, p(500f, 250f))
        touch(Action.UP, 80)
        assertEquals(click(InputKind.BUTTON_LEFT, 500f, 250f), out)
    }

    @Test
    fun secondTapNearTheFirstReusesItsPositionForADoubleClick() {
        touch(Action.DOWN, 0, p(500f, 250f))
        touch(Action.UP, 60)
        touch(Action.DOWN, 150, p(506f, 253f))
        touch(Action.UP, 210)
        assertEquals(click(InputKind.BUTTON_LEFT, 500f, 250f) + click(InputKind.BUTTON_LEFT, 500f, 250f), out)
    }

    @Test
    fun pressHeldStillRightClicks() {
        touch(Action.DOWN, 0, p(200f, 100f))
        touch(Action.UP, DirectTouchEngine.LONG_PRESS_MS + 50)
        assertEquals(click(InputKind.BUTTON_RIGHT, 200f, 100f), out)
    }

    @Test
    fun oneFingerDragHoldsTheLeftButtonFromWhereItStarted() {
        touch(Action.DOWN, 0, p(100f, 100f))
        touch(Action.MOVE, 160, p(130f, 100f))
        touch(Action.MOVE, 180, p(160f, 120f))
        touch(Action.UP, 200)
        assertEquals(
            listOf(
                at(100f, 100f), InputEvent.button(InputKind.BUTTON_LEFT, true), at(130f, 100f),
                at(160f, 120f),
                at(160f, 120f), InputEvent.button(InputKind.BUTTON_LEFT, false),
            ),
            out,
        )
    }

    @Test
    fun smallJitterStaysATap() {
        touch(Action.DOWN, 0, p(100f, 100f))
        touch(Action.MOVE, 20, p(105f, 103f))
        touch(Action.UP, 60)
        assertEquals(click(InputKind.BUTTON_LEFT, 100f, 100f), out)
    }

    @Test
    fun twoFingersScrollTheWindowUnderThem() {
        touch(Action.DOWN, 0, p(100f, 100f))
        touch(Action.POINTER_DOWN, 10, p(100f, 100f), p(200f, 100f))
        touch(Action.MOVE, 20, p(100f, 120f), p(200f, 120f))
        touch(Action.MOVE, 30, p(100f, 130f), p(200f, 130f))
        touch(Action.POINTER_UP, 40, p(100f, 130f))
        touch(Action.UP, 50)
        // The pointer first moves to the fingers' centre, then the wheel follows them.
        assertEquals(at(150f, 120f), out.first())
        val wheel = out.drop(1).filter { it.kind == InputKind.SCROLL }.sumOf { it.y }
        assertEquals((30 * TouchGestureEngine.SCROLL_UNITS_PER_PX).toInt(), wheel)
        assertTrue(out.none { it.kind == InputKind.MOUSE_BUTTON })
    }

    @Test
    fun twoFingerTapRightClicks() {
        touch(Action.DOWN, 0, p(100f, 100f))
        touch(Action.POINTER_DOWN, 10, p(100f, 100f), p(200f, 100f))
        touch(Action.POINTER_UP, 80, p(100f, 100f))
        touch(Action.UP, 90)
        assertEquals(click(InputKind.BUTTON_RIGHT, 150f, 100f), out)
    }

    @Test
    fun threeFingerTapTogglesTheHud() {
        touch(Action.DOWN, 0, p(100f, 100f))
        touch(Action.POINTER_DOWN, 10, p(100f, 100f), p(200f, 100f))
        touch(Action.POINTER_DOWN, 20, p(100f, 100f), p(200f, 100f), p(300f, 100f))
        touch(Action.UP, 120)
        assertEquals(1, huds)
        assertTrue(out.isEmpty())
    }

    @Test
    fun cancelledDragReleasesTheButton() {
        touch(Action.DOWN, 0, p(100f, 100f))
        touch(Action.MOVE, 200, p(150f, 100f))
        out.clear()
        touch(Action.CANCEL, 30)
        assertEquals(listOf(at(150f, 100f), InputEvent.button(InputKind.BUTTON_LEFT, false)), out)
    }

    @Test
    fun aQuickSwipeIsStillADrag() {
        touch(Action.DOWN, 0, p(100f, 100f))
        touch(Action.MOVE, 20, p(130f, 100f))
        touch(Action.MOVE, 40, p(160f, 120f))
        touch(Action.UP, 60)
        assertEquals(
            listOf(
                at(100f, 100f), InputEvent.button(InputKind.BUTTON_LEFT, true),
                at(160f, 120f), InputEvent.button(InputKind.BUTTON_LEFT, false),
            ),
            out,
        )
    }

    // A two-finger scroll whose first finger moved before the second landed
    // used to start a left-button drag, selecting text on the page.
    @Test
    fun aSecondFingerLandingLateStillScrollsWithoutSelecting() {
        touch(Action.DOWN, 0, p(100f, 100f))
        touch(Action.MOVE, 40, p(100f, 130f))
        touch(Action.POINTER_DOWN, 70, p(100f, 135f), p(200f, 135f))
        touch(Action.MOVE, 90, p(100f, 160f), p(200f, 160f))
        touch(Action.MOVE, 110, p(100f, 190f), p(200f, 190f))
        touch(Action.POINTER_UP, 130, p(100f, 190f))
        touch(Action.UP, 140)
        assertTrue(out.none { it.kind == InputKind.MOUSE_BUTTON })
        val wheel = out.filter { it.kind == InputKind.SCROLL }.sumOf { it.y }
        assertEquals((55 * TouchGestureEngine.SCROLL_UNITS_PER_PX).toInt(), wheel)
    }

    @Test
    fun aSecondFingerDuringADragTurnsItIntoAScroll() {
        touch(Action.DOWN, 0, p(100f, 100f))
        touch(Action.MOVE, 200, p(100f, 130f))
        touch(Action.POINTER_DOWN, 300, p(100f, 130f), p(200f, 130f))
        touch(Action.MOVE, 320, p(100f, 160f), p(200f, 160f))
        touch(Action.UP, 340)
        val release = out.indexOf(InputEvent.button(InputKind.BUTTON_LEFT, false))
        val firstScroll = out.indexOfFirst { it.kind == InputKind.SCROLL }
        assertTrue("the drag is released", release >= 0)
        assertTrue("then the fingers scroll", firstScroll > release)
        assertEquals(1, out.count { it == InputEvent.button(InputKind.BUTTON_LEFT, false) })
    }
}
