package com.desklink.client.video

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import kotlin.math.abs
import kotlin.math.roundToInt

class StreamColorTest {
    /**
     * The host's conversion (vapostproc with colorimetry 2:3:7:1): sRGB-encoded
     * R'G'B' to 10-bit limited-range Y'CbCr with the BT.709 matrix, no
     * transfer change.
     */
    private fun hostEncode(r: Int, g: Int, b: Int): IntArray {
        val rf = r / 255.0
        val gf = g / 255.0
        val bf = b / 255.0
        val y = 0.2126 * rf + 0.7152 * gf + 0.0722 * bf
        val cb = (bf - y) / 1.8556
        val cr = (rf - y) / 1.5748
        return intArrayOf((64 + 876 * y).roundToInt(), (512 + 896 * cb).roundToInt(), (512 + 896 * cr).roundToInt())
    }

    /** What the compose shader does with sampled values: the range uniform, then the matrix uniform. */
    private fun shaderDecode(codes: IntArray, codeScale: Double = StreamColor.CODE_SCALE): DoubleArray {
        val range = StreamColor.range
        val y = (codes[0] / codeScale - range[0]) * range[1]
        val cb = (codes[1] / codeScale - range[2]) * range[3]
        val cr = (codes[2] / codeScale - range[2]) * range[3]
        val m = StreamColor.toRgb
        // Column-major: column 0 multiplies Y', column 1 Cb, column 2 Cr.
        return DoubleArray(3) { row -> (m[row] * y + m[3 + row] * cb + m[6 + row] * cr) * 255 }
    }

    @Test
    fun everyDesktopGreyComesBackExactly() {
        for (v in 0..255) {
            val codes = hostEncode(v, v, v)
            assertEquals("grey $v", v, shaderDecode(codes)[1].roundToInt())
            // A sampler that reads P010's 16-bit container divides by 1023.98.
            assertEquals("grey $v from 16 bits", v, shaderDecode(codes, 65535.0 / 64)[1].roundToInt())
        }
    }

    @Test
    fun theDarkThemesGreyIsTheLumaCodeTheHostWrites() {
        // Grey 128 is luma 504, as the host's encoder comment measured.
        assertEquals(504, hostEncode(128, 128, 128)[0])
        assertEquals(28, shaderDecode(hostEncode(28, 28, 28))[0].roundToInt())
    }

    @Test
    fun coloursComeBackWithinTheTenBitStepsOfTheStream() {
        var worst = 0.0
        for (r in 0..255 step 15) for (g in 0..255 step 15) for (b in 0..255 step 15) {
            val rgb = shaderDecode(hostEncode(r, g, b))
            worst = maxOf(worst, abs(rgb[0] - r), abs(rgb[1] - g), abs(rgb[2] - b))
        }
        // One chroma step (1/896) moves red by 0.45 and blue by 0.53 of an 8-bit level.
        assertTrue("worst error $worst levels", worst < 0.75)
    }

    @Test
    fun saturatedSyntaxColoursKeepTheirHue() {
        for (c in listOf(intArrayOf(101, 172, 255), intArrayOf(248, 95, 105), intArrayOf(0, 0, 255), intArrayOf(255, 0, 0))) {
            val rgb = shaderDecode(hostEncode(c[0], c[1], c[2]))
            for (i in 0..2) assertEquals("channel $i of ${c.toList()}", c[i].toDouble(), rgb[i], 0.75)
        }
    }
}
