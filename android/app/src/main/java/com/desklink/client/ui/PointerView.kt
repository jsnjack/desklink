package com.desklink.client.ui

import android.annotation.SuppressLint
import android.content.Context
import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.Paint
import android.graphics.Path
import android.os.Looper
import android.util.Base64
import android.view.View
import com.desklink.client.input.LocalPointer
import com.desklink.client.model.CursorShape
import com.desklink.client.model.InputKind
import java.nio.ByteBuffer

/**
 * Draws the desk's pointer over the video ([LocalPointer]), for a session
 * that leaves it out of the video. It covers the video's area and moves by
 * its translation, a property of its render node, so following the input
 * redraws nothing but the frame that shows it. Until the desk sends a shape
 * it draws an arrow.
 */
@SuppressLint("ViewConstructor")
class PointerView(context: Context, private val pointer: LocalPointer, private val streamWidth: () -> Int) : View(context) {
    private var drawn: CursorShape? = null
    private var bitmap: Bitmap? = null
    private val paint = Paint(Paint.FILTER_BITMAP_FLAG)
    private val arrowFill = Paint(Paint.ANTI_ALIAS_FLAG).apply { color = Color.WHITE }
    private val arrowEdge = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        color = Color.BLACK
        style = Paint.Style.STROKE
        strokeWidth = 1.5f
    }
    private val arrow = Path().apply {
        moveTo(0f, 0f)
        lineTo(0f, 17f)
        lineTo(4.5f, 13f)
        lineTo(7.5f, 19.5f)
        lineTo(10f, 18.5f)
        lineTo(7f, 12f)
        lineTo(12.5f, 12f)
        close()
    }
    private val update = Runnable { place() }

    init {
        isClickable = false
        isFocusable = false
        importantForAccessibility = IMPORTANT_FOR_ACCESSIBILITY_NO
    }

    override fun onAttachedToWindow() {
        super.onAttachedToWindow()
        pointer.listener = {
            if (Looper.myLooper() == Looper.getMainLooper()) place() else post(update)
        }
        place()
    }

    override fun onDetachedFromWindow() {
        pointer.listener = null
        removeCallbacks(update)
        super.onDetachedFromWindow()
    }

    override fun onSizeChanged(w: Int, h: Int, oldw: Int, oldh: Int) = place()

    /** How many view pixels one of the shape's pixels covers. */
    private fun scale(s: CursorShape?): Float {
        val span = s?.span?.takeIf { it > 0 } ?: (1.0 / streamWidth().coerceAtLeast(1))
        return (span * width).toFloat()
    }

    /** Moves the drawing to the pointer, and redraws it when its shape changed. */
    private fun place() {
        val s = pointer.shape
        if (s !== drawn) {
            drawn = s
            bitmap?.recycle()
            bitmap = s?.let(::toBitmap)
            invalidate()
        }
        val k = scale(s)
        val px = pointer.x / InputKind.ABS_MAX * width
        val py = pointer.y / InputKind.ABS_MAX * height
        translationX = px - (s?.hotX ?: 0) * k
        translationY = py - (s?.hotY ?: 0) * k
    }

    override fun onDraw(canvas: Canvas) {
        val s = drawn
        val b = bitmap
        val k = scale(s)
        canvas.save()
        canvas.scale(k, k)
        if (s != null && b != null) {
            canvas.drawBitmap(b, 0f, 0f, paint)
        } else if (s == null) {
            // About the size of GNOME's arrow at the scales tablets use.
            canvas.scale(ARROW_SCALE, ARROW_SCALE)
            canvas.drawPath(arrow, arrowFill)
            canvas.drawPath(arrow, arrowEdge)
        }
        canvas.restore()
    }

    private companion object {
        const val ARROW_SCALE = 1.6f

        /** The shape's premultiplied RGBA as a bitmap, whose memory is premultiplied RGBA too; null if malformed. */
        fun toBitmap(s: CursorShape): Bitmap? {
            if (s.w <= 0 || s.h <= 0) return null
            val px = runCatching { Base64.decode(s.rgba, Base64.DEFAULT) }.getOrNull() ?: return null
            if (px.size != s.w * s.h * 4) return null
            return Bitmap.createBitmap(s.w, s.h, Bitmap.Config.ARGB_8888).apply {
                copyPixelsFromBuffer(ByteBuffer.wrap(px))
            }
        }
    }
}
