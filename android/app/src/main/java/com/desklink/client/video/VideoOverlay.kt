package com.desklink.client.video

import android.opengl.GLES30

/**
 * Layers drawn above the video by [GuidedRenderer], at the stream's pixels,
 * such as exact tiles of a part of the desktop that stopped moving. It runs
 * on the renderer's thread with the renderer's GL context current.
 */
interface VideoOverlay {
    /**
     * Draws the overlay with [quads] after the video of every frame and of
     * every [GuidedRenderer.redraw]. [frameMicros] names the frame drawn: its
     * presentation time, which is the host's capture time of the frame, or 0
     * when the decoder did not give one. It may create and upload textures
     * here, and must leave the framebuffer, viewport and blending as it found
     * them.
     */
    fun draw(quads: OverlayQuads, frameMicros: Long)

    /**
     * The renderer's GL context is about to go away (the decoder was rebuilt
     * or the session ended), and every texture made in [draw] with it. The
     * next renderer calls [draw] in a new context.
     */
    fun release() {}
}

/** Draws textured quads at stream pixel coordinates, for a [VideoOverlay]. */
class OverlayQuads internal constructor(
    private val program: Int,
    private val rect: Int,
    private val maskSpan: Int,
    private val sampler: Int,
    private val maskSampler: Int,
) {
    /**
     * Draws the GL_TEXTURE_2D [texture] over the stream rectangle whose
     * top-left corner is [left], [top] pixels from the stream's top-left
     * corner, [width] × [height] pixels in size; a texture of that size lands
     * 1:1 on the stream's pixels, its first row at the top. The texture holds
     * premultiplied RGBA with the desktop's sRGB-encoded values, as the video
     * is drawn. It is sampled with GL_NEAREST whatever its own filter.
     *
     * [mask], a GL_TEXTURE_2D whose red channel is the opacity, spans
     * [maskWidth] × [maskHeight] stream pixels from the same corner, its
     * texels evenly spread, and is sampled with GL_LINEAR between texel
     * centres; the texture is multiplied by it.
     */
    fun draw(texture: Int, left: Int, top: Int, width: Int, height: Int, mask: Int, maskWidth: Int, maskHeight: Int) {
        GLES30.glUseProgram(program)
        GLES30.glUniform4f(rect, left.toFloat(), top.toFloat(), width.toFloat(), height.toFloat())
        GLES30.glUniform2f(maskSpan, maskWidth.toFloat(), maskHeight.toFloat())
        GLES30.glActiveTexture(GLES30.GL_TEXTURE0 + UNIT)
        GLES30.glBindTexture(GLES30.GL_TEXTURE_2D, texture)
        GLES30.glBindSampler(UNIT, sampler)
        GLES30.glActiveTexture(GLES30.GL_TEXTURE0 + MASK_UNIT)
        GLES30.glBindTexture(GLES30.GL_TEXTURE_2D, mask)
        GLES30.glBindSampler(MASK_UNIT, maskSampler)
        GLES30.glDrawArrays(GLES30.GL_TRIANGLE_STRIP, 0, 4)
    }

    internal companion object {
        /** The texture unit overlay quads use; the video passes use the ones below it. */
        const val UNIT = 3

        /** The texture unit of an overlay quad's mask. */
        const val MASK_UNIT = 4
    }
}
