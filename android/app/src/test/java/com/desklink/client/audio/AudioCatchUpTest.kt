package com.desklink.client.audio

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class AudioCatchUpTest {
    private val policy = AudioCatchUp()

    /**
     * Plays 10 ms packets that arrive every 10 ms while the speaker takes
     * 10 ms each, from [queuedMs] queued at [startMs] for [forMs]; returns
     * the queue at the end and the times packets were dropped.
     */
    private fun steady(startMs: Long, queuedMs: Long, forMs: Long, silent: Boolean): Pair<Long, List<Long>> {
        var queue = queuedMs
        val drops = ArrayList<Long>()
        for (t in startMs until startMs + forMs step 10) {
            if (policy.play(t, queue, 10, silent)) queue += 10 else drops += t
            queue = (queue - 10).coerceAtLeast(0)
        }
        return queue to drops
    }

    @Test
    fun aQueueAtTheTargetPlaysEveryPacket() {
        val (queue, drops) = steady(0, 30, 10_000, silent = false)
        assertEquals(30L, queue)
        assertEquals(emptyList<Long>(), drops)
    }

    @Test
    fun afterAHiccupSilenceIsDroppedUntilTheQueueIsBackAtTheTarget() {
        val (queue, drops) = steady(0, 70, 2_000, silent = true)
        assertEquals(40L, queue)
        assertEquals("three packets of silence, once the queue stayed long for half a second", listOf(500L, 510L, 520L), drops)
    }

    @Test
    fun soundThatGoesOnIsCaughtUpSlowly() {
        val (queue, drops) = steady(0, 70, 5_000, silent = false)
        assertEquals(40L, queue)
        assertEquals("a 10 ms cut every quarter second, after two seconds", listOf(2_000L, 2_250L, 2_500L), drops)
    }

    @Test
    fun aBurstThatDrainsByItselfLosesNothing() {
        assertTrue(policy.play(0, 60, 10, silent = true))
        assertTrue(policy.play(300, 50, 10, silent = true))
        assertTrue(policy.play(400, 30, 10, silent = true))
        assertTrue("the wait starts again", policy.play(800, 50, 10, silent = true))
    }

    @Test
    fun moreThanTheMaximumIsDroppedAtOnce() {
        assertFalse(policy.play(0, 75, 10, silent = false))
        assertTrue(policy.play(10, 70, 10, silent = false))
    }

    @Test
    fun quietSamplesAreSilenceAndAnAudibleOneIsNot() {
        val quiet = byteArrayOf(10, 0, -10, -1, 63, 0, -63, -1)
        assertTrue(AudioCatchUp.isSilent(quiet, quiet.size))
        val audible = quiet + byteArrayOf(0, 1) // 256
        assertFalse(AudioCatchUp.isSilent(audible, audible.size))
        val negative = byteArrayOf(0, -1) // -256
        assertFalse(AudioCatchUp.isSilent(negative, negative.size))
        assertTrue("only the given size counts", AudioCatchUp.isSilent(audible, quiet.size))
    }
}
