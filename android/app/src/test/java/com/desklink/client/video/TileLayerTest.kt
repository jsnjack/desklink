package com.desklink.client.video

import com.desklink.client.model.Tile
import com.desklink.client.model.TileFrame
import com.desklink.client.model.TileGrid
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class TileLayerTest {
    private val grid = TileGrid(256, 128)
    private var startOvers = 0
    private var hidden = 0
    private val layer = TileLayer(grid, onStartOver = { startOvers++ }, onHidden = { hidden++ })
    private val plan = TilePlan(grid.count)

    // Frame n is presented at 1000 + n microseconds.
    private fun at(seq: Long) = 1000 + seq

    private fun inv(seq: Long, vararg changed: Int, all: Boolean = false, g: TileGrid = grid) = TileFrame(g, seq, at(seq), all, changed)

    /** A tile_frame message on the WebSocket, as older hosts send them. */
    private fun frame(seq: Long, vararg changed: Int, all: Boolean = false, g: TileGrid = grid) = layer.onFrame(inv(seq, *changed, all = all, g = g))

    private fun frames(seqs: LongRange) = seqs.forEach { frame(it) }

    /** A UDP packet: its frame's invalidation, then the ones before it. */
    private fun packet(vararg records: TileFrame) = layer.onFrames(records.toList())

    private fun tile(seq: Long, slot: Int) = Tile(seq, slot, grid.x(slot), grid.y(slot), 64, 64, ByteArray(64 * 64 * 4))

    private fun present(seq: Long, maxUploads: Int = 64): TilePlan {
        layer.present(at(seq), plan, maxUploads)
        return plan
    }

    private fun uploaded() = (0 until plan.uploadCount).map { plan.uploads[it]!!.slot to plan.uploads[it]!!.seq }

    private fun cleared() = plan.clears.copyOf(plan.clearCount)

    // Whether the plan takes the tile in slot away: alone, or with the whole texture.
    private fun goes(slot: Int) = plan.clearAll || slot in cleared()

    @Test
    fun aTileIsDrawnFromItsFrameOn() {
        frame(5)
        layer.onTiles(listOf(tile(5, 2)))
        present(5)
        assertEquals(listOf(2 to 5L), uploaded())
        assertTrue(plan.draw)
        frame(6)
        present(6)
        assertEquals("a tile is uploaded once", 0, plan.uploadCount)
        assertTrue(plan.draw)
    }

    @Test
    fun aTileWaitsUntilItsFrameIsPresented() {
        frames(5L..7L)
        layer.onTiles(listOf(tile(7, 0)))
        present(5)
        assertFalse(plan.draw)
        assertEquals(0, plan.uploadCount)
        present(7)
        assertEquals(listOf(0 to 7L), uploaded())
    }

    @Test
    fun aFrameWhoseTileFrameHasNotArrivedShowsNoTiles() {
        frame(5)
        layer.onTiles(listOf(tile(5, 1)))
        present(5)
        assertTrue(plan.draw)
        present(6)
        assertFalse("tiles were drawn over a frame nobody checked", plan.draw)
        assertTrue("the late tile_frame does not ask to draw the frame again", frame(6))
        present(6)
        assertTrue(plan.draw)
    }

    @Test
    fun aTileGoesWithTheFrameThatChangesItsPart() {
        frame(5)
        layer.onTiles(listOf(tile(5, 3), tile(5, 4)))
        present(5)
        frames(6L..7L)
        frame(8, 3)
        present(6)
        assertTrue("the tile went before the frame that changes it", plan.draw)
        assertEquals(0, plan.clearCount)
        present(8)
        assertArrayEquals(intArrayOf(3), cleared())
        assertTrue("the other tile stays", plan.draw)
    }

    @Test
    fun aTileGoesEvenWhenTheFrameThatChangedItWasNeverPresented() {
        frame(5)
        layer.onTiles(listOf(tile(5, 3)))
        present(5)
        frames(6L..7L)
        frame(8, 3)
        frame(9)
        present(9)
        assertTrue(goes(3))
        assertFalse(plan.draw)
    }

    @Test
    fun aTileThatArrivesAfterItsPartChangedIsDiscarded() {
        frames(5L..7L)
        frame(8, 0)
        layer.onTiles(listOf(tile(5, 0)))
        present(8)
        assertEquals(0, plan.uploadCount)
        assertFalse(plan.draw)
    }

    @Test
    fun aWaitingTileIsDroppedWhenItsPartChanges() {
        frame(5)
        layer.onTiles(listOf(tile(7, 0)))
        frames(6L..7L)
        frame(8, 0)
        present(8)
        assertEquals(0, plan.uploadCount)
    }

    @Test
    fun aNewerTileReplacesTheOldOne() {
        frame(5)
        layer.onTiles(listOf(tile(5, 1)))
        present(5)
        frames(6L..7L)
        frame(8, 1)
        layer.onTiles(listOf(tile(8, 1)))
        present(8)
        assertTrue(goes(1))
        assertEquals(listOf(1 to 8L), uploaded())
        assertTrue(plan.draw)
    }

    @Test
    fun aFrameThatInvalidatesEveryTileClearsTheTexture() {
        frame(5)
        layer.onTiles(listOf(tile(5, 0), tile(5, 7)))
        present(5)
        // The tile_frames between were dropped when the host's queue overflowed.
        frame(10, all = true)
        layer.onTiles(listOf(tile(6, 2)))
        present(10)
        assertTrue(plan.clearAll)
        assertFalse(plan.draw)
        assertEquals("a tile from before the reset was kept", 0, plan.uploadCount)
    }

    @Test
    fun aTileFromTheFrameThatInvalidatedEveryTileSurvivesIt() {
        frame(10, all = true)
        layer.onTiles(listOf(tile(10, 2)))
        present(10)
        assertEquals(listOf(2 to 10L), uploaded())
        assertTrue(plan.draw)
    }

    @Test
    fun tilesForAFrameAlreadyPresentedAskToDrawItAgain() {
        frame(5)
        present(5)
        assertTrue(layer.onTiles(listOf(tile(5, 0))))
        assertFalse(layer.onTiles(listOf(tile(9, 1))))
    }

    @Test
    fun uploadsAreSpreadOverFrames() {
        val g = TileGrid(64 * 10, 64 * 8)
        val l = TileLayer(g)
        val p = TilePlan(g.count)
        l.onFrame(TileFrame(g, 1, 1, false, IntArray(0)))
        l.onTiles((0 until g.count).map { Tile(1, it, g.x(it), g.y(it), 64, 64, ByteArray(0)) })
        l.present(1, p, 64)
        assertEquals(64, p.uploadCount)
        assertTrue(p.more)
        l.present(1, p, 64)
        assertEquals(16, p.uploadCount)
        assertFalse(p.more)
    }

    @Test
    fun aLostTextureDropsTheDrawnTilesButKeepsWhatTheHostSaid() {
        frame(5)
        layer.onTiles(listOf(tile(5, 0)))
        present(5)
        assertTrue("tiles were drawn, so the host must send them again", layer.textureLost())
        assertFalse(layer.textureLost())
        frames(6L..7L)
        frame(8, 1)
        layer.onTiles(listOf(tile(5, 1)))
        present(8)
        assertEquals("a stale tile was taken after the texture went", 0, plan.uploadCount)
        assertFalse(plan.draw)
    }

    @Test
    fun messagesForAnotherStreamSizeAreIgnored() {
        frame(5, all = true, g = TileGrid(128, 64))
        present(5)
        assertFalse(plan.draw)
        assertFalse(plan.clearAll)
    }

    @Test
    fun aFrameOlderThanTheHistoryShowsNoTiles() {
        val l = TileLayer(grid, historySize = 2)
        for (seq in 1L..3L) l.onFrame(TileFrame(grid, seq, at(seq), false, IntArray(0)))
        l.onTiles(listOf(tile(1, 0)))
        l.present(at(1), plan)
        assertFalse(plan.draw)
        l.present(at(3), plan)
        assertTrue(plan.draw)
    }

    @Test
    fun anOldHostsWebSocketTileFrameStillWorks() {
        // The host's first tile_frames, with the one for every tile, came
        // before the session message that made this layer.
        frames(40L..41L)
        layer.onTiles(listOf(tile(38, 0)))
        present(41)
        assertEquals(listOf(0 to 38L), uploaded())
        assertTrue(plan.draw)
        assertEquals(0, startOvers)
    }

    @Test
    fun overUdpTilesAreDrawnOnlyFromAnInvalidationOfEveryTileOn() {
        packet(inv(5))
        layer.onTiles(listOf(tile(5, 0)))
        present(5)
        assertFalse("a tile was drawn before the host said which tiles were valid", plan.draw)
        packet(inv(6, all = true), inv(5))
        layer.onTiles(listOf(tile(6, 1)))
        present(6)
        assertEquals(listOf(1 to 6L), uploaded())
    }

    @Test
    fun anInvalidationArrivingAfterTheTilesDataStillRemovesIt() {
        packet(inv(5, all = true))
        packet(inv(6), inv(5, all = true))
        layer.onTiles(listOf(tile(6, 3), tile(6, 4)))
        present(6)
        assertEquals(listOf(3 to 6L, 4 to 6L), uploaded())
        packet(inv(7, 3), inv(6), inv(5, all = true))
        present(7)
        assertArrayEquals(intArrayOf(3), cleared())
        assertTrue(plan.draw)
    }

    @Test
    fun tileDataOlderThanTheNewestInvalidationOfItsTileIsDiscarded() {
        packet(inv(5, all = true))
        packet(inv(6))
        packet(inv(7, 3))
        layer.onTiles(listOf(tile(6, 3), tile(6, 4)))
        present(7)
        assertEquals(listOf(4 to 6L), uploaded())
    }

    @Test
    fun aFrameWhoseInvalidationWasLostHidesTiles() {
        packet(inv(5, all = true))
        layer.onTiles(listOf(tile(5, 0)))
        present(5)
        assertTrue(plan.draw)
        present(6)
        assertFalse("tiles were drawn over a frame whose invalidation never came", plan.draw)
        present(6) // drawn again, at the next refresh
        assertEquals("one frame hid the tiles", 1, hidden)
        present(5)
        assertTrue(plan.draw)
    }

    @Test
    fun aLostInvalidationArrivesWithTheNextFramesPacket() {
        packet(inv(5, all = true))
        layer.onTiles(listOf(tile(5, 0), tile(5, 1)))
        present(5)
        // Frame 6's packet was lost; frame 7's repeats it.
        packet(inv(7), inv(6, 0), inv(5, all = true))
        present(7)
        assertArrayEquals(intArrayOf(0), cleared())
        assertTrue("the other tile stays", plan.draw)
        assertEquals(0, startOvers)
    }

    @Test
    fun aRepeatedInvalidationChangesNothing() {
        packet(inv(5, all = true))
        layer.onTiles(listOf(tile(5, 1)))
        present(5)
        packet(inv(6), inv(5, all = true))
        packet(inv(6), inv(5, all = true))
        present(6)
        assertTrue(plan.draw)
        assertEquals(0, plan.clearCount)
    }

    @Test
    fun aLateCopyOfAnOlderInvalidationDoesNotKeepATilePastTheFrameThatChangedIt() {
        packet(inv(5, all = true))
        layer.onTiles(listOf(tile(5, 1), tile(5, 2)))
        present(5)
        packet(inv(6, 1))
        layer.onTiles(listOf(tile(6, 1)))
        present(6)
        assertEquals(listOf(1 to 6L), uploaded())
        packet(inv(8, 1), inv(7), inv(6, 1))
        // The copy sent after frame 6's fragments arrives last.
        packet(inv(6, 1))
        present(7)
        assertTrue(plan.draw)
        present(8)
        assertArrayEquals("frame 8 changed the tile frame 6 brought", intArrayOf(1), cleared())
    }

    @Test
    fun anInvalidationThatNeverArrivesDropsOlderTilesAndAsksTheHostToStartOver() {
        packet(inv(5, all = true))
        layer.onTiles(listOf(tile(5, 0)))
        present(5)
        // Frame 6's packet and the three after it, which repeat it, were lost.
        val last = 7L + TileLayer.LATE_FRAMES + 3
        for (seq in 10L..last) packet(inv(seq))
        present(last)
        assertTrue("a tile from before the lost invalidation was kept", plan.clearAll)
        assertFalse(plan.draw)
        assertEquals(1, startOvers)
        layer.onTiles(listOf(tile(10, 2)))
        present(last)
        assertEquals("a tile from after it is drawn", listOf(2 to 10L), uploaded())
        packet(inv(last + 1))
        assertEquals("asked once until the host starts over", 1, startOvers)
    }

    @Test
    fun waitingTooLongForTheFirstInvalidationOfEveryTileAsksTheHostToStartOver() {
        for (seq in 1L..TileLayer.LATE_FRAMES.toLong()) packet(inv(seq))
        assertEquals(0, startOvers)
        packet(inv(TileLayer.LATE_FRAMES + 1L))
        assertEquals(1, startOvers)
        packet(inv(20, all = true))
        layer.onTiles(listOf(tile(20, 0)))
        present(20)
        assertTrue(plan.draw)
    }

    @Test
    fun aLateInvalidationAsksToDrawTheFrameAgain() {
        packet(inv(5, all = true))
        layer.onTiles(listOf(tile(5, 0)))
        present(5)
        present(6)
        assertFalse(plan.draw)
        assertTrue(packet(inv(6), inv(5, all = true)))
        present(6)
        assertTrue(plan.draw)
    }
}
