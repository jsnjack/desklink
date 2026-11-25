package com.desklink.client.ui

import com.desklink.client.input.Zoom
import com.desklink.client.model.ScreenMode
import com.desklink.client.model.SessionInfo
import com.desklink.client.model.StreamSize
import kotlin.math.roundToInt

/** A rectangle in physical pixels of the stream view. */
data class VideoRect(val x: Int, val y: Int, val width: Int, val height: Int)

/**
 * How much a stream that fits the view may grow to fill it before it is
 * shown pixel for pixel with a thin border instead: resampling blurs text,
 * and a few percent larger gains nothing.
 */
const val SNAP_GROWTH = 0.05

/**
 * Where a stream of [streamWidth] x [streamHeight] lands in a view of
 * [viewWidth] x [viewHeight], centred and as large as fits without changing
 * its shape; one that fits and would grow by less than [SNAP_GROWTH] keeps
 * its own size, so it shows pixel for pixel.
 */
fun videoRect(viewWidth: Int, viewHeight: Int, streamWidth: Int, streamHeight: Int): VideoRect {
    if (viewWidth <= 0 || viewHeight <= 0 || streamWidth <= 0 || streamHeight <= 0) {
        return VideoRect(0, 0, viewWidth.coerceAtLeast(0), viewHeight.coerceAtLeast(0))
    }
    val fit = minOf(viewWidth.toDouble() / streamWidth, viewHeight.toDouble() / streamHeight)
    val scale = if (fit >= 1 && fit < 1 + SNAP_GROWTH) 1.0 else fit
    val w = (streamWidth * scale).roundToInt().coerceAtMost(viewWidth)
    val h = (streamHeight * scale).roundToInt().coerceAtMost(viewHeight)
    return VideoRect((viewWidth - w) / 2, (viewHeight - h) / 2, w, h)
}

/**
 * Where the decoder's output lands on the screen: the video placed at
 * [placed], its SurfaceView's buffer [surface] pixels as surfaceChanged
 * reported it (the placed size until then), enlarged and moved by the pinch
 * [zoom], whose offsets are fractions of the video's size.
 */
fun shownRect(placed: VideoRect, surface: StreamSize?, zoom: Zoom): VideoRect {
    val w = surface?.width ?: placed.width
    val h = surface?.height ?: placed.height
    return VideoRect(
        placed.x + (zoom.x * w).roundToInt(), placed.y + (zoom.y * h).roundToInt(),
        (w * zoom.scale).roundToInt(), (h * zoom.scale).roundToInt(),
    )
}

/**
 * The size the host captures: the own screen's mode, which is the top of the
 * quality ladder, or the mirrored monitor's mode; null when the host does
 * not say (a mirroring host from before the monitor list).
 */
fun captureSize(info: SessionInfo): StreamSize? = when (info.screen) {
    ScreenMode.OWN -> info.levels.firstOrNull()?.let { StreamSize(it.width, it.height) } ?: StreamSize(info.width, info.height)
    else -> info.monitors.firstOrNull { it.connector == info.mirroring }?.let { StreamSize(it.width, it.height) }
}

/**
 * How much the screen scales the encoded picture of [encoded] pixels that
 * lands on [shown], or null when it lands pixel for pixel.
 */
fun shownScale(shown: VideoRect, encoded: StreamSize): Double? =
    if (shown.width == encoded.width && shown.height == encoded.height) null else shown.width.toDouble() / encoded.width

/**
 * Decides when the own screen follows the stream view's size, as when the
 * tablet rotates. A rotation can report a size or two on its way, and every
 * request makes the host start the screen again, so a size is asked for
 * only once it has held for [quietMs]. A size the screen has already, or
 * that was asked for and not yet applied, is not asked again.
 */
class ScreenResizer(private val quietMs: Long, private val now: () -> Long) {
    private var wanted: StreamSize? = null
    private var since = 0L
    private var asked: StreamSize? = null

    /** The stream view now wants a stream of [size]. */
    fun viewChanged(size: StreamSize) {
        if (size == wanted) return
        wanted = size
        since = now()
    }

    /** The size to ask the host for now, given the own screen's size [screen], or null. */
    fun due(screen: StreamSize): StreamSize? {
        val want = wanted ?: return null
        if (want == screen) {
            asked = null
            return null
        }
        if (now() - since < quietMs || want == asked) return null
        asked = want
        return want
    }
}
