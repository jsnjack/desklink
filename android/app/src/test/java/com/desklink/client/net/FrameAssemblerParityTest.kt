package com.desklink.client.net

import com.desklink.client.model.ParityPacket
import com.desklink.client.model.VideoFragment
import com.desklink.client.model.Wire
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/** The same cases as TestFrameAssemblerParity in host/internal/client/client_test.go. */
class FrameAssemblerParityTest {
    private val capture = 4242L

    private fun dataOf(i: Int) = ('a' + i % 26).toString().repeat(10 + i * 7).toByteArray()

    private fun fragments(id: Int, n: Int, key: Boolean) =
        (0 until n).map { i -> val d = dataOf(i); VideoFragment(id, i, n, key, capture, d, 0, d.size) }

    // The host's ParityFor: fragment i joins group i mod stride.
    private fun parityFor(frags: List<VideoFragment>): List<ParityPacket> {
        val n = frags.size
        val stride = (n + 7) / 8
        return (0 until stride).map { g ->
            val members = (g until n step stride).map { i ->
                val f = frags[i]
                Wire.videoPayload(f.index, f.count, f.keyframe, f.captureMicros, f.data.copyOfRange(f.offset, f.offset + f.length))
            }
            val data = ByteArray(members.maxOf { it.size })
            var lenXor = 0
            for (m in members) {
                for (j in m.indices) data[j] = (data[j].toInt() xor m[j].toInt()).toByte()
                lenXor = lenXor xor m.size
            }
            ParityPacket(frags[0].frameId, g, stride, n, lenXor, data)
        }
    }

    private fun joined(frags: List<VideoFragment>) = frags.joinToString("") { String(it.data) }

    @Test
    fun aLostFragmentIsRebuiltFromItsGroup() {
        val a = FrameAssembler()
        val frags = fragments(1, 10, true)
        val out = frags.filterIndexed { i, _ -> i != 3 }.flatMap { a.add(it) } + parityFor(frags).flatMap { a.addParity(it) }
        assertEquals(1, out.size)
        assertEquals(joined(frags), String(out[0].data))
        assertTrue(out[0].keyframe)
        assertEquals(capture, out[0].captureMicros)
    }

    @Test
    fun aBurstAsLongAsTheStrideIsRebuilt() {
        val a = FrameAssembler()
        val frags = fragments(1, 10, true)
        val out = parityFor(frags).flatMap { a.addParity(it) } + frags.filterIndexed { i, _ -> i != 4 && i != 5 }.flatMap { a.add(it) }
        assertEquals(1, out.size)
        assertEquals(joined(frags), String(out[0].data))
    }

    @Test
    fun aSingleFragmentFrameComesBackFromParityAlone() {
        val a = FrameAssembler()
        val out = a.addParity(parityFor(fragments(1, 1, true))[0])
        assertEquals(1, out.size)
        assertTrue(out[0].keyframe)
        assertEquals(capture, out[0].captureMicros)
        assertEquals("aaaaaaaaaa", String(out[0].data))
    }

    @Test
    fun aRepeatedParityRecordIsKeptOnce() {
        val a = FrameAssembler()
        val frags = fragments(1, 10, true)
        val group0 = parityFor(frags)[0]
        repeat(3) { assertEquals(0, a.addParity(group0).size) }
        assertEquals(1, a.parityHeld(1))
        val out = frags.filterIndexed { i, _ -> i != 4 }.flatMap { a.add(it) }
        assertEquals(1, out.size)
        assertEquals(joined(frags), String(out[0].data))
    }

    @Test
    fun twoLossesInOneGroupStayLost() {
        val a = FrameAssembler()
        val frags = fragments(1, 10, true)
        val out = frags.filterIndexed { i, _ -> i != 2 && i != 4 }.flatMap { a.add(it) } + parityFor(frags).flatMap { a.addParity(it) }
        assertEquals(0, out.size)
    }
}
