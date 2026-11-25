package com.desklink.client.video

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class RenderBudgetTest {
    private val second = 1_000_000_000L
    private val quick = 1_000_000L
    private val slow = RenderBudget.DEFAULT_BUDGET_NANOS + 1

    /** Feeds [budget] [fps] frames a second for [seconds] seconds from [from], each taking [nanos]; returns the next frame's time. */
    private fun run(budget: RenderBudget, from: Long, seconds: Int, fps: Int, nanos: (Int) -> Long): Long {
        var t = from
        repeat(seconds * fps) { i ->
            budget.record(nanos(i), t)
            t += second / fps
        }
        return t
    }

    /** A budget past its warm-up, whose frames were far over budget, and the next frame's time. */
    private fun warmedUp(): Pair<RenderBudget, Long> {
        val b = RenderBudget(warmupFrames = 10)
        var t = 0L
        repeat(10) {
            b.record(slow * 10, t)
            t += second / 120
        }
        return b to t
    }

    @Test
    fun theWarmUpFramesAreNotJudged() {
        val b = RenderBudget(warmupFrames = 30)
        repeat(30) { assertNull(b.record(second, it * 1000L)) }
        assertFalse(b.exhausted)
    }

    @Test
    fun threeSlowSecondsInARowGiveUp() {
        val (b, t) = warmedUp()
        val end = run(b, t, 3, 120) { slow }
        b.record(quick, end + second)
        assertTrue(b.exhausted)
    }

    @Test
    fun twoSlowSecondsDoNotGiveUp() {
        val (b, t) = warmedUp()
        val end = run(b, t, 2, 120) { slow }
        b.record(quick, end + second)
        assertFalse(b.exhausted)
    }

    @Test
    fun aQuickSecondBetweenSlowOnesStartsTheCountAgain() {
        val (b, t0) = warmedUp()
        var t = run(b, t0, 2, 120) { slow }
        t = run(b, t, 1, 120) { quick }
        t = run(b, t, 2, 120) { slow }
        b.record(quick, t + second)
        assertFalse(b.exhausted)
    }

    @Test
    fun aSecondIsSlowOnlyWhenMostOfItsFramesAre() {
        val (b, t) = warmedUp()
        // Every third frame over budget: spikes, not a renderer that cannot keep up.
        val end = run(b, t, 5, 120) { i -> if (i % 3 == 0) slow else quick }
        b.record(quick, end + second)
        assertFalse(b.exhausted)
    }

    @Test
    fun aStillDesktopsFewSlowFramesDoNotGiveUp() {
        val (b, t) = warmedUp()
        // A GPU woken from idle draws the odd frame slowly.
        val end = run(b, t, 5, 5) { slow }
        b.record(quick, end + second)
        assertFalse(b.exhausted)
    }

    @Test
    fun eachSecondIsSummarised() {
        val (b, t) = warmedUp()
        val end = run(b, t, 1, 120) { i -> if (i == 0) 3_000_000L else 1_000_000L }
        val s = checkNotNull(b.record(quick, end))
        assertEquals(120, s.frames)
        assertEquals(0, s.slowFrames)
        assertEquals(3000L, s.maxMicros)
        assertEquals((3_000_000L + 119 * 1_000_000L) / 120 / 1000, s.meanMicros)
    }
}
