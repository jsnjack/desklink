package com.desklink.client.net

import org.junit.Assert.assertEquals
import org.junit.Test

class StatsHistoryTest {
    @Test
    fun keepsTheLastMinuteOldestFirst() {
        var h = StatsHistory()
        for (fps in 1..75) h += LinkStats(fps = fps)
        assertEquals(StatsHistory.CAPACITY, h.samples.size)
        assertEquals(16, h.samples.first().fps)
        assertEquals(75, h.samples.last().fps)
    }

    @Test
    fun growsUntilFull() {
        val h = StatsHistory() + LinkStats(fps = 60) + LinkStats(fps = 59)
        assertEquals(listOf(60, 59), h.samples.map { it.fps })
    }
}
