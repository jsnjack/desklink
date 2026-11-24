package com.desklink.client.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class ChartScaleTest {
    @Test
    fun theScaleEndsAtARoundNumberAboveTheValues() {
        assertEquals(60.0, chartCeiling(listOf(58.0, 60.0), minimum = 30.0), 1e-9)
        assertEquals(120.0, chartCeiling(listOf(118.0), minimum = 30.0), 1e-9)
        assertEquals(40.0, chartCeiling(listOf(37.4), minimum = 1.0), 1e-9)
        assertEquals(250.0, chartCeiling(listOf(212.0), minimum = 50.0), 1e-9)
    }

    @Test
    fun aQuietMetricDoesNotFillTheChart() {
        assertEquals(2.0, chartCeiling(listOf(0.1, 0.3), minimum = 2.0), 1e-9)
        assertEquals(50.0, chartCeiling(emptyList(), minimum = 50.0), 1e-9)
    }

    @Test
    fun scaleLabels() {
        assertEquals("120", formatScale(1.2 * 100))
        assertEquals("2", formatScale(2.0))
        assertEquals("1.5", formatScale(1.5))
    }

    @Test
    fun theNewestSampleIsAtTheRightEdgeAndUnknownsAreGaps() {
        val points = chartPoints(listOf(30.0, null, 60.0), capacity = 60, width = 590f, height = 100f, ceiling = 60.0)
        assertEquals(ChartPoint(570f, 50f), points[0])
        assertNull(points[1])
        assertEquals(ChartPoint(590f, 0f), points[2])
    }

    @Test
    fun valuesBeyondTheScaleAreClamped() {
        val points = chartPoints(listOf(-1.0, 90.0), capacity = 2, width = 10f, height = 100f, ceiling = 60.0)
        assertEquals(100f, points[0]!!.y)
        assertEquals(0f, points[1]!!.y)
    }
}
