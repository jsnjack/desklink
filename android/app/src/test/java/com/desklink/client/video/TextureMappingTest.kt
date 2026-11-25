package com.desklink.client.video

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Test
import kotlin.math.floor

class TextureMappingTest {
    /**
     * The matrix SurfaceTexture (GLConsumer::computeTransformMatrix) gives a
     * buffer [bufW] × [bufH] cropped to [cropW] × [cropH] from its top-left
     * corner, with filtering on: a cropped edge is pulled in by a texel (YUV
     * buffers), then the whole is flipped vertically.
     */
    private fun surfaceTextureMatrix(bufW: Int, bufH: Int, cropW: Int, cropH: Int): FloatArray {
        val shrink = 1f
        var tx = 0f
        var sx = 1f
        var ty = 0f
        var sy = 1f
        if (cropW < bufW) {
            tx = shrink / bufW
            sx = (cropW - 2 * shrink) / bufW
        }
        if (cropH < bufH) {
            ty = (bufH - cropH + shrink) / bufH
            sy = (cropH - 2 * shrink) / bufH
        }
        // flipV × crop, column-major.
        return floatArrayOf(sx, 0f, 0f, 0f, 0f, -sy, 0f, 0f, 0f, 0f, 1f, 0f, tx, 1f - ty, 0f, 1f)
    }

    private fun mapping(m: FloatArray) = FloatArray(4).also { TextureMapping.unshrink(m, it) }

    @Test
    fun aPictureThatFillsItsBufferMapsStraightAcross() {
        assertArrayEquals(floatArrayOf(0f, 1f, 1f, -1f), mapping(surfaceTextureMatrix(2560, 1600, 2560, 1600)), 0f)
    }

    @Test
    fun theCropShrinkIsTakenOut() {
        val m = mapping(surfaceTextureMatrix(2048, 1216, 1920, 1200))
        assertArrayEquals(floatArrayOf(0f, 1200f / 1216, 1920f / 2048, -1200f / 1216), m, 1e-6f)
    }

    @Test
    fun everyStreamPixelLandsOnTheCentreOfItsOwnTexel() {
        val bufW = 2048
        val bufH = 1216
        val m = mapping(surfaceTextureMatrix(bufW, bufH, 1920, 1200))
        for (x in 0 until 1920) {
            val t = (m[0] + m[2] * (x + 0.5f) / 1920) * bufW
            assertEquals("column $x", x.toDouble(), floor(t).toDouble(), 0.0)
            assertEquals("column $x centre", 0.5, (t - floor(t)).toDouble(), 1e-3)
        }
        // Bottom-up rows: the bottom stream row is the crop's last buffer row.
        for (y in 0 until 1200) {
            val t = (m[1] + m[3] * (y + 0.5f) / 1200) * bufH
            assertEquals("row $y", (1199 - y).toDouble(), floor(t).toDouble(), 0.0)
        }
    }
}
