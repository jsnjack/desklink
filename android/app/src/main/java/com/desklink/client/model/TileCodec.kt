package com.desklink.client.model

import java.util.zip.DataFormatException
import java.util.zip.Inflater

/**
 * Exact tiles: binary messages on the control WebSocket that carry the exact
 * pixels of parts of the screen that stopped changing, and say for every
 * video frame which of those it changed. host/internal/net/tiles.go is the
 * reference and docs/protocol.md describes the layout.
 */
object TileWire {
    /** The side of a tile in stream pixels; the last column and row are cut to the stream. */
    const val TILE_SIZE = 64

    /** Byte 0 of a tile_frame message. */
    const val KIND_FRAME = 1

    /** Byte 0 of a tile_data message. */
    const val KIND_DATA = 2
    const val FRAME_HEADER = 20
    const val DATA_HEADER = 8
    const val RECORD = 8
    const val MAX_TILES = 64
    private const val FLAG_ALL = 0x01

    /** Decodes a tile_frame message; null when it is malformed. */
    fun parseFrame(b: ByteArray): TileFrame? {
        if (b.size < FRAME_HEADER || b[0].toInt() != KIND_FRAME) return null
        val n = u16(b, 18)
        if (b.size != FRAME_HEADER + 2 * n) return null
        return TileFrame(
            grid = TileGrid(u16(b, 2), u16(b, 4)),
            seq = u32(b, 6),
            captureMicros = u64(b, 10),
            all = b[1].toInt() and FLAG_ALL != 0,
            changed = IntArray(n) { u16(b, FRAME_HEADER + 2 * it) },
        )
    }

    /**
     * Decodes a tile_data message and its tiles' pixels into RGBA; null when
     * it is malformed. The decompressed size is fixed by the records, so a
     * message cannot make the decoder allocate more than they declare.
     */
    fun parseData(b: ByteArray): TileData? {
        if (b.size < DATA_HEADER || b[0].toInt() != KIND_DATA) return null
        val grid = TileGrid(u16(b, 2), u16(b, 4))
        val n = u16(b, 6)
        val body = DATA_HEADER + n * RECORD
        if (n == 0 || n > MAX_TILES || b.size < body || grid.count == 0) return null
        val lengths = IntArray(n)
        val seqs = LongArray(n)
        val slots = IntArray(n)
        var total = 0
        for (i in 0 until n) {
            val r = DATA_HEADER + i * RECORD
            seqs[i] = u32(b, r)
            slots[i] = u16(b, r + 4)
            lengths[i] = u16(b, r + 6)
            if (slots[i] >= grid.count) return null
            total += lengths[i]
        }
        val ops = ByteArray(total)
        val inflater = Inflater()
        try {
            inflater.setInput(b, body, b.size - body)
            var done = 0
            while (done < total) {
                val got = inflater.inflate(ops, done, total - done)
                if (got == 0 && (inflater.needsInput() || inflater.finished() || inflater.needsDictionary())) return null
                done += got
            }
        } catch (_: DataFormatException) {
            return null
        } finally {
            inflater.end()
        }
        var at = 0
        val tiles = ArrayList<Tile>(n)
        for (i in 0 until n) {
            val slot = slots[i]
            val x = grid.x(slot)
            val y = grid.y(slot)
            val w = minOf(TILE_SIZE, grid.width - x)
            val h = minOf(TILE_SIZE, grid.height - y)
            val rgba = ByteArray(w * h * 4)
            if (!decodeQoi(ops, at, lengths[i], w * h, rgba)) return null
            at += lengths[i]
            tiles += Tile(seqs[i], slot, x, y, w, h, rgba)
        }
        return TileData(grid, tiles)
    }

    private const val OP_INDEX = 0x00
    private const val OP_DIFF = 0x40
    private const val OP_LUMA = 0x80
    private const val OP_RGB = 0xfe
    private const val OP_RGBA = 0xff
    private const val MASK = 0xc0

    /**
     * Decodes [length] bytes of QOI ops at [offset] into [pixels] RGBA
     * pixels with alpha 255 (the spec's op stream without header or end
     * marker, starting afresh); false when they do not make exactly that many.
     */
    fun decodeQoi(ops: ByteArray, offset: Int, length: Int, pixels: Int, out: ByteArray): Boolean {
        val index = IntArray(64)
        var r = 0
        var g = 0
        var b = 0
        var run = 0
        var p = offset
        val end = offset + length
        var o = 0
        for (i in 0 until pixels) {
            if (run > 0) {
                run--
            } else {
                if (p >= end) return false
                val b1 = ops[p++].toInt() and 0xff
                when {
                    b1 == OP_RGB || b1 == OP_RGBA -> {
                        val size = if (b1 == OP_RGBA) 4 else 3
                        if (p + size > end) return false
                        r = ops[p].toInt() and 0xff
                        g = ops[p + 1].toInt() and 0xff
                        b = ops[p + 2].toInt() and 0xff
                        p += size
                    }
                    b1 and MASK == OP_INDEX -> {
                        val v = index[b1]
                        r = v ushr 16 and 0xff
                        g = v ushr 8 and 0xff
                        b = v and 0xff
                    }
                    b1 and MASK == OP_DIFF -> {
                        r = (r + (b1 shr 4 and 3) - 2) and 0xff
                        g = (g + (b1 shr 2 and 3) - 2) and 0xff
                        b = (b + (b1 and 3) - 2) and 0xff
                    }
                    b1 and MASK == OP_LUMA -> {
                        if (p >= end) return false
                        val b2 = ops[p++].toInt() and 0xff
                        val dg = (b1 and 0x3f) - 32
                        r = (r + dg + (b2 shr 4) - 8) and 0xff
                        g = (g + dg) and 0xff
                        b = (b + dg + (b2 and 0x0f) - 8) and 0xff
                    }
                    else -> run = b1 and 0x3f
                }
                index[(r * 3 + g * 5 + b * 7 + 255 * 11) % 64] = (r shl 16) or (g shl 8) or b
            }
            out[o] = r.toByte()
            out[o + 1] = g.toByte()
            out[o + 2] = b.toByte()
            out[o + 3] = -1
            o += 4
        }
        return run == 0 && p == end
    }

    private fun u16(b: ByteArray, i: Int) = (b[i].toInt() and 0xff shl 8) or (b[i + 1].toInt() and 0xff)

    private fun u32(b: ByteArray, i: Int) = (u16(b, i).toLong() shl 16) or u16(b, i + 2).toLong()

    private fun u64(b: ByteArray, i: Int) = (u32(b, i) shl 32) or u32(b, i + 4)
}

/** How a [width] × [height] stream is cut into tiles, numbered row by row from the top-left corner. */
data class TileGrid(val width: Int, val height: Int) {
    val cols: Int = (width + TileWire.TILE_SIZE - 1) / TileWire.TILE_SIZE
    val rows: Int = (height + TileWire.TILE_SIZE - 1) / TileWire.TILE_SIZE
    val count: Int = cols * rows

    /** The left edge of tile [slot] in stream pixels. */
    fun x(slot: Int) = slot % cols * TileWire.TILE_SIZE

    /** The top edge of tile [slot] in stream pixels. */
    fun y(slot: Int) = slot / cols * TileWire.TILE_SIZE
}

/**
 * A tile_frame message: frame [seq] of the host's encoded frames, which the
 * decoder presents at [captureMicros], changed the tiles [changed]; with
 * [all], every tile from an earlier frame is invalid.
 */
class TileFrame(val grid: TileGrid, val seq: Long, val captureMicros: Long, val all: Boolean, val changed: IntArray)

/** One tile's exact pixels, valid from frame [seq] on, at [x], [y], [w] × [h] stream pixels, as RGBA. */
class Tile(val seq: Long, val slot: Int, val x: Int, val y: Int, val w: Int, val h: Int, val rgba: ByteArray)

/** A tile_data message. */
class TileData(val grid: TileGrid, val tiles: List<Tile>)
