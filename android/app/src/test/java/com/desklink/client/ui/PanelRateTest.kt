package com.desklink.client.ui

import org.junit.Assert.assertEquals
import org.junit.Test

class PanelRateTest {
    private val judge = PanelRateJudge()

    /** Samples every half second from [fromMs] to [toMs], with a touch at each sample when [touched]; returns the last shown rate. */
    private fun run(fromMs: Long, toMs: Long, s: PanelState, touched: Boolean): Int {
        var shown = 0
        for (t in fromMs..toMs step 500) shown = judge.sample(t, s, lastTouchMs = if (touched) t else Long.MIN_VALUE / 2)
        return shown
    }

    @Test
    fun aPanelHeldAt60WhileTouchedForThreeSecondsShows60() {
        assertEquals(120, run(0, 2_500, PanelState(60f, 120f, false), touched = true))
        assertEquals(60, run(3_000, 3_000, PanelState(60f, 120f, false), touched = true))
        assertEquals(60, judge.heldHz)
    }

    @Test
    fun anIdlePanelResting60IsNotJudged() {
        assertEquals(120, run(0, 10_000, PanelState(60f, 120f, false), touched = false))
        assertEquals(0, judge.heldHz)
    }

    @Test
    fun lowSamplesFarApartDoNotAddUp() {
        for (t in listOf(0L, 60_000L, 120_000L, 180_000L)) {
            assertEquals(120, judge.sample(t, PanelState(60f, 120f, false), lastTouchMs = t))
        }
    }

    @Test
    fun powerSavingNeedsNoTouch() {
        assertEquals(60, run(0, 3_000, PanelState(60f, 120f, true), touched = false))
    }

    @Test
    fun aPanelThatReachesItsHighestRateIsNotHeld() {
        run(0, 3_000, PanelState(60f, 120f, true), touched = false)
        assertEquals(120, judge.sample(3_500, PanelState(120f, 120f, true), 0))
        assertEquals("the wait starts again", 120, run(4_000, 6_000, PanelState(60f, 120f, true), touched = false))
    }

    @Test
    fun touchesThatBringTheHighestRateBetweenLowSamplesKeepItFree() {
        for (t in 0L..10_000L step 500) {
            val hz = if (t % 1_000 == 0L) 120f else 60f
            assertEquals("at $t ms", 120, judge.sample(t, PanelState(hz, 120f, false), lastTouchMs = t))
        }
    }

    @Test
    fun aPanelWhoseFastestModeIs60Shows60AtOnce() {
        // Samsung's Standard motion smoothness may hide the 120 Hz modes altogether.
        assertEquals(60, judge.sample(0, PanelState(60f, 60f, false), lastTouchMs = Long.MIN_VALUE / 2))
    }

    @Test
    fun ratesNearTheHighestModeCountAsIt() {
        assertEquals(120, run(0, 5_000, PanelState(119.88f, 120f, true), touched = true))
    }

    @Test
    fun theCapIsTheShownRateOnlyBelowTheStreamsRate() {
        assertEquals(60, PanelRateJudge.maxFps(shownHz = 60, streamFps = 120))
        assertEquals(0, PanelRateJudge.maxFps(shownHz = 60, streamFps = 60))
        assertEquals(0, PanelRateJudge.maxFps(shownHz = 120, streamFps = 120))
        assertEquals(90, PanelRateJudge.maxFps(shownHz = 90, streamFps = 120))
    }
}
