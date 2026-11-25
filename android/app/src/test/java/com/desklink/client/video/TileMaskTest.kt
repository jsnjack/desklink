package com.desklink.client.video

import com.desklink.client.model.Tile
import com.desklink.client.model.TileGrid
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import kotlin.math.floor

class TileMaskTest {
    // Four columns and three rows of tiles; slot 5 is in the middle row, second column.
    private val grid = TileGrid(256, 192)
    private val mask = TileMask(grid)
    private val plan = TilePlan(grid.count)
    private val fade = TileMask.FADE_NANOS
    private val start = 1_000_000_000L

    private fun show(at: Long, vararg slots: Int) {
        plan.reset()
        for (s in slots) plan.uploads[plan.uploadCount++] = Tile(1, s, grid.x(s), grid.y(s), 64, 64, ByteArray(0))
        mask.apply(plan, at)
    }

    private fun clear(vararg slots: Int) {
        plan.reset()
        for (s in slots) plan.clears[plan.clearCount++] = s
        mask.apply(plan, start)
    }

    /** The tile's opacity at the centre of stream pixel [x], [y], as GL_LINEAR samples the mask there. */
    private fun opacity(x: Int, y: Int): Double {
        val u = (x + 0.5) / TileMask.BLOCK - 0.5
        val v = (y + 0.5) / TileMask.BLOCK - 0.5
        val i = floor(u).toInt()
        val j = floor(v).toInt()
        val fu = u - i
        val fv = v - j
        fun at(bx: Int, by: Int) =
            (mask.alpha[by.coerceIn(0, mask.height - 1) * mask.width + bx.coerceIn(0, mask.width - 1)].toInt() and 0xff) / 255.0
        val top = at(i, j) * (1 - fu) + at(i + 1, j) * fu
        val bottom = at(i, j + 1) * (1 - fu) + at(i + 1, j + 1) * fu
        return top * (1 - fv) + bottom * fv
    }

    private fun shown(vararg slots: Int) {
        show(start, *slots)
        mask.update(start + fade)
    }

    @Test
    fun aTileNextToVideoBlendsIntoItAtItsOuterEdge() {
        shown(5)
        val y = 96
        // Tile 5 spans x 64 until 128; its neighbours show video.
        for (x in 64 until 68) assertEquals("the outer pixels at x $x show the video", 0.0, opacity(x, y), 1e-9)
        for (x in 68 until 76) {
            assertTrue("x $x blends", opacity(x, y) > opacity(x - 1, y))
        }
        for (x in 76 until 116) assertEquals("x $x shows the tile", 1.0, opacity(x, y), 1e-9)
        for (x in 124 until 128) assertEquals("the outer pixels at x $x show the video", 0.0, opacity(x, y), 1e-9)
        assertEquals("the middle of the blend", 0.5, opacity(72, y), 0.07)
    }

    @Test
    fun twoAdjacentDrawnTilesHaveNoBlendBetweenThem() {
        shown(5, 6)
        for (x in 76 until 180) assertEquals("x $x across the shared edge at 128", 1.0, opacity(x, 96), 1e-9)
    }

    @Test
    fun aTileAtTheStreamsEdgeDoesNotBlendTowardIt() {
        shown(0)
        assertEquals(1.0, opacity(0, 0), 1e-9)
        assertEquals(1.0, opacity(32, 0), 1e-9)
        assertEquals(1.0, opacity(0, 32), 1e-9)
        assertEquals("toward tile 1, which shows video", 0.0, opacity(63, 32), 1e-9)
    }

    @Test
    fun theCornerOfATileNextToVideoOnlyDiagonallyBlendsToo() {
        // Tiles 0, 1 and 4 drawn around tile 5, which shows video.
        shown(0, 1, 4)
        assertEquals("tile 0's corner at tile 5", 0.0, opacity(63, 63), 1e-9)
        assertEquals("tile 1's side along tile 5", 0.0, opacity(96, 63), 1e-9)
        assertEquals("tile 4's side along tile 5", 0.0, opacity(63, 96), 1e-9)
        assertEquals("tile 0 away from its corner", 1.0, opacity(40, 60), 1e-9)
        assertEquals("between tiles 0 and 1", 1.0, opacity(64, 30), 1e-9)
    }

    @Test
    fun aNewTileFadesInOver120Milliseconds() {
        show(start, 5)
        assertTrue(mask.update(start))
        assertEquals("when first drawn", 0.0, opacity(96, 96), 1e-9)
        assertTrue(mask.update(start + fade / 2))
        assertEquals("half way", 0.5, opacity(96, 96), 0.01)
        assertTrue(mask.update(start + fade * 3 / 4))
        assertTrue(opacity(96, 96) > 0.8)
        assertFalse("done fading", mask.update(start + fade))
        assertEquals(1.0, opacity(96, 96), 1e-9)
    }

    @Test
    fun twoTilesFadingInTogetherHaveNoDipBetweenThem() {
        show(start, 5, 6)
        mask.update(start + fade / 2)
        val inside = opacity(96, 96)
        for (x in 96 until 160) assertEquals("x $x", inside, opacity(x, 96), 1e-9)
    }

    @Test
    fun aDrawnTileNextToOneFadingInBlendsWithIt() {
        shown(5)
        show(start + fade, 6)
        mask.update(start + fade + fade / 2)
        assertEquals("tile 5's side along tile 6 is as opaque as tile 6", opacity(150, 96), opacity(127, 96), 0.01)
        assertEquals(1.0, opacity(96, 96), 1e-9)
    }

    @Test
    fun anInvalidatedTileDisappearsAtOnce() {
        shown(5, 6)
        clear(6)
        assertFalse("nothing fades out", mask.update(start + fade + 1))
        for (x in 128 until 192) assertEquals("x $x in the cleared tile", 0.0, opacity(x, 96), 1e-9)
        assertEquals("tile 5 now blends toward it", 0.0, opacity(127, 96), 1e-9)
        assertEquals(1.0, opacity(96, 96), 1e-9)
    }

    @Test
    fun clearingEveryTileHidesThemAll() {
        shown(0, 5, 11)
        plan.reset()
        plan.clearAll = true
        mask.apply(plan, start + fade)
        mask.update(start + fade)
        assertTrue(mask.alpha.all { it.toInt() == 0 })
    }

    @Test
    fun aTileReplacedInTheSameFrameFadesInAgain() {
        shown(5)
        plan.reset()
        plan.clears[plan.clearCount++] = 5
        plan.uploads[plan.uploadCount++] = Tile(2, 5, 64, 64, 64, 64, ByteArray(0))
        mask.apply(plan, start + fade)
        assertTrue(mask.update(start + fade))
        assertEquals(0.0, opacity(96, 96), 1e-9)
    }

    @Test
    fun onlyTheChangedTileAndItsNeighboursArePainted() {
        shown(0)
        show(start + fade, 10)
        mask.update(start + fade + fade / 2)
        // Tile 10 is column 2, row 2: columns 1 to 3, rows 1 to 2, in blocks of 8 per tile.
        assertEquals(listOf(8, 8, 32, 24), listOf(mask.dirtyLeft, mask.dirtyTop, mask.dirtyRight, mask.dirtyBottom))
        mask.update(start + fade * 3)
        assertTrue("the fade's last step", mask.dirty)
        mask.update(start + fade * 4)
        assertFalse("nothing changed", mask.dirty)
    }

    @Test
    fun aCutTileAtTheStreamsCornerBlendsLikeAnyOther() {
        // 200×100: the last column of tiles is 8 pixels wide, the last row 36 high.
        val cut = TileMask(TileGrid(200, 100))
        assertEquals(25, cut.width)
        assertEquals(13, cut.height)
        val p = TilePlan(cut.grid.count)
        for (slot in listOf(6, 7)) p.uploads[p.uploadCount++] = Tile(1, slot, cut.grid.x(slot), cut.grid.y(slot), 64, 36, ByteArray(0))
        cut.apply(p, start)
        cut.update(start + fade)
        fun block(x: Int, y: Int) = cut.alpha[y * cut.width + x].toInt() and 0xff
        assertEquals("the corner block, beside drawn tile 6", 255, block(24, 12))
        assertEquals("its top, toward tile 3, which shows video", 0, block(24, 8))
        assertEquals("tile 6 inside", 255, block(20, 11))
    }

    @Test
    fun resetForgetsEveryTile() {
        shown(5)
        mask.reset()
        assertTrue(mask.alpha.all { it.toInt() == 0 })
        assertFalse(mask.update(start + fade * 2))
        assertFalse(mask.dirty)
    }
}
