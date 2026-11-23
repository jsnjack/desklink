package com.desklink.client.net

import com.desklink.client.net.MediaWatchdog.Companion.CHECK_INTERVAL_MS
import com.desklink.client.net.MediaWatchdog.Companion.FIRST_PACKET_MS
import com.desklink.client.net.MediaWatchdog.Companion.SILENCE_MS
import com.desklink.client.net.MediaWatchdog.Verdict
import org.junit.Assert.assertEquals
import org.junit.Test

class MediaWatchdogTest {
    // Checks every CHECK_INTERVAL_MS from [from] up to and including [to], as the transport does.
    private fun MediaWatchdog.checkUntil(from: Long, to: Long): Verdict {
        var t = from
        var v = check(t)
        while (t < to) {
            t = minOf(t + CHECK_INTERVAL_MS, to)
            v = check(t)
        }
        return v
    }

    @Test
    fun waitsForTheFirstDatagramThenGivesUp() {
        val w = MediaWatchdog(startMs = 0)
        assertEquals(Verdict.WAITING, w.checkUntil(0, FIRST_PACKET_MS - 1))
        assertEquals(Verdict.NEVER_ARRIVED, w.checkUntil(FIRST_PACKET_MS - 1, FIRST_PACKET_MS))
    }

    @Test
    fun theFirstDatagramMakesThePathAlive() {
        val w = MediaWatchdog(startMs = 0)
        w.checkUntil(0, 1_000)
        w.onPacket(1_200)
        assertEquals(Verdict.ALIVE, w.check(1_500))
    }

    @Test
    fun keepalivesEveryTwoSecondsKeepItAlive() {
        val w = MediaWatchdog(startMs = 0)
        var t = 0L
        while (t < 60_000) {
            w.onPacket(t)
            assertEquals(Verdict.ALIVE, w.checkUntil(t, t + 2_000))
            t += 2_000
        }
    }

    @Test
    fun silenceAfterDatagramsLosesThePath() {
        val w = MediaWatchdog(startMs = 0)
        w.onPacket(100)
        assertEquals(Verdict.ALIVE, w.checkUntil(0, 100 + SILENCE_MS - 1))
        assertEquals(Verdict.LOST, w.checkUntil(100 + SILENCE_MS - 1, 100 + SILENCE_MS))
    }

    @Test
    fun anAppPauseDoesNotCountAsSilence() {
        val w = MediaWatchdog(startMs = 0)
        w.onPacket(100)
        w.checkUntil(0, 1_000)
        // The process was frozen for a minute: no check ran and nothing could be read.
        assertEquals(Verdict.ALIVE, w.check(61_000))
        assertEquals(Verdict.LOST, w.checkUntil(61_000, 61_000 + SILENCE_MS))
    }

    @Test
    fun anAppPauseBeforeTheFirstDatagramRestartsTheWait() {
        val w = MediaWatchdog(startMs = 0)
        w.check(500)
        assertEquals(Verdict.WAITING, w.check(30_000))
        assertEquals(Verdict.NEVER_ARRIVED, w.checkUntil(30_000, 30_000 + FIRST_PACKET_MS))
    }
}
