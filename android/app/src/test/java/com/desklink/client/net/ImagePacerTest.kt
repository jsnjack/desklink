package com.desklink.client.net

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ImagePacerTest {
    /** A WebSocket queue that OkHttp's writer drains by one part per poll. */
    private class FakeQueue {
        var queued = 0L
        var clock = 0L
        val queuedAtSend = mutableListOf<Long>()

        fun drain(ms: Long) {
            clock += ms
            queued = maxOf(0, queued - PART)
        }

        fun send(part: Int) {
            queuedAtSend += queued
            queued += PART
        }
    }

    @Test
    fun eachPartWaitsUntilTheQueueIsEmpty() {
        val q = FakeQueue()
        q.queued = 3 * PART // an earlier message still going out
        val pacer = ImagePacer(queued = { q.queued }, open = { true }, sleep = q::drain, now = { q.clock })
        assertTrue(pacer.send(listOf(0, 1, 2), q::send))
        assertEquals(listOf(0L, 0L, 0L), q.queuedAtSend)
    }

    @Test
    fun aMessageSentMeanwhileWaitsBehindAtMostOnePart() {
        val q = FakeQueue()
        var ahead = -1L
        val pacer = ImagePacer(queued = { q.queued }, open = { true }, sleep = q::drain, now = { q.clock })
        pacer.send(listOf(0, 1, 2)) { part ->
            q.send(part)
            if (part == 1) ahead = q.queued // text to type queued right after this part
        }
        assertEquals(PART, ahead)
    }

    @Test
    fun aConnectionThatChangesGetsNoMoreParts() {
        val q = FakeQueue()
        var open = true
        val pacer = ImagePacer(queued = { q.queued }, open = { open }, sleep = q::drain, now = { q.clock })
        val sent = mutableListOf<Int>()
        val done = pacer.send(listOf(0, 1, 2)) { part ->
            sent += part
            q.send(part)
            open = false // a reconnect replaced the connection
        }
        assertFalse(done)
        assertEquals(listOf(0), sent)
    }

    @Test
    fun aStalledQueueGivesTheImageUp() {
        var clock = 0L
        val pacer = ImagePacer(queued = { PART }, open = { true }, sleep = { clock += it }, now = { clock })
        val sent = mutableListOf<Int>()
        assertFalse(pacer.send(listOf(0, 1)) { sent += it })
        assertTrue(sent.isEmpty())
        assertTrue(clock >= ImagePacer.PART_WAIT_MS)
    }

    private companion object {
        const val PART = 700_000L
    }
}
