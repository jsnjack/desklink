package com.desklink.client.video

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class DecodeTimingTest {
    @Test
    fun aFrameIsTimedFromArrivalToTheDecodersOutputByItsCaptureTime() {
        val clock = DecodeClock()
        clock.queued(1_000, 5_000_000)
        clock.queued(2_000, 6_000_000)
        assertEquals(3_000L, clock.decoded(1_000, 8_000_000))
        assertEquals(4_000L, clock.decoded(2_000, 10_000_000))
        assertNull("a frame comes out once", clock.decoded(2_000, 11_000_000))
    }

    @Test
    fun framesWithoutACaptureTimeAreNotTimed() {
        val clock = DecodeClock()
        clock.queued(0, 1_000)
        assertNull(clock.decoded(0, 2_000))
    }

    @Test
    fun framesTheDecoderDroppedAreForgotten() {
        val clock = DecodeClock(capacity = 2)
        clock.queued(1, 0)
        clock.queued(2, 0)
        clock.queued(3, 0)
        assertNull(clock.decoded(1, 1_000))
        assertEquals(1L, clock.decoded(3, 1_000))
    }

    @Test
    fun aSecondOfDecodeTimesGivesItsMeanAndNinetyFifthPercentile() {
        val meter = DecodeMeter()
        // 19 frames of 2 ms and one of 12 ms: the slow one is the top 5 %.
        repeat(19) { meter.add(2_000) }
        meter.add(12_000)
        assertEquals(DecodeTime(meanMicros = 2_500, p95Micros = 2_000, frames = 20), meter.take())
        repeat(18) { meter.add(2_000) }
        meter.add(12_000)
        meter.add(12_000)
        assertEquals(12_000L, meter.take()?.p95Micros)
        assertNull("each period starts empty", meter.take())
    }

    @Test
    fun theDecoderIsRatedByItsPerformancePointsBeforeItsMeasurements() {
        assertEquals(DecoderRating.RATED, DecoderCapability(true, pointCovers = true, measuredMaxFps = 30.0).rating(120))
        assertEquals(DecoderRating.NOT_RATED, DecoderCapability(true, pointCovers = false, measuredMaxFps = 240.0).rating(120))
    }

    @Test
    fun withoutPerformancePointsTheMeasuredRateDecides() {
        assertEquals(DecoderRating.RATED, DecoderCapability(true, null, 130.0).rating(120))
        assertEquals(DecoderRating.NOT_RATED, DecoderCapability(true, null, 90.0).rating(120))
        assertEquals(DecoderRating.UNKNOWN, DecoderCapability(true, null, null).rating(120))
    }

    @Test
    fun aSizeAndRateTheDecoderDoesNotSupportIsNeverRated() {
        assertEquals(DecoderRating.NOT_RATED, DecoderCapability(false, pointCovers = true, measuredMaxFps = 240.0).rating(120))
    }

    @Test
    fun theReportNamesTheOptionsTheDecoderRefused() {
        val r = DecoderReport("c2.qti.av1.decoder", setOf(DecoderOption.REALTIME), 2560, 1600, 120, DecoderRating.RATED, "", emptyList())
        assertEquals(listOf("vendor low latency", "low latency"), r.refused)
    }
}
