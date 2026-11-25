package com.desklink.client.video

import kotlin.math.max
import kotlin.math.min

/**
 * The stream's colour encoding, as the host writes it (streamColorimetry in
 * host/internal/video/encoder.go): the desktop's sRGB-encoded R'G'B' values
 * turned into 10-bit limited-range Y'CbCr with the BT.709 matrix, the sRGB
 * transfer left as it is. [GuidedRenderer] undoes exactly this and writes
 * R'G'B' unchanged, as the direct path's decoder does with
 * COLOR_TRANSFER_SRGB, so neither re-curves the values.
 *
 * The host's converter writes every code [SOURCE_SCALE] times the standard
 * one, which this undoes too, so that the video's greys match the exact
 * tiles drawn over it (docs/android.md, "Exact text").
 */
object StreamColor {
    /** BT.709's red weight in luma. */
    const val KR = 0.2126

    /** BT.709's blue weight in luma. */
    const val KB = 0.0722

    /** BT.709's green weight in luma. */
    const val KG = 1 - KR - KB

    /** The standard 10-bit luma code of black. */
    const val LUMA_BLACK = 64

    /** The standard 10-bit luma codes from black to white (64..940). */
    const val LUMA_SPAN = 876

    /** The standard 10-bit chroma code of no colour. */
    const val CHROMA_ZERO = 512

    /** The standard 10-bit chroma codes from -0.5 to +0.5 (64..960). */
    const val CHROMA_SPAN = 896

    /**
     * How much larger than the standard codes the host's codes are. Mesa's
     * vapostproc (radeonsi) writes 8-bit studio-range levels into P010's
     * 16-bit samples (black 16 as 0x1010, no colour 128 as 0x8080), so a
     * grey's chroma is 514 instead of 512 and, read as standard, comes back
     * a level too red and too blue; host/internal/video's
     * TestConversionKeepsDesktopColours pins it.
     */
    const val SOURCE_SCALE = 257.0 / 256

    /**
     * The chroma codes above [CHROMA_ZERO] that read as no colour: the host
     * writes a grey's chroma as 514, but the encoder rounds flat areas,
     * predicted from the frame's edge, to 512.
     */
    const val NEUTRAL_BAND = CHROMA_ZERO * (SOURCE_SCALE - 1)

    /**
     * What a sampler divides a 10-bit code by. A P010 sample read as a
     * 16-bit value divides by 1023.98 instead; that moves white by a quarter
     * of an 8-bit level, which still rounds to 255.
     */
    const val CODE_SCALE = 1023.0

    /**
     * The shader's range uniform: offset and gain taking a sampled luma value
     * v to Y' = (v - offset) * gain in 0..1, then what takes a sampled chroma
     * value back to its code and the gain from code to Cb or Cr ([chroma]).
     * Constants from the 10-bit codes, not 16/255 and 219/255, because a
     * 10-bit sampler's white (940/1023) with the 8-bit constants decodes to
     * 254. The encoder keeps the top 10 bits of the converter's 16, so luma
     * codes fall up to a code short; black sits half a code low to centre
     * them (else a 16-bit sampler's grey 220 decodes to 219).
     */
    val range: FloatArray = floatArrayOf(
        ((LUMA_BLACK * SOURCE_SCALE - 0.5) / CODE_SCALE).toFloat(),
        (CODE_SCALE / (LUMA_SPAN * SOURCE_SCALE)).toFloat(),
        CODE_SCALE.toFloat(),
        (1 / (CHROMA_SPAN * SOURCE_SCALE)).toFloat(),
    )

    /**
     * Cb or Cr in -0.5..0.5 from a 10-bit [code] as the host's converter
     * wrote it: the codes [NEUTRAL_BAND] wide above [CHROMA_ZERO] are no
     * colour, those beyond are scaled back by [SOURCE_SCALE], and the band
     * as wide below closes the gap, so the mapping has no steps. The stats
     * shader ([GuidedShaders.STATS]) does the same arithmetic.
     */
    fun chroma(code: Double): Double {
        val d = code - CHROMA_ZERO
        return (max(d - NEUTRAL_BAND, 0.0) + min(d, 0.0) + max(min(d, 0.0), -NEUTRAL_BAND)) * range[3]
    }

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
