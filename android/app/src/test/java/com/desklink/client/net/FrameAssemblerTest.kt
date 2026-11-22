package com.desklink.client.net

import com.desklink.client.model.VideoFragment
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

private fun frag(frameId: Int, index: Int, count: Int, keyframe: Boolean, payload: String) =
    VideoFragment(frameId and 0xFFFF, index, count, keyframe, frameId.toLong(), payload.toByteArray(), 0, payload.length)

private fun FrameAssembler.feed(vararg fs: VideoFragment): List<Int> = fs.flatMap { add(it) }.map { it.frameId }

class FrameAssemblerTest {
    @Test
    fun waitsForAKeyframeBeforeDelivering() {
        val a = FrameAssembler()
        assertEquals(emptyList<Int>(), a.feed(frag(1, 0, 1, false, "p")))
        assertTrue(a.needKeyframe)
        assertEquals(listOf(2, 3), a.feed(frag(2, 0, 1, true, "k"), frag(3, 0, 1, false, "p")))
        assertFalse(a.needKeyframe)
    }

    @Test
    fun reassemblesFragmentsInIndexOrder() {
        val a = FrameAssembler()
        val out = listOf(frag(5, 2, 3, true, "c"), frag(5, 0, 3, true, "a"), frag(5, 1, 3, true, "b")).flatMap { a.add(it) }
        assertEquals(1, out.size)
        assertArrayEquals("abc".toByteArray(), out[0].data)
        assertTrue(out[0].keyframe)
    }

    @Test
    fun holdsALaterFrameUntilTheEarlierOneCompletes() {
        val a = FrameAssembler()
        a.feed(frag(1, 0, 1, true, "k"))
        assertEquals(emptyList<Int>(), a.feed(frag(2, 0, 2, false, "x"), frag(3, 0, 1, false, "p")))
        assertEquals(listOf(2, 3), a.feed(frag(2, 1, 2, false, "y")))
    }

    @Test
    fun declaresLossTwoFramesLaterAndRecoversAtTheNextKeyframe() {
        val a = FrameAssembler()
        a.feed(frag(1, 0, 1, true, "k"))
        a.feed(frag(2, 0, 2, false, "half"))
        a.feed(frag(3, 0, 1, false, "p"))
        assertFalse(a.needKeyframe)
        assertEquals(emptyList<Int>(), a.feed(frag(4, 0, 1, false, "p")))
        assertTrue("frame 2 is two frames old", a.needKeyframe)
        assertEquals(1, a.lastContiguousFrameId)
        assertEquals("predicted frames are dropped", emptyList<Int>(), a.feed(frag(5, 0, 1, false, "p")))
        assertEquals(listOf(6, 7), a.feed(frag(6, 0, 1, true, "k"), frag(7, 0, 1, false, "p")))
        assertFalse(a.needKeyframe)
    }

    @Test
    fun ignoresFragmentsOfDeliveredFrames() {
        val a = FrameAssembler()
        a.feed(frag(1, 0, 1, true, "k"), frag(2, 0, 1, false, "p"))
        assertEquals(emptyList<Int>(), a.feed(frag(1, 0, 1, true, "k")))
    }

    @Test
    fun frameIdsWrapAround() {
        val a = FrameAssembler()
        assertEquals(listOf(65534, 65535, 0, 1), a.feed(frag(65534, 0, 1, true, "k"), frag(65535, 0, 1, false, "p"), frag(0, 0, 1, false, "p"), frag(1, 0, 1, false, "p")))
    }

    @Test
    fun forcedKeyframeDropsUntilTheNextOne() {
        val a = FrameAssembler()
        a.feed(frag(1, 0, 1, true, "k"))
        a.forceKeyframe()
        assertEquals(emptyList<Int>(), a.feed(frag(2, 0, 1, false, "p")))
        assertEquals(listOf(3), a.feed(frag(3, 0, 1, true, "k")))
    }

    @Test
    fun distanceIsSignedOnTheCircle() {
        assertEquals(1, FrameAssembler.distance(0, 65535))
        assertEquals(-1, FrameAssembler.distance(65535, 0))
        assertEquals(10, FrameAssembler.distance(20, 10))
    }
}

class LossMeterTest {
    @Test
    fun noLossWhenEverySequenceNumberArrives() {
        val m = LossMeter()
        (1..10).forEach(m::onPacket)
        assertEquals(0, m.sample())
    }

    @Test
    fun countsGapsPerWindow() {
        val m = LossMeter()
        listOf(1, 2, 4, 5).forEach(m::onPacket)
        assertEquals(2000, m.sample())
        (6..9).forEach(m::onPacket)
        assertEquals("the next window starts clean", 0, m.sample())
    }

    @Test
    fun emptyWindowReportsZero() {
        val m = LossMeter()
        assertEquals(0, m.sample())
    }
}
