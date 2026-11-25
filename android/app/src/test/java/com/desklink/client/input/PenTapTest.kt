package com.desklink.client.input

import com.desklink.client.input.PenTapSteadier.Point
import org.junit.Assert.assertEquals
import org.junit.Test

class PenTapTest {
    private val pen = PenTapSteadier()

    @Test
    fun aPenThatWobblesAsItTapsClicksWhereItLanded() {
        assertEquals(Point(100f, 100f), pen.down(100f, 100f, 0))
        assertEquals(Point(100f, 100f), pen.move(104f, 97f, 30))
        assertEquals(Point(100f, 100f), pen.up(106f, 103f, 70))
    }

    @Test
    fun aStrokeThatLeavesTheCircleMovesFromThenOn() {
        pen.down(100f, 100f, 0)
        assertEquals(Point(125f, 100f), pen.move(125f, 100f, 20))
        // Back inside the circle it is not held again: drawing keeps its shape.
        assertEquals(Point(105f, 100f), pen.move(105f, 100f, 40))
    }

    @Test
    fun aPenHeldLongerThanATapMovesFreely() {
        pen.down(100f, 100f, 0)
        assertEquals(Point(100f, 100f), pen.move(103f, 100f, 100))
        assertEquals(Point(103f, 100f), pen.move(103f, 100f, 101))
    }

    @Test
    fun aQuickSecondTapNearbyLandsOnTheFirst() {
        pen.down(100f, 100f, 0)
        pen.up(102f, 101f, 60)
        assertEquals(Point(100f, 100f), pen.down(130f, 120f, 180))
        // It wobbles around where it really landed, and stays on the first tap.
        assertEquals(Point(100f, 100f), pen.move(140f, 120f, 200))
        assertEquals(Point(100f, 100f), pen.up(135f, 118f, 240))
    }

    @Test
    fun aSlowOrDistantSecondTapLandsWhereItTouches() {
        pen.down(100f, 100f, 0)
        pen.up(100f, 100f, 60)
        assertEquals("too late", Point(110f, 100f), pen.down(110f, 100f, 211))
        pen.up(110f, 100f, 260)
        assertEquals("too far", Point(170f, 100f), pen.down(170f, 100f, 300))
    }

    @Test
    fun theNextStrokeOfHandwritingStartsWhereItTouches() {
        pen.down(100f, 100f, 0)
        pen.move(140f, 100f, 30)
        pen.up(140f, 100f, 60)
        // Within 150 ms and 50 px of the lift, but the last touch was a stroke, not a tap.
        assertEquals(Point(150f, 110f), pen.down(150f, 110f, 120))
    }

    @Test
    fun aCancelledTouchIsNoFirstTap() {
        pen.down(100f, 100f, 0)
        pen.cancel()
        assertEquals(Point(105f, 100f), pen.down(105f, 100f, 50))
    }
}
