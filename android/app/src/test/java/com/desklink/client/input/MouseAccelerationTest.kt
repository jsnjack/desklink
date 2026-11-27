package com.desklink.client.input

import com.desklink.client.model.MouseInfo
import org.junit.Assert.assertEquals
import org.junit.Test

class MouseAccelerationTest {
    /**
     * How many pixels a count moves once the mouse moves steadily, [counts]
     * every [intervalUs]: the factor of the third report, when libinput's
     * last velocity equals the current one.
     */
    private fun steadyFactor(counts: Float, intervalUs: Long, mouse: MouseInfo?): Float {
        val a = MouseAcceleration()
        var t = 5_000_000_000L
        var out = 0f
        repeat(3) {
            out = a.accelerate(counts, 0f, t, mouse).first
            t += intervalUs * 1000
        }
        return out / counts
    }

    // libinput's units are counts at 1000 dpi; its curve is in units per ms.
    // A tracker's time span is one microsecond longer than the gap.
    private fun perMs(counts: Float, intervalUs: Long) = counts * 1000.0 / (intervalUs + 1)

    @Test
    fun aSteadyMediumMotionIsSpedUpOnLibinputsIncline() {
        // 1 unit/ms: 1.1 * (1 - 0.4) + 1.
        assertEquals(1.0 + 1.1 * (perMs(1f, 1000) - 0.4), steadyFactor(1f, 1000, null).toDouble(), 1e-4)
    }

    @Test
    fun aFastFlickReachesTwiceTheMotion() {
        assertEquals(2f, steadyFactor(8f, 1000, null), 1e-6f)
    }

    @Test
    fun ordinaryPointingMovesOnePixelPerCount() {
        // 0.2 units/ms lies between 0.07 and the 0.4 threshold.
        assertEquals(1f, steadyFactor(1f, 5000, null), 1e-6f)
    }

    @Test
    fun slowPrecisePointingIsSlowedDown() {
        // 0.05 units/ms: 10 * 0.05 + 0.3.
        assertEquals(10 * perMs(1f, 20_000) + 0.3, steadyFactor(1f, 20_000, null).toDouble(), 1e-4)
    }

    @Test
    fun theDesksScaleTurnsLogicalPixelsIntoTheClientsPixels() {
        val scale = 5.0 / 3
        assertEquals(scale.toFloat(), steadyFactor(1f, 5000, MouseInfo(scale = scale)), 1e-5f)
        assertEquals(2 * scale.toFloat(), steadyFactor(8f, 1000, MouseInfo(scale = scale)), 1e-5f)
    }

    // The desk where this was written has GNOME's mouse speed at -0.296:
    // acceleration starts later and stops at 1.56 times.
    @Test
    fun theDesksSpeedSettingShapesTheCurve() {
        val speed = -0.29565217391304344
        val mouse = MouseInfo(speed = speed)
        assertEquals((2 + 1.5 * speed).toFloat(), steadyFactor(8f, 1000, mouse), 1e-5f)
        val threshold = 0.4 - 0.25 * speed
        assertEquals(1f, steadyFactor(9f, 20_000, mouse), 1e-6f) // 0.45 units/ms, under the threshold
        assertEquals(1.0 + (1.1 + 0.75 * speed) * (perMs(1f, 1000) - threshold), steadyFactor(1f, 1000, mouse).toDouble(), 1e-4)
    }

    @Test
    fun theFlatProfileMultipliesBySpeedPlusOne() {
        val mouse = MouseInfo(speed = 0.5, flat = true, scale = 2.0)
        assertEquals(3f, steadyFactor(1f, 20_000, mouse), 1e-6f)
        assertEquals(3f, steadyFactor(8f, 1000, mouse), 1e-6f)
    }

    @Test
    fun theFirstMotionAfterAPauseCountsAsSlow() {
        val a = MouseAcceleration()
        // The first motion: libinput's velocity over its one-second timeout,
        // averaged with no motion before it.
        val first = a.accelerate(4f, 0f, 1_000_000_000L, null).first / 4
        val v = perMs(4f, 1_000_000)
        assertEquals((10 * v + 0.3 + 0.3 + 4 * (10 * v / 2 + 0.3)) / 6, first.toDouble(), 1e-4)
        // Two seconds later it is slow again, now averaged with the same slow
        // velocity before it.
        assertEquals(10 * v + 0.3, a.accelerate(4f, 0f, 3_000_000_000L, null).first / 4.0, 1e-4)
    }

    @Test
    fun aButtonWithoutMotionLeavesTheVelocityAlone() {
        val a = MouseAcceleration()
        var t = 5_000_000_000L
        repeat(3) {
            a.accelerate(8f, 0f, t, null)
            t += 1_000_000
        }
        assertEquals(0f to 0f, a.accelerate(0f, 0f, t, null))
        assertEquals(16f, a.accelerate(8f, 0f, t, null).first, 1e-3f)
    }

    @Test
    fun motionKeepsItsDirection() {
        val a = MouseAcceleration()
        var t = 5_000_000_000L
        var out = 0f to 0f
        repeat(3) {
            out = a.accelerate(-3f, 4f, t, null)
            t += 1_000_000
        }
        assertEquals(-6f, out.first, 1e-4f)
        assertEquals(8f, out.second, 1e-4f)
    }
}
