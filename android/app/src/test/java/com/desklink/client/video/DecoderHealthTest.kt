package com.desklink.client.video

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test

class DecoderHealthTest {
    @Test
    fun aDecoderThatRefusesTheVendorKeysStartsWithoutThem() {
        val tried = ArrayList<Set<DecoderOption>>()
        val started = DecoderSetup.withFewerOptions(refused = { options, _ -> tried += options }) { options ->
            if (DecoderOption.VENDOR_LOW_LATENCY in options) throw IllegalArgumentException("unknown key")
            "codec"
        }
        assertEquals("codec", started.value)
        assertEquals(setOf(DecoderOption.LOW_LATENCY, DecoderOption.REALTIME), started.options)
        assertEquals(listOf(DecoderSetup.ATTEMPTS.first()), tried)
    }

    @Test
    fun eachRefusalDropsTheVendorKeysThenLowLatencyThenTheOperatingRate() {
        val tried = ArrayList<Set<DecoderOption>>()
        val started = DecoderSetup.withFewerOptions { options ->
            tried += options
            if (options.isNotEmpty()) throw IllegalStateException("configure failed")
            "codec"
        }
        assertEquals(emptySet<DecoderOption>(), started.options)
        assertEquals(
            listOf(
                setOf(DecoderOption.VENDOR_LOW_LATENCY, DecoderOption.LOW_LATENCY, DecoderOption.REALTIME),
                setOf(DecoderOption.LOW_LATENCY, DecoderOption.REALTIME),
                setOf(DecoderOption.REALTIME),
                emptySet(),
            ),
            tried,
        )
    }

    @Test
    fun whenNoSetupStartsTheLastFailureCarriesTheOthers() {
        var n = 0
        try {
            DecoderSetup.withFewerOptions<String> { throw IllegalStateException("attempt ${++n}") }
            fail("a decoder that never starts returned")
        } catch (e: IllegalStateException) {
            assertEquals("attempt 4", e.message)
            assertEquals(listOf("attempt 1", "attempt 2", "attempt 3"), e.suppressed.map { it.message })
        }
    }

    @Test
    fun aDecoderStillHeldElsewhereIsNotRetriedWithFewerOptions() {
        val tried = ArrayList<Set<DecoderOption>>()
        try {
            DecoderSetup.withFewerOptions<String>(optionsMayExplain = { it.message != "busy" }) { options ->
                tried += options
                throw IllegalStateException("busy")
            }
            fail("a busy decoder started")
        } catch (e: IllegalStateException) {
            assertEquals("busy", e.message)
        }
        assertEquals(listOf(DecoderSetup.ATTEMPTS.first()), tried)
    }

    @Test
    fun aDecoderThatKeepsTakingFramesIsNotHung() {
        val watch = HangWatch(limitMs = 5000)
        for (t in 0L..20_000L step 100) {
            assertFalse(watch.frameArrived(t))
            watch.inputTaken()
        }
    }

    @Test
    fun aDecoderThatTakesNothingForFiveSecondsWhileFramesArriveIsHung() {
        val watch = HangWatch(limitMs = 5000)
        watch.frameArrived(0)
        watch.inputTaken()
        assertFalse(watch.frameArrived(1000))
        assertFalse(watch.frameArrived(5999))
        assertTrue(watch.frameArrived(6000))
    }

    @Test
    fun aLateFirstFrameIsNoHang() {
        // The decoder waits for a keyframe that takes long to arrive; the wait counts from the frame.
        val watch = HangWatch(limitMs = 5000)
        assertFalse(watch.frameArrived(30_000))
    }

    @Test
    fun aDecoderThatKeepsFailingIsRebuiltTenTimesAndNoMore() {
        val limit = RebuildLimit(max = 10, windowMs = 300_000)
        repeat(10) { assertTrue("rebuild ${it + 1}", limit.allow(it * 1000L)) }
        assertFalse(limit.allow(10_000))
    }

    @Test
    fun rareFailuresInALongSessionAreAlwaysRebuilt() {
        val limit = RebuildLimit(max = 10, windowMs = 300_000)
        for (hour in 0L until 24) assertTrue(limit.allow(hour * 3_600_000))
    }
}
