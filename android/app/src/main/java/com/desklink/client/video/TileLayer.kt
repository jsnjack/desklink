package com.desklink.client.video

import com.desklink.client.model.Tile
import com.desklink.client.model.TileFrame
import com.desklink.client.model.TileGrid

/**
 * Decides which exact tiles to draw over each presented video frame, without
 * touching GL ([TileOverlay] applies its [TilePlan]s).
 *
 * The host numbers its encoded frames (seq) and says, for each, which tiles
 * the frame changed (its invalidation), with the frame's capture time, which
 * is also the time the decoder presents it at; so the renderer's frame time
 * names the frame it shows. Invalidations come in UDP packets with the frame,
 * each repeating the three before it, or from older hosts as tile_frame
 * messages on the WebSocket, while tiles come on the WebSocket: they may
 * arrive in either order, twice, late or never.
 *
 * A tile is drawn over a frame only when the invalidations of that frame and
 * of every frame since the tile's have arrived and none listed its slot.
 * Without them no tile is drawn over the frame at all, so a frame nobody
 * checked never shows a stale tile; [onHidden] counts the frames that hid
 * tiles so. A tile is discarded when an invalidation newer than it lists its
 * slot, whichever arrives first.
 *
 * Over UDP, drawing starts at an invalidation of every tile, which the host
 * sends first. An invalidation that never arrives, though [LATE_FRAMES]
 * newer ones did, counts as one of every tile, and [onStartOver] asks the
 * host to send its tiles again, since it still counts them as sent; so does
 * waiting that long for the first. The WebSocket brings every tile_frame in
 * order, after the tiles they invalidate, so drawing starts at the first
 * (the ones before may have come before this layer).
 *
 * The network threads call [onFrames] and [onTiles], the render thread
 * [present] and [textureLost].
 */
class TileLayer(
    val grid: TileGrid,
    private val historySize: Int = DEFAULT_HISTORY,
    private val onStartOver: () -> Unit = {},
    private val onHidden: () -> Unit = {},
) {
    private val lock = Any()

    // The seq of each recent frame whose invalidation arrived, by the capture time of its frame.
    private val seqOf = object : LinkedHashMap<Long, Long>() {
        override fun removeEldestEntry(eldest: MutableMap.MutableEntry<Long, Long>?) = size > historySize
    }

    // Seqs whose invalidation arrived, beyond complete: each at its seq modulo the history.
    private val arrived = LongArray(historySize) { NONE }

    // Drawing starts at the invalidation of every tile at start (NONE before
    // the first); the invalidations from there to complete have all arrived.
    private var start = NONE
    private var complete = NONE
    private var newest = NONE

    // The oldest invalidation seen while waiting for the first of every tile.
    private var firstSeen = NONE

    // The newest seq when the host was asked to start over, until an
    // invalidation of every tile after it arrives; NONE when not asked.
    private var askedAt = NONE

    // The newest invalidation that listed each slot, or listed all of them.
    private val invalidatedAt = LongArray(grid.count)
    private var allInvalidatedAt = 0L

    // The first frame at which the tile drawn in each slot must go, or NONE.
    private val killAt = LongArray(grid.count) { NONE }

    // The seq of the tile in the texture for each slot, or -1.
    private val shownSeq = LongArray(grid.count) { -1L }
    private var shownCount = 0

    // A tile waiting for its frame to be presented, per slot.
    private val pending = arrayOfNulls<Tile>(grid.count)
    private var presented = -1L
    private var gateClosedAt: Long? = null
    private var hiddenAt: Long? = null

    /** Tiles drawn over the last presented frame, for the stats. */
    @Volatile
    var shown: Int = 0
        private set

    /** Takes a tile_frame message from the WebSocket ([onFrames]). */
    fun onFrame(f: TileFrame): Boolean = onFrames(listOf(f), ordered = true)

    /**
     * Takes invalidations: a UDP packet's (its frame's and the ones before
     * it), or with [ordered] a tile_frame message's. Returns true when the
     * last presented frame was waiting for them, so drawing it again would
     * show its tiles.
     */
    fun onFrames(frames: List<TileFrame>, ordered: Boolean = false): Boolean {
        var startOver = false
        val redraw = synchronized(lock) {
            for (f in frames.sortedBy { it.seq }) if (f.grid == grid) take(f, ordered)
            startOver = settleGaps()
            val gated = gateClosedAt
            gated != null && drawable(seqOf[gated])
        }
        if (startOver) onStartOver()
        return redraw
    }

    private fun take(f: TileFrame, ordered: Boolean) {
        val seq = f.seq
        if (start != NONE && seq <= complete) return
        if (arrived[slotOf(seq)] == seq) return
        if (start == NONE && ordered) {
            start = seq
            complete = seq - 1
        }
        if (start != NONE && seq - complete >= historySize) giveUp(seq - historySize + 1)
        seqOf[f.captureMicros] = seq
        newest = if (newest == NONE) seq else maxOf(newest, seq)
        if (start == NONE) firstSeen = minOf(firstSeen, seq)
        if (f.all) {
            invalidateAll(seq)
            if (askedAt != NONE && seq > askedAt) askedAt = NONE
        } else {
            for (slot in f.changed) if (slot in 0 until grid.count) invalidate(slot, seq)
        }
        arrived[slotOf(seq)] = seq
        // An invalidation of every tile after a gap makes the gap irrelevant.
        if (f.all && (start == NONE || seq > complete + 1)) {
            start = seq
            complete = seq
        }
        advance()
    }

    private fun slotOf(seq: Long) = (seq % historySize).toInt()

    private fun advance() {
        if (start == NONE) return
        while (arrived[slotOf(complete + 1)] == complete + 1) complete++
    }

    // Gives up on invalidations that never came; returns whether to ask the host to start over.
    private fun settleGaps(): Boolean {
        var lost = false
        if (start == NONE) {
            lost = firstSeen != NONE && newest - firstSeen >= LATE_FRAMES
        } else if (newest - complete > LATE_FRAMES) {
            // Frames before the last gap given up get no tiles, so the tiles
            // from before it may all go there.
            var at: Long
            do {
                at = complete + 1
                start = at
                complete = at
                advance()
            } while (newest - complete > LATE_FRAMES)
            invalidateAll(at)
            lost = true
        }
        if (!lost || askedAt != NONE) return false
        askedAt = newest
        return true
    }

    // The invalidation at seq never arrived: it may have listed any tile from before it.
    private fun giveUp(seq: Long) {
        start = seq
        complete = seq
        advance()
        invalidateAll(seq)
    }

    private fun invalidate(slot: Int, seq: Long) {
        invalidatedAt[slot] = maxOf(invalidatedAt[slot], seq)
        if (shownSeq[slot] in 0 until seq) killAt[slot] = minOf(killAt[slot], seq)
        val p = pending[slot]
        if (p != null && p.seq < seq) pending[slot] = null
    }

    private fun invalidateAll(seq: Long) {
        allInvalidatedAt = maxOf(allInvalidatedAt, seq)
        for (slot in 0 until grid.count) {
            if (shownSeq[slot] in 0 until seq) killAt[slot] = minOf(killAt[slot], seq)
            val p = pending[slot]
            if (p != null && p.seq < seq) pending[slot] = null
        }
    }

    // Whether tiles may be drawn over the frame seq: its invalidation and every one before it since drawing started arrived.
    private fun drawable(seq: Long?) = seq != null && start != NONE && seq in start..complete

    /**
     * Takes tiles of a tile_data message for this grid, discarding those
     * that a newer invalidation already listed. Returns true when one of
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
        if (seq == null || !drawable(seq)) {
            gateClosedAt = frameMicros
            if (shownCount > 0 && hiddenAt != frameMicros) {
                hiddenAt = frameMicros
                onHidden()
            }
            shown = 0
            return@synchronized
        }
        gateClosedAt = null
        presented = seq
        var going = 0
        for (slot in shownSeq.indices) if (shownSeq[slot] >= 0 && killAt[slot] <= seq) going++
        if (going > 0 && going == shownCount) {
            plan.clearAll = true
            shownSeq.fill(-1L)
            killAt.fill(NONE)
            shownCount = 0
        } else if (going > 0) {
            for (slot in shownSeq.indices) if (shownSeq[slot] >= 0 && killAt[slot] <= seq) clear(slot, plan)
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
            // No invalidation newer than the tile has arrived, or it would have gone.
            shownSeq[slot] = p.seq
            killAt[slot] = NONE
            plan.uploads[plan.uploadCount++] = p
        }
        plan.draw = shownCount > 0
        shown = shownCount
    }

    private fun clear(slot: Int, plan: TilePlan) {
        shownSeq[slot] = -1L
        killAt[slot] = NONE
        shownCount--
        plan.clears[plan.clearCount++] = slot
    }

    /**
     * The texture went with the renderer's GL context: no tile is drawn any
     * more, though what the invalidations said stays known. Returns true when
     * tiles were drawn, which the host must send again.
     */
    fun textureLost(): Boolean = synchronized(lock) {
        val had = shownCount > 0
        shownSeq.fill(-1L)
        killAt.fill(NONE)
        shownCount = 0
        shown = 0
        had
    }

    companion object {
        private const val NONE = Long.MAX_VALUE

        /**
         * How many newer invalidations may arrive before a missing one counts
         * as lost: the three packets after it repeat it, and a few more allow
         * for datagrams that overtake each other.
         */
        const val LATE_FRAMES = 8

        // About four seconds of frames at 120 a second: the renderer presents
        // a frame well within that of its invalidation.
        private const val DEFAULT_HISTORY = 512

        // 64 tiles are 1 MB of RGBA, uploaded in well under a millisecond.
        private const val DEFAULT_UPLOADS = 64
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
