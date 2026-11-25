package com.desklink.client.video

import com.desklink.client.model.Tile
import com.desklink.client.model.TileFrame
import com.desklink.client.model.TileGrid

/**
 * Decides which exact tiles to draw over each presented video frame, without
 * touching GL ([TileOverlay] applies its [TilePlan]s).
 *
 * The host numbers its encoded frames and sends a tile_frame for each, with
 * the frame's capture time, which is also the time the decoder presents it
 * at; so the renderer's frame time names the frame it shows. A tile is drawn
 * over a frame only when that frame's tile_frame has arrived, the tile is
 * from that frame or an earlier one, and no tile_frame between them listed
 * its slot. Without the frame's tile_frame (late, or lost with a reset) no
 * tile is drawn at all, so a frame nobody checked never shows a stale tile.
 * A tile that arrives after a newer tile_frame listed its slot is discarded.
 *
 * The network thread calls [onFrame] and [onTiles], the render thread
 * [present] and [textureLost].
 */
class TileLayer(val grid: TileGrid, private val historySize: Int = DEFAULT_HISTORY) {
    private val lock = Any()

    // The seq of each recent tile_frame, by the capture time of its frame.
    private val seqOf = object : LinkedHashMap<Long, Long>() {
        override fun removeEldestEntry(eldest: MutableMap.MutableEntry<Long, Long>?) = size > historySize
    }

    // The newest tile_frame that listed each slot, or listed all of them.
    private val invalidatedAt = LongArray(grid.count)
    private var allInvalidatedAt = 0L

    // The frame at which the tile drawn in each slot must go, or NONE.
    private val removeAt = LongArray(grid.count) { NONE }
    private var removeAllAt = NONE

    // The seq of the tile in the texture for each slot, or -1.
    private val shownSeq = LongArray(grid.count) { -1L }
    private var shownCount = 0

    // A tile waiting for its frame to be presented, per slot.
    private val pending = arrayOfNulls<Tile>(grid.count)
    private var presented = -1L
    private var gateClosedAt: Long? = null

    /** Tiles drawn over the last presented frame, for the stats. */
    @Volatile
    var shown: Int = 0
        private set

    /**
     * Takes a tile_frame message. Returns true when the last presented frame
     * was waiting for it, so drawing that frame again would show its tiles.
     */
    fun onFrame(f: TileFrame): Boolean = synchronized(lock) {
        if (f.grid != grid) return false
        seqOf[f.captureMicros] = f.seq
        if (f.all) {
            allInvalidatedAt = maxOf(allInvalidatedAt, f.seq)
            removeAllAt = minOf(removeAllAt, f.seq)
            for (slot in pending.indices) dropStalePending(slot, f.seq)
        }
        for (slot in f.changed) {
            if (slot !in 0 until grid.count) continue
            invalidatedAt[slot] = maxOf(invalidatedAt[slot], f.seq)
            removeAt[slot] = minOf(removeAt[slot], f.seq)
            dropStalePending(slot, f.seq)
        }
        gateClosedAt == f.captureMicros
    }

    private fun dropStalePending(slot: Int, seq: Long) {
        val p = pending[slot] ?: return
        if (p.seq < seq) pending[slot] = null
    }

    /**
     * Takes tiles of a tile_data message for this grid, discarding those
     * that a newer tile_frame already invalidated. Returns true when one of
     * them can be drawn over the frame presented last.
     */
    fun onTiles(tiles: List<Tile>): Boolean = synchronized(lock) {
        var now = false
        for (t in tiles) {
            if (t.slot !in 0 until grid.count || t.seq < maxOf(invalidatedAt[t.slot], allInvalidatedAt)) continue
            val p = pending[t.slot]
            if (p != null && p.seq > t.seq) continue
            pending[t.slot] = t
            if (t.seq <= presented) now = true
        }
        now
    }

    /**
     * Fills [plan] with what to change in the texture before drawing over
     * the frame presented at [frameMicros], and whether to draw at all. At
     * most [maxUploads] tiles are uploaded; [TilePlan.more] then says that
     * drawing the frame again would show more.
     */
    fun present(frameMicros: Long, plan: TilePlan, maxUploads: Int = DEFAULT_UPLOADS) = synchronized(lock) {
        plan.reset()
        val seq = seqOf[frameMicros]
        if (seq == null) {
            gateClosedAt = frameMicros
            shown = 0
            return@synchronized
        }
        gateClosedAt = null
        presented = seq
        if (removeAllAt <= seq) {
            if (shownCount > 0 && shownSeq.all { it < 0 || it < removeAllAt }) {
                plan.clearAll = true
                shownSeq.fill(-1L)
                shownCount = 0
            } else {
                for (slot in shownSeq.indices) if (shownSeq[slot] in 0 until removeAllAt) clear(slot, plan)
            }
            removeAllAt = NONE
        }
        for (slot in removeAt.indices) {
            if (removeAt[slot] > seq) continue
            if (shownSeq[slot] in 0 until removeAt[slot]) clear(slot, plan)
            removeAt[slot] = NONE
        }
        for (slot in pending.indices) {
            val p = pending[slot] ?: continue
            if (p.seq > seq) continue
            if (plan.uploadCount == maxUploads) {
                plan.more = true
                break
            }
            pending[slot] = null
            if (shownSeq[slot] < 0) shownCount++
            shownSeq[slot] = p.seq
            plan.uploads[plan.uploadCount++] = p
        }
        plan.draw = shownCount > 0
        shown = shownCount
    }

    private fun clear(slot: Int, plan: TilePlan) {
        shownSeq[slot] = -1L
        shownCount--
        plan.clears[plan.clearCount++] = slot
    }

    /**
     * The texture went with the renderer's GL context: no tile is drawn any
     * more, though what the tile_frames said stays known. Returns true when
     * tiles were drawn, which the host must send again.
     */
    fun textureLost(): Boolean = synchronized(lock) {
        val had = shownCount > 0
        shownSeq.fill(-1L)
        shownCount = 0
        shown = 0
        had
    }

    private companion object {
        const val NONE = Long.MAX_VALUE

        // About four seconds of frames at 120 a second: the renderer presents
        // a frame well within that of its tile_frame.
        const val DEFAULT_HISTORY = 512

        // 64 tiles are 1 MB of RGBA, uploaded in well under a millisecond.
        const val DEFAULT_UPLOADS = 64
    }
}

/** What [TileLayer.present] asks of the texture for one frame; reused from frame to frame. */
class TilePlan(slots: Int) {
    /** Slots to clear, in increasing order, before the uploads. */
    val clears = IntArray(slots)
    var clearCount = 0

    /** Clear the whole texture instead of [clears]. */
    var clearAll = false

    /** Tiles to upload after the clears. */
    val uploads = arrayOfNulls<Tile>(slots)
    var uploadCount = 0

    /** Whether any tile is to be drawn over this frame. */
    var draw = false

    /** Tiles are waiting for an upload that did not fit this frame. */
    var more = false

    fun reset() {
        clearCount = 0
        clearAll = false
        uploads.fill(null, 0, uploadCount)
        uploadCount = 0
        draw = false
        more = false
    }
}
