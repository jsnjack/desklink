package com.desklink.client.video

import com.desklink.client.model.TileGrid
import com.desklink.client.model.TileWire
import kotlin.math.min

/**
 * How opaque the exact tiles are over the video, one value for every
 * [BLOCK] × [BLOCK] block of the stream: the overlay's mask texture, which
 * [TileOverlay] samples with GL_LINEAR between block centres and multiplies
 * the tiles by. It decides without GL, so TileMaskTest covers it.
 *
 * Where the video's colours differ a little from the exact pixels, a tile
 * that appears or a hard 64-pixel edge between tile and video shows as a
 * patch. So a tile fades in over [fadeNanos] from the frame it is first
 * drawn over, and inside a drawn tile the blocks along a side or corner that
 * meets video (a tile not drawn, or still fading in) take that neighbour's
 * opacity: interpolated, the tile's outer 4 pixels show the video and the
 * next 8 blend into the tile. Between drawn tiles nothing blends, nor at the
 * stream's edge. A tile the plan clears (its frame changed it) goes at once,
 * never fading out over the pixels that changed.
 *
 * It runs on the render thread only.
 */
class TileMask(val grid: TileGrid, private val fadeNanos: Long = FADE_NANOS) {
    /** The mask's size in blocks; it spans width × [BLOCK] stream pixels, at least the stream's width. */
    val width = (grid.width + BLOCK - 1) / BLOCK
    val height = (grid.height + BLOCK - 1) / BLOCK

    /** The mask, row by row from the top: 255 shows the tile, 0 the video. */
    val alpha = ByteArray(width * height)

    /** The blocks the last [update] painted: columns [dirtyLeft] until [dirtyRight], rows [dirtyTop] until [dirtyBottom]. */
    var dirtyLeft = 0
        private set
    var dirtyTop = 0
        private set
    var dirtyRight = 0
        private set
    var dirtyBottom = 0
        private set

    /** Whether the last [update] painted any block. */
    val dirty: Boolean get() = dirtyRight > dirtyLeft && dirtyBottom > dirtyTop

    // When each slot's tile was first drawn, or HIDDEN.
    private val shownAt = LongArray(grid.count) { HIDDEN }
    private val opacity = FloatArray(grid.count)
    private var fading = false

    // The tiles whose opacity changed since the last update, inclusive.
    private var changedLeft = Int.MAX_VALUE
    private var changedTop = Int.MAX_VALUE
    private var changedRight = -1
    private var changedBottom = -1

    /** Takes what [plan] changes in the texture at [nowNanos]: cleared tiles go at once, uploaded ones start to fade in. */
    fun apply(plan: TilePlan, nowNanos: Long) {
        if (plan.clearAll) {
            for (slot in shownAt.indices) hide(slot)
        } else {
            for (i in 0 until plan.clearCount) hide(plan.clears[i])
        }
        for (i in 0 until plan.uploadCount) {
            val t = plan.uploads[i] ?: continue
            hide(t.slot)
            shownAt[t.slot] = nowNanos
            fading = true
        }
    }

    /** Forgets every tile: the texture went with the GL context, and the next one starts clear. */
    fun reset() {
        shownAt.fill(HIDDEN)
        opacity.fill(0f)
        alpha.fill(0)
        fading = false
        changedRight = -1
        dirtyLeft = 0
        dirtyRight = 0
    }

    /**
     * Brings each tile's fade to [nowNanos] and paints the blocks of every
     * tile whose opacity changed and of its neighbours (the dirty
     * rectangle). Returns true while a tile is still fading in, so the frame
     * should be drawn again at the next refresh.
     */
    fun update(nowNanos: Long): Boolean {
        if (fading) {
            fading = false
            for (slot in shownAt.indices) {
                val start = shownAt[slot]
                if (start == HIDDEN || opacity[slot] >= 1f) continue
                val o = fadeAt(nowNanos - start)
                if (o != opacity[slot]) setOpacity(slot, o)
                if (o < 1f) fading = true
            }
        }
        paint()
        return fading
    }

    private fun hide(slot: Int) {
        shownAt[slot] = HIDDEN
        if (opacity[slot] != 0f) setOpacity(slot, 0f)
    }

    private fun fadeAt(elapsed: Long): Float {
        val t = (elapsed.toFloat() / fadeNanos).coerceIn(0f, 1f)
        return t * t * (3 - 2 * t)
    }

    private fun setOpacity(slot: Int, o: Float) {
        opacity[slot] = o
        val col = slot % grid.cols
        val row = slot / grid.cols
        changedLeft = minOf(changedLeft, col)
        changedTop = minOf(changedTop, row)
        changedRight = maxOf(changedRight, col)
        changedBottom = maxOf(changedBottom, row)
    }

    private fun paint() {
        if (changedRight < 0) {
            dirtyLeft = 0
            dirtyRight = 0
            return
        }
        val left = maxOf(changedLeft - 1, 0)
        val top = maxOf(changedTop - 1, 0)
        val right = minOf(changedRight + 1, grid.cols - 1)
        val bottom = minOf(changedBottom + 1, grid.rows - 1)
        for (row in top..bottom) for (col in left..right) paintTile(col, row)
        dirtyLeft = left * PER_TILE
        dirtyTop = top * PER_TILE
        dirtyRight = minOf((right + 1) * PER_TILE, width)
        dirtyBottom = minOf((bottom + 1) * PER_TILE, height)
        changedLeft = Int.MAX_VALUE
        changedTop = Int.MAX_VALUE
        changedRight = -1
        changedBottom = -1
    }

    // A neighbour beyond the stream's edge counts as drawn: nothing blends there.
    private fun opacityAt(col: Int, row: Int): Float =
        if (col < 0 || row < 0 || col >= grid.cols || row >= grid.rows) 1f else opacity[row * grid.cols + col]

    private fun paintTile(col: Int, row: Int) {
        val own = opacity[row * grid.cols + col]
        val x0 = col * PER_TILE
        val y0 = row * PER_TILE
        val x1 = minOf(x0 + PER_TILE, width)
        val y1 = minOf(y0 + PER_TILE, height)
        val l = opacityAt(col - 1, row)
        val r = opacityAt(col + 1, row)
        val t = opacityAt(col, row - 1)
        val b = opacityAt(col, row + 1)
        val tl = opacityAt(col - 1, row - 1)
        val tr = opacityAt(col + 1, row - 1)
        val bl = opacityAt(col - 1, row + 1)
        val br = opacityAt(col + 1, row + 1)
        for (y in y0 until y1) {
            val atTop = y == y0
            val atBottom = y == y1 - 1
            for (x in x0 until x1) {
                val atLeft = x == x0
                val atRight = x == x1 - 1
                var v = own
                if (atLeft) v = min(v, l)
                if (atRight) v = min(v, r)
                if (atTop) v = min(v, t)
                if (atBottom) v = min(v, b)
                if (atTop && atLeft) v = min(v, tl)
                if (atTop && atRight) v = min(v, tr)
                if (atBottom && atLeft) v = min(v, bl)
                if (atBottom && atRight) v = min(v, br)
                alpha[y * width + x] = (v * 255 + 0.5f).toInt().toByte()
            }
        }
    }

    companion object {
        /** Stream pixels per mask texel, across and down. */
        const val BLOCK = 8

        /** How long a new tile takes to fade in. */
        const val FADE_NANOS = 120_000_000L

        private const val PER_TILE = TileWire.TILE_SIZE / BLOCK
        private const val HIDDEN = Long.MIN_VALUE
    }
}
