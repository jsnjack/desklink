package com.desklink.client.net

import org.junit.Assert.assertEquals
import org.junit.Test

/** The same cases as TestReplayWindow in host/internal/net/udp_test.go. */
class ReplayWindowTest {
    private fun accepts(vararg seqs: Long): List<Boolean> {
        val w = ReplayWindow()
        return seqs.map { w.accept(it.toInt()) }
    }

    @Test
    fun inOrderNumbersAreAccepted() = assertEquals(listOf(true, true, true), accepts(1, 2, 3))

    @Test
    fun aDuplicateIsRejected() = assertEquals(listOf(true, true, false), accepts(1, 2, 2))

    @Test
    fun numbersReorderedWithinTheWindowAreAccepted() =
        assertEquals(listOf(true, true, true, true, true), accepts(1, 5, 3, 4, 2))

    @Test
    fun aReorderedDuplicateIsRejected() = assertEquals(listOf(true, true, false), accepts(5, 3, 3))

    @Test
    fun aNumberOlderThanTheWindowIsRejected() =
        assertEquals(listOf(true, false), accepts(2000, 2000L - ReplayWindow.SIZE))

    @Test
    fun aJumpClearsTheWindow() {
        val far = 1L + 3 * ReplayWindow.SIZE
        assertEquals(listOf(true, true, true), accepts(1, far, far - 1))
    }

    @Test
    fun zeroIsNeverValid() = assertEquals(listOf(false), accepts(0))

    @Test
    fun numbersAboveTwoToThe31AreUnsigned() =
        assertEquals(listOf(true, true, false), accepts(0x7FFFFFFFL, 0x80000000L, 0x80000000L))
}
