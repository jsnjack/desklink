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
    private val layer = TileLayer(grid)
    private val plan = TilePlan(grid.count)

    // Frame n is presented at 1000 + n microseconds.
    private fun at(seq: Long) = 1000 + seq

    private fun frame(seq: Long, vararg changed: Int, all: Boolean = false, g: TileGrid = grid) =
        layer.onFrame(TileFrame(g, seq, at(seq), all, changed))

    private fun tile(seq: Long, slot: Int) = Tile(seq, slot, grid.x(slot), grid.y(slot), 64, 64, ByteArray(64 * 64 * 4))

    private fun present(seq: Long, maxUploads: Int = 64): TilePlan {
        layer.present(at(seq), plan, maxUploads)
        return plan
    }

    private fun uploaded() = (0 until plan.uploadCount).map { plan.uploads[it]!!.slot to plan.uploads[it]!!.seq }

    private fun cleared() = plan.clears.copyOf(plan.clearCount)

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
        frame(5)
        frame(7)
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
        frame(6)
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
        frame(8, 3)
        frame(9)
        present(9)
        assertArrayEquals(intArrayOf(3), cleared())
        assertFalse(plan.draw)
    }

    @Test
    fun aTileThatArrivesAfterItsPartChangedIsDiscarded() {
        frame(5)
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
        frame(7)
        frame(8, 0)
        present(8)
        assertEquals(0, plan.uploadCount)
    }

    @Test
    fun aNewerTileReplacesTheOldOne() {
        frame(5)
        layer.onTiles(listOf(tile(5, 1)))
        present(5)
        frame(8, 1)
        layer.onTiles(listOf(tile(8, 1)))
        present(8)
        assertArrayEquals(intArrayOf(1), cleared())
        assertEquals(listOf(1 to 8L), uploaded())
        assertTrue(plan.draw)
    }

    @Test
    fun aFrameThatInvalidatesEveryTileClearsTheTexture() {
        frame(5)
        layer.onTiles(listOf(tile(5, 0), tile(5, 7)))
        present(5)
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
}
