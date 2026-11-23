package com.desklink.client.net

import com.desklink.client.ui.formatBytes
import org.junit.Assert.assertEquals
import org.junit.Test

class LinkStatsTest {
    @Test
    fun qualityGradesRoundTripAndLoss() {
        assertEquals(LinkQuality.EXCELLENT, LinkStats(rttMs = 5, lossPct = 0.0).quality)
        assertEquals(LinkQuality.GOOD, LinkStats(rttMs = 40, lossPct = 0.0).quality)
        assertEquals(LinkQuality.GOOD, LinkStats(rttMs = 5, lossPct = 1.0).quality)
        assertEquals(LinkQuality.FAIR, LinkStats(rttMs = 90, lossPct = 0.0).quality)
        assertEquals(LinkQuality.FAIR, LinkStats(rttMs = 5, lossPct = 2.0).quality)
        assertEquals(LinkQuality.POOR, LinkStats(rttMs = 200, lossPct = 0.0).quality)
        assertEquals(LinkQuality.POOR, LinkStats(rttMs = 5, lossPct = 7.5).quality)
    }

    @Test
    fun bytesAreShownInTheLargestFittingUnit() {
        assertEquals("0 KB", formatBytes(512))
        assertEquals("900 KB", formatBytes(900L * 1024))
        assertEquals("1.5 MB", formatBytes(1536L * 1024))
        assertEquals("2.25 GB", formatBytes((2.25 * (1L shl 30)).toLong()))
    }
}
