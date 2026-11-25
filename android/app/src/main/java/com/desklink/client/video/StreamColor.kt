package com.desklink.client.video

/**
 * The stream's colour encoding, as the host writes it (streamColorimetry in
 * host/internal/video/encoder.go): the desktop's sRGB-encoded R'G'B' values
 * turned into 10-bit limited-range Y'CbCr with the BT.709 matrix, the sRGB
 * transfer left as it is. [GuidedRenderer] undoes exactly this and writes
 * R'G'B' unchanged, as the direct path's decoder does with
 * COLOR_TRANSFER_SRGB, so neither re-curves the values.
 */
object StreamColor {
    /** BT.709's red weight in luma. */
    const val KR = 0.2126

    /** BT.709's blue weight in luma. */
    const val KB = 0.0722

    /** BT.709's green weight in luma. */
    const val KG = 1 - KR - KB

    /** The 10-bit luma code of black. */
    const val LUMA_BLACK = 64

    /** The 10-bit luma codes from black to white (64..940). */
    const val LUMA_SPAN = 876

    /** The 10-bit chroma code of no colour. */
    const val CHROMA_ZERO = 512

    /** The 10-bit chroma codes from -0.5 to +0.5 (64..960). */
    const val CHROMA_SPAN = 896

    /**
     * What a sampler divides a 10-bit code by. A P010 sample read as a
     * 16-bit value divides by 1023.98 instead; that moves white by a quarter
     * of an 8-bit level, which still rounds to 255.
     */
    const val CODE_SCALE = 1023.0

    /**
     * The shader's range uniform: offset and gain taking a sampled luma value
     * v to Y' = (v - offset) * gain in 0..1, then the same for chroma to
     * Cb, Cr in -0.5..0.5. Constants from the 10-bit codes, not 16/255 and
     * 219/255, because a 10-bit sampler's white (940/1023) with the 8-bit
     * constants decodes to 254.
     */
    val range: FloatArray = floatArrayOf(
        (LUMA_BLACK / CODE_SCALE).toFloat(),
        (CODE_SCALE / LUMA_SPAN).toFloat(),
        (CHROMA_ZERO / CODE_SCALE).toFloat(),
        (CODE_SCALE / CHROMA_SPAN).toFloat(),
    )

    /**
     * The 3×3 matrix taking (Y', Cb, Cr) to (R', G', B'), column-major as
     * glUniformMatrix3fv takes it: R' = Y' + 2(1-Kr)Cr, B' = Y' + 2(1-Kb)Cb,
     * and G' from the luma equation.
     */
    val toRgb: FloatArray = run {
        val crToR = 2 * (1 - KR)
        val cbToB = 2 * (1 - KB)
        val cbToG = -KB * cbToB / KG
        val crToG = -KR * crToR / KG
        floatArrayOf(
            1f, 1f, 1f,
            0f, cbToG.toFloat(), cbToB.toFloat(),
            crToR.toFloat(), crToG.toFloat(), 0f,
        )
    }
}
