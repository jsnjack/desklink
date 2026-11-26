package com.desklink.client.video

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import kotlin.math.abs
import kotlin.math.pow
import kotlin.math.roundToInt

class StreamColorTest {
    /**
     * The host's conversion as Mesa's vapostproc does it (colorimetry
     * 2:3:7:1): sRGB-encoded R'G'B' to limited-range Y'CbCr with the BT.709
     * matrix, no transfer change, at 8-bit studio levels written into P010's
     * 16 bits (level × 257), of which the encoder takes the top 10.
     */
    private fun hostEncode(r: Int, g: Int, b: Int): IntArray {
        val rf = r / 255.0
        val gf = g / 255.0
        val bf = b / 255.0
        val y = 0.2126 * rf + 0.7152 * gf + 0.0722 * bf
        val cb = (bf - y) / 1.8556
        val cr = (rf - y) / 1.5748
        fun code(level: Double) = (level / 255 * 65535).roundToInt() shr 6
        return intArrayOf(code(16 + 219 * y), code(128 + 224 * cb), code(128 + 224 * cr))
    }

    /** What the shaders do with sampled values: the range uniform and chroma mapping, then the matrix uniform. */
    private fun shaderDecode(codes: IntArray, codeScale: Double = StreamColor.CODE_SCALE): DoubleArray {
        val range = StreamColor.range
        val y = (codes[0] / codeScale - range[0]) * range[1]
        val cb = StreamColor.chroma(codes[1] / codeScale * range[2])
        val cr = StreamColor.chroma(codes[2] / codeScale * range[2])
        val m = StreamColor.toRgb
        // Column-major: column 0 multiplies Y', column 1 Cb, column 2 Cr.
        return DoubleArray(3) { row -> ((m[row] * y + m[3 + row] * cb + m[6 + row] * cr) * 255).coerceIn(0.0, 255.0) }
    }

    private fun assertComesBack(what: String, rgb: IntArray, decoded: DoubleArray) {
        for (c in 0..2) assertEquals("$what, channel $c of ${decoded.toList()}", rgb[c], decoded[c].roundToInt())
    }

    @Test
    fun everyDesktopGreyComesBackGrey() {
        for (v in 0..255) {
            val codes = hostEncode(v, v, v)
            assertComesBack("grey $v", intArrayOf(v, v, v), shaderDecode(codes))
            // A sampler that reads P010's 16-bit container divides by 1023.98.
            assertComesBack("grey $v from 16 bits", intArrayOf(v, v, v), shaderDecode(codes, 65535.0 / 64))
        }
    }

    @Test
    fun aGreyWhoseChromaTheEncoderRoundedToTheStandardCodeComesBackGreyToo() {
        // Flat areas, predicted from the frame's edge, decode with chroma 512.
        for (v in 0..255) {
            val codes = hostEncode(v, v, v)
            assertComesBack("grey $v", intArrayOf(v, v, v), shaderDecode(intArrayOf(codes[0], 512, 512)))
        }
    }

    @Test
    fun theCodesTheHostsGpuWritesDecodeToTheDesktopsColours() {
        // vapostproc on the RX 7800 XT (Mesa 26.2.7), as TestConversionKeepsDesktopColours reads them.
        val written = mapOf(
            listOf(0, 0, 0) to intArrayOf(64, 514, 514),
            listOf(18, 18, 18) to intArrayOf(126, 514, 514),
            listOf(28, 28, 28) to intArrayOf(160, 514, 514),
            listOf(36, 36, 36) to intArrayOf(188, 514, 514),
            listOf(128, 128, 128) to intArrayOf(505, 514, 514),
            listOf(225, 225, 225) to intArrayOf(840, 514, 514),
            listOf(255, 255, 255) to intArrayOf(943, 514, 514),
            listOf(101, 172, 255) to intArrayOf(626, 689, 375),
            listOf(248, 95, 105) to intArrayOf(506, 469, 782),
            listOf(53, 132, 228) to intArrayOf(485, 715, 359),
            listOf(255, 0, 0) to intArrayOf(251, 410, 963),
            listOf(0, 0, 255) to intArrayOf(127, 963, 472),
        )
        for ((rgb, codes) in written) {
            assertEquals("the model of the host's conversion for $rgb", codes.toList(), hostEncode(rgb[0], rgb[1], rgb[2]).toList())
            val decoded = shaderDecode(codes)
            if (rgb[0] == rgb[1] && rgb[1] == rgb[2]) {
                assertComesBack("grey $rgb", rgb.toIntArray(), decoded)
            } else {
                // A chroma code falls up to a step short of the converter's value.
                for (c in 0..2) assertEquals("channel $c of $rgb", rgb[c].toDouble(), decoded[c], 0.75)
            }
        }
    }

    @Test
    fun theDarkThemesGreyIsTheLumaCodeTheHostWrites() {
        // Grey 128 is luma 504 in the standard codes, as the host's encoder
        // comment measured, and 505 as vapostproc writes it.
        assertEquals(505, hostEncode(128, 128, 128)[0])
        assertEquals(28, shaderDecode(hostEncode(28, 28, 28))[0].roundToInt())
    }

    @Test
    fun coloursComeBackWithinALevel() {
        var worst = 0.0
        for (r in 0..255 step 15) for (g in 0..255 step 15) for (b in 0..255 step 15) {
            val rgb = shaderDecode(hostEncode(r, g, b))
            worst = maxOf(worst, abs(rgb[0] - r), abs(rgb[1] - g), abs(rgb[2] - b))
        }
        // One chroma step (1/896) moves red by 0.45 and blue by 0.53 of an
        // 8-bit level; colours within a level of grey are pulled to grey by up
        // to two steps.
        assertTrue("worst error $worst levels", worst < 1.1)
    }

    @Test
    fun saturatedSyntaxColoursKeepTheirHue() {
        for (c in listOf(intArrayOf(101, 172, 255), intArrayOf(248, 95, 105), intArrayOf(0, 0, 255), intArrayOf(255, 0, 0))) {
            val rgb = shaderDecode(hostEncode(c[0], c[1], c[2]))
            for (i in 0..2) assertEquals("channel $i of ${c.toList()}", c[i].toDouble(), rgb[i], 0.75)
        }
    }

    @Test
    fun theChromaMappingHasNoSteps() {
        var last = StreamColor.chroma(0.0)
        var code = 0.0
        while (code <= 1023) {
            val c = StreamColor.chroma(code)
            assertTrue("chroma falls at code $code", c >= last)
            assertTrue("chroma jumps at code $code", c - last <= 2.01 * 0.25 * StreamColor.range[3])
            last = c
            code += 0.25
        }
        assertEquals(0.0, StreamColor.chroma(512.0), 0.0)
        assertEquals(0.0, StreamColor.chroma(514.0), 0.0)
    }

    /** What the Tab S9 does to the frames on the way to the renderer: decodes them with gamma 2.2 and encodes them as sRGB. */
    private fun darken(v: Double): Double {
        val linear = v.pow(2.2)
        return if (linear <= 0.0031308) linear * 12.92 else 1.055 * linear.pow(1 / 2.4) - 0.055
    }

    @Test
    fun theTabletsDarkeningIsUndoneForEveryGrey() {
        for (v in 0..255) {
            assertEquals("grey $v", v, (StreamColor.undoDarkening(darken(v / 255.0)) * 255).roundToInt())
        }
    }

    @Test
    fun theGreysMeasuredOnTheTabletComeBackWithinALevel() {
        // A Tab S9 screenshot of a grey chart (2026-11-26, Android 16, AV1):
        // each desk grey drawn as an exact tile and, beside it, as video.
        // Below 8 the screenshot's 8 bits cannot tell the darkened levels apart.
        val videoOfDeskGrey = mapOf(
            8 to 2, 12 to 4, 16 to 7, 20 to 12, 24 to 17, 28 to 22, 32 to 26, 40 to 35, 48 to 44,
            64 to 62, 80 to 79, 96 to 96, 128 to 129, 160 to 162, 192 to 193, 224 to 225, 255 to 255,
        )
        for ((desk, video) in videoOfDeskGrey) {
            val restored = StreamColor.undoDarkening(video / 255.0) * 255
            assertTrue("grey $desk arrived as $video and was restored to $restored", abs(restored - desk) <= 1.0)
        }
    }
}
