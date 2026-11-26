package com.desklink.client.video

import org.junit.Assert.assertEquals
import org.junit.Test
import java.nio.ByteBuffer
import java.nio.ByteOrder
import java.util.zip.GZIPInputStream
import kotlin.math.abs
import kotlin.math.floor
import kotlin.math.roundToInt

/**
 * Runs the arithmetic of [GuidedShaders]' three passes on a decoded frame and
 * measures how far the result is from the desktop's real pixels.
 *
 * The sample (coloured-text.bin.gz) is a 168×88 crop, at 180,148, of a
 * synthetic 2560×1600 desktop: "if (i != 1) {" in Noto Sans Mono 16 px, in
 * grey (225,225,225), blue (101,172,255) and red (248,95,105) on the dark
 * theme's grey 28, converted to P010 by the host's vapostproc and encoded by
 * vaav1enc at QP 12, as desklinkd does, then decoded with ffmpeg. It holds
 * the decoded 10-bit planes and the original RGB of the same crop. The error
 * is the mean absolute 8-bit channel difference on and within one pixel of
 * the letters, as in the research that proposed the method (offline, in
 * Python: 13.01 levels with plain chroma interpolation, 3.83 guided; with
 * the converter's code scale undone, [StreamColor], 12.96 and 3.76).
 */
class GuidedChromaTest {
    private class Sample(
        val width: Int,
        val height: Int,
        val left: Int,
        val top: Int,
        val luma: IntArray,
        val cb: IntArray,
        val cr: IntArray,
        val source: IntArray,
    )

    private fun load(): Sample {
        val raw = GZIPInputStream(checkNotNull(javaClass.getResourceAsStream("coloured-text.bin.gz"))).use { it.readBytes() }
        val b = ByteBuffer.wrap(raw).order(ByteOrder.LITTLE_ENDIAN)
        val magic = ByteArray(4).also { b.get(it) }
        check(String(magic, Charsets.US_ASCII) == "DLGC") { "not a DeskLink chroma sample" }
        val w = b.short.toInt() and 0xffff
        val h = b.short.toInt() and 0xffff
        val left = b.short.toInt() and 0xffff
        val top = b.short.toInt() and 0xffff
        fun codes(n: Int) = IntArray(n) { b.short.toInt() and 0xffff }
        val luma = codes(w * h)
        val cb = codes(w * h / 4)
        val cr = codes(w * h / 4)
        val source = IntArray(w * h * 3) { b.get().toInt() and 0xff }
        return Sample(w, h, left, top, luma, cb, cr, source)
    }

    /** Rounds to the nearest half float, as the RGBA16F textures between the passes store it. */
    private fun half(v: Float): Float {
        if (v == 0f) return v
        val step = Math.scalb(1f, maxOf(Math.getExponent(v), -14) - 10)
        return (Math.rint((v / step).toDouble()) * step).toFloat()
    }

    private val range = StreamColor.range

    private fun sampled(code: Int) = code / StreamColor.CODE_SCALE.toFloat()

    /** Pass 1: per chroma sample, the 2×2 block's mean Y' and the sample's Cb, Cr. */
    private fun stats(s: Sample): Array<FloatArray> {
        val cw = s.width / 2
        val ch = s.height / 2
        val out = Array(3) { FloatArray(cw * ch) }
        for (j in 0 until ch) for (i in 0 until cw) {
            fun y(dx: Int, dy: Int) = sampled(s.luma[(2 * j + dy) * s.width + 2 * i + dx])
            val luma = y(0, 0) + y(1, 0) + y(0, 1) + y(1, 1)
            out[0][j * cw + i] = half((luma * 0.25f - range[0]) * range[1])
            out[1][j * cw + i] = half(StreamColor.chroma((sampled(s.cb[j * cw + i]) * range[2]).toDouble()).toFloat())
            out[2][j * cw + i] = half(StreamColor.chroma((sampled(s.cr[j * cw + i]) * range[2]).toDouble()).toFloat())
        }
        return out
    }

    /** Pass 2: slope and intercept of Cb and Cr against Y' over the clamped window. */
    private fun coefficients(st: Array<FloatArray>, cw: Int, ch: Int): Array<FloatArray> {
        val r = GuidedShaders.RADIUS
        val eps = GuidedShaders.EPSILON.toFloat()
        val out = Array(4) { FloatArray(cw * ch) }
        for (j in 0 until ch) for (i in 0 until cw) {
            val sum = FloatArray(3)
            val products = FloatArray(3)
            for (dy in -r..r) for (dx in -r..r) {
                val k = (j + dy).coerceIn(0, ch - 1) * cw + (i + dx).coerceIn(0, cw - 1)
                for (c in 0..2) {
                    sum[c] += st[c][k]
                    products[c] += st[0][k] * st[c][k]
                }
            }
            val n = ((2 * r + 1) * (2 * r + 1)).toFloat()
            val mean = FloatArray(3) { sum[it] / n }
            val meanProducts = FloatArray(3) { products[it] / n }
            val variance = maxOf(meanProducts[0] - mean[0] * mean[0], 0f)
            for (c in 1..2) {
                val slope = (meanProducts[c] - mean[0] * mean[c]) / (variance + eps)
                out[2 * (c - 1)][j * cw + i] = half(slope)
                out[2 * (c - 1) + 1][j * cw + i] = half(mean[c] - slope * mean[0])
            }
        }
        return out
    }

    /**
     * Pass 3: coefficients interpolated bilinearly at each pixel, applied to
     * its Y', then R'G'B' in 8 bits. The shader then undoes the tablet's
     * darkening ([StreamColor.undoDarkening]); this crop was decoded with
     * dav1d, which does not darken, so that last step is left out here and
     * StreamColorTest covers it.
     */
    private fun compose(s: Sample, k: Array<FloatArray>): IntArray {
        val cw = s.width / 2
        val ch = s.height / 2
        val m = StreamColor.toRgb
        val out = IntArray(s.width * s.height * 3)
        fun bilinear(plane: FloatArray, u: Float, v: Float): Float {
            val i0 = floor(u).toInt()
            val j0 = floor(v).toInt()
            val fu = u - i0
            val fv = v - j0
            fun at(i: Int, j: Int) = plane[j.coerceIn(0, ch - 1) * cw + i.coerceIn(0, cw - 1)]
            val topRow = at(i0, j0) * (1 - fu) + at(i0 + 1, j0) * fu
            val bottomRow = at(i0, j0 + 1) * (1 - fu) + at(i0 + 1, j0 + 1) * fu
            return topRow * (1 - fv) + bottomRow * fv
        }
        for (y in 0 until s.height) for (x in 0 until s.width) {
            val luma = (sampled(s.luma[y * s.width + x]) - range[0]) * range[1]
            val u = (x + 0.5f) / 2 - 0.5f
            val v = (y + 0.5f) / 2 - 0.5f
            val cb = bilinear(k[0], u, v) * luma + bilinear(k[1], u, v)
            val cr = bilinear(k[2], u, v) * luma + bilinear(k[3], u, v)
            for (c in 0..2) {
                val value = m[c] * luma + m[3 + c] * cb + m[6 + c] * cr
                out[(y * s.width + x) * 3 + c] = (value.coerceIn(0f, 1f) * 255).roundToInt()
            }
        }
        return out
    }

    private fun guided(s: Sample): IntArray = compose(s, coefficients(stats(s), s.width / 2, s.height / 2))

    /** Plain bilinear chroma: a slope of 0 and the chroma sample as the intercept. */
    private fun interpolated(s: Sample): IntArray {
        val st = stats(s)
        val zero = FloatArray(st[0].size)
        return compose(s, arrayOf(zero, st[1], zero, st[2]))
    }

    /** The mean channel error on and next to the letters in frame rows [top] until [bottom], columns 188 until 340. */
    private fun letterError(s: Sample, rgb: IntArray, top: Int, bottom: Int): Double {
        val x0 = 188 - s.left
        val x1 = 340 - s.left
        val y0 = top - s.top
        val y1 = bottom - s.top
        fun src(x: Int, y: Int, c: Int) = s.source[(y * s.width + x) * 3 + c]
        fun ink(x: Int, y: Int) = (0..2).any { abs(src(x, y, it) - 28) > 8 }
        var sum = 0.0
        var count = 0
        for (y in y0 until y1) for (x in x0 until x1) {
            val near = (-1..1).any { dy -> (-1..1).any { dx -> (y + dy) in y0 until y1 && (x + dx) in x0 until x1 && ink(x + dx, y + dy) } }
            if (!near) continue
            for (c in 0..2) sum += abs(src(x, y, c) - rgb[(y * s.width + x) * 3 + c])
            count += 3
        }
        return sum / count
    }

    @Test
    fun rebuildingChromaFromLumaCutsTheErrorOnColouredTextFrom13To4Levels() {
        val s = load()
        assertEquals(12.96, letterError(s, interpolated(s), 180, 228), 0.05)
        assertEquals(3.76, letterError(s, guided(s), 180, 228), 0.05)
    }

    @Test
    fun greyTextStaysAsCloseAsWithPlainInterpolation() {
        val s = load()
        assertEquals(1.03, letterError(s, interpolated(s), 156, 180), 0.05)
        assertEquals(1.03, letterError(s, guided(s), 156, 180), 0.05)
    }
}
