package com.desklink.client.video

import kotlin.math.max
import kotlin.math.min

/**
 * Where the decoded picture lies in its texture, for sampling one texel per
 * stream pixel. SurfaceTexture's transform matrix maps the picture (0..1,
 * bottom-up like GL) into its buffer; for a buffer larger than the picture
 * it also pulls each cropped edge one texel inwards, so that bilinear
 * filtering never reads past the crop. That shrink stretches the picture by
 * up to a texel across its width, which would put every sample between two
 * texels. [unshrink] takes it out again.
 */
object TextureMapping {
    /**
     * Writes into [out] the offset x, offset y, scale x and scale y with
     * which texture coordinate = offset + scale × picture coordinate, from
     * the column-major 4×4 [matrix] of SurfaceTexture.getTransformMatrix,
     * without the crop shrink. It assumes that the decoder crops from the
     * buffer's top-left corner, as decoders do, and that the matrix neither
     * rotates nor mirrors sideways, which a decoder's output never does.
     */
    fun unshrink(matrix: FloatArray, out: FloatArray) {
        axis(matrix[12], matrix[0], out, 0)
        axis(matrix[13], matrix[5], out, 1)
    }

    private fun axis(offset: Float, scale: Float, out: FloatArray, i: Int) {
        // The picture spans lo..hi in texture coordinates, flipped when the
        // scale is negative. A crop from the corner starts at 0, so a lo above
        // 0 is the shrink, which hi lost too.
        val lo = min(offset, offset + scale)
        val hi = max(offset, offset + scale)
        val shrink = max(lo, 0f)
        val start = lo - shrink
        val end = hi + shrink
        if (scale >= 0f) {
            out[i] = start
            out[i + 2] = end - start
        } else {
            out[i] = end
            out[i + 2] = start - end
        }
    }
}
