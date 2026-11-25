package com.desklink.client.video

import android.opengl.GLES30
import android.util.Log
import android.view.Choreographer
import com.desklink.client.model.TileWire
import java.nio.ByteBuffer

/**
 * Draws exact tiles over the video ([VideoOverlay]): one RGBA texture of the
 * stream's size holds every tile at its place and is transparent elsewhere,
 * so a frame costs one quad however many tiles there are. Each tile is
 * uploaded once; a tile that goes is cleared with a scissored clear of the
 * texture, which is free on the GPU. The quad goes through the renderer's
 * own transform, so the pinch zoom moves tiles with the video.
 *
 * A one-byte-a-block mask ([TileMask]) fades new tiles in and blends their
 * edges into the video next to them; while a tile fades in, the overlay
 * asks for a redraw at every refresh of the display.
 *
 * [onLost] is called when the GL context went with tiles drawn, so the host
 * must send them again; [redraw] when tiles wait for an upload that did not
 * fit the last frame, or are fading in.
 */
class TileOverlay(
    val layer: TileLayer,
    private val onLost: () -> Unit,
    private val redraw: () -> Unit,
) : VideoOverlay {
    private val width = layer.grid.width
    private val height = layer.grid.height
    private val plan = TilePlan(layer.grid.count)
    private val mask = TileMask(layer.grid)
    private val maskPixels = ByteBuffer.wrap(mask.alpha)
    private var texture = 0
    private var maskTexture = 0
    private var framebuffer = 0
    private val ids = IntArray(2)

    // Choreographer is the render thread's own: draw and release run there.
    private var animating = false
    private val nextRefresh = Choreographer.FrameCallback {
        animating = false
        redraw()
    }

    override fun draw(quads: OverlayQuads, frameMicros: Long) {
        layer.present(frameMicros, plan)
        if (texture == 0 && !plan.draw) return
        if (texture == 0) setUp()
        if (plan.clearAll || plan.clearCount > 0) clear()
        if (plan.uploadCount > 0) upload()
        val now = System.nanoTime()
        mask.apply(plan, now)
        val fading = mask.update(now)
        if (mask.dirty) uploadMask()
        if (plan.draw) quads.draw(texture, 0, 0, width, height, maskTexture, mask.width * TileMask.BLOCK, mask.height * TileMask.BLOCK)
        if (plan.more) redraw()
        if (fading && !animating) {
            animating = true
            Choreographer.getInstance().postFrameCallback(nextRefresh)
        }
    }

    private fun setUp() {
        GLES30.glGenTextures(2, ids, 0)
        texture = ids[0]
        maskTexture = ids[1]
        GLES30.glActiveTexture(GLES30.GL_TEXTURE0 + OverlayQuads.UNIT)
        GLES30.glBindTexture(GLES30.GL_TEXTURE_2D, maskTexture)
        GLES30.glTexStorage2D(GLES30.GL_TEXTURE_2D, 1, GLES30.GL_R8, mask.width, mask.height)
        uploadMask(0, 0, mask.width, mask.height)
        GLES30.glBindTexture(GLES30.GL_TEXTURE_2D, texture)
        GLES30.glTexStorage2D(GLES30.GL_TEXTURE_2D, 1, GLES30.GL_RGBA8, width, height)
        GLES30.glGenFramebuffers(1, ids, 0)
        framebuffer = ids[0]
        GLES30.glBindFramebuffer(GLES30.GL_FRAMEBUFFER, framebuffer)
        GLES30.glFramebufferTexture2D(GLES30.GL_FRAMEBUFFER, GLES30.GL_COLOR_ATTACHMENT0, GLES30.GL_TEXTURE_2D, texture, 0)
        val status = GLES30.glCheckFramebufferStatus(GLES30.GL_FRAMEBUFFER)
        if (status != GLES30.GL_FRAMEBUFFER_COMPLETE) Log.w(TAG, "tile texture is not renderable: 0x${status.toString(16)}")
        GLES30.glClearColor(0f, 0f, 0f, 0f)
        GLES30.glClear(GLES30.GL_COLOR_BUFFER_BIT)
        GLES30.glBindFramebuffer(GLES30.GL_FRAMEBUFFER, 0)
    }

    // Clears the plan's slots, a run of neighbours in a row at a time, through
    // a framebuffer on the texture; framebuffer row 0 is the texture's first
    // row, the top of the stream.
    private fun clear() {
        GLES30.glBindFramebuffer(GLES30.GL_FRAMEBUFFER, framebuffer)
        if (plan.clearAll) {
            GLES30.glClear(GLES30.GL_COLOR_BUFFER_BIT)
        } else {
            GLES30.glEnable(GLES30.GL_SCISSOR_TEST)
            val grid = layer.grid
            var i = 0
            while (i < plan.clearCount) {
                val first = plan.clears[i]
                var last = first
                while (i + 1 < plan.clearCount && plan.clears[i + 1] == last + 1 && (last + 1) % grid.cols != 0) last = plan.clears[++i]
                val x = grid.x(first)
                val y = grid.y(first)
                val right = minOf(width, grid.x(last) + TileWire.TILE_SIZE)
                val bottom = minOf(height, y + TileWire.TILE_SIZE)
                GLES30.glScissor(x, y, right - x, bottom - y)
                GLES30.glClear(GLES30.GL_COLOR_BUFFER_BIT)
                i++
            }
            GLES30.glDisable(GLES30.GL_SCISSOR_TEST)
        }
        GLES30.glBindFramebuffer(GLES30.GL_FRAMEBUFFER, 0)
    }

    private fun upload() {
        GLES30.glActiveTexture(GLES30.GL_TEXTURE0 + OverlayQuads.UNIT)
        GLES30.glBindTexture(GLES30.GL_TEXTURE_2D, texture)
        for (i in 0 until plan.uploadCount) {
            val t = plan.uploads[i] ?: continue
            GLES30.glTexSubImage2D(GLES30.GL_TEXTURE_2D, 0, t.x, t.y, t.w, t.h, GLES30.GL_RGBA, GLES30.GL_UNSIGNED_BYTE, ByteBuffer.wrap(t.rgba))
        }
    }

    private fun uploadMask() {
        GLES30.glActiveTexture(GLES30.GL_TEXTURE0 + OverlayQuads.UNIT)
        GLES30.glBindTexture(GLES30.GL_TEXTURE_2D, maskTexture)
        uploadMask(mask.dirtyLeft, mask.dirtyTop, mask.dirtyRight, mask.dirtyBottom)
    }

    // Uploads blocks [left] until [right], [top] until [bottom] of the mask
    // into the bound texture, then puts the unpack state back to GL's defaults.
    private fun uploadMask(left: Int, top: Int, right: Int, bottom: Int) {
        GLES30.glPixelStorei(GLES30.GL_UNPACK_ALIGNMENT, 1)
        GLES30.glPixelStorei(GLES30.GL_UNPACK_ROW_LENGTH, mask.width)
        GLES30.glPixelStorei(GLES30.GL_UNPACK_SKIP_PIXELS, left)
        GLES30.glPixelStorei(GLES30.GL_UNPACK_SKIP_ROWS, top)
        GLES30.glTexSubImage2D(GLES30.GL_TEXTURE_2D, 0, left, top, right - left, bottom - top, GLES30.GL_RED, GLES30.GL_UNSIGNED_BYTE, maskPixels)
        GLES30.glPixelStorei(GLES30.GL_UNPACK_ALIGNMENT, 4)
        GLES30.glPixelStorei(GLES30.GL_UNPACK_ROW_LENGTH, 0)
        GLES30.glPixelStorei(GLES30.GL_UNPACK_SKIP_PIXELS, 0)
        GLES30.glPixelStorei(GLES30.GL_UNPACK_SKIP_ROWS, 0)
    }

    override fun release() {
        // The textures and framebuffer die with the context.
        texture = 0
        maskTexture = 0
        framebuffer = 0
        if (animating) Choreographer.getInstance().removeFrameCallback(nextRefresh)
        animating = false
        mask.reset()
        if (layer.textureLost()) onLost()
    }

    private companion object {
        const val TAG = "DeskLink"
    }
}
