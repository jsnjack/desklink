package com.desklink.client.input

import com.desklink.client.input.TouchGestureEngine.Point
import com.desklink.client.model.InputKind
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlin.math.abs
import kotlin.math.hypot

/**
 * How the video is zoomed on the tablet: [scale] times larger, with the
 * zoomed video's top-left corner at [x], [y] in fractions of the view. The
 * offsets stay between 1 - [scale] and 0, so the video always covers the
 * view. The view point at fraction f shows the video point (f - offset) / scale.
 */
data class Zoom(val scale: Float = 1f, val x: Float = 0f, val y: Float = 0f) {
    /** Whether the video is enlarged, so that two fingers pan it instead of scrolling the desktop. */
    val zoomed: Boolean get() = scale > 1f

    /** The absolute pointer x (0..ABS_MAX across the video) under view pixel [px] of a view [width] pixels wide. */
    fun absX(px: Float, width: Int): Int = toAbs((px / width - x) / scale)

    /** The absolute pointer y (0..ABS_MAX down the video) under view pixel [py] of a view [height] pixels high. */
    fun absY(py: Float, height: Int): Int = toAbs((py / height - y) / scale)

    private fun toAbs(f: Float): Int = (f * InputKind.ABS_MAX).toInt().coerceIn(0, InputKind.ABS_MAX)

    /**
     * The zoom after two fingers moved their centre from [fromX], [fromY] to
     * [toX], [toY] (fractions of the view) and changed the distance between
     * them by [ratio]: the video point under the old centre stays under the
     * new one, as far as the video still covers the view.
     */
    fun moved(fromX: Float, fromY: Float, toX: Float, toY: Float, ratio: Float): Zoom {
        var s = (scale * ratio).coerceIn(1f, MAX_SCALE)
        // A zoom just above 1x would turn two-finger scrolls into pans of a
        // video that hardly moves.
        if (s < SNAP_SCALE) s = 1f
        val nx = toX - (fromX - x) / scale * s
        val ny = toY - (fromY - y) / scale * s
        return Zoom(s, nx.coerceIn(1f - s, 0f), ny.coerceIn(1f - s, 0f))
    }

    /** Zoom limits. */
    companion object {
        /** The largest zoom: 4x. */
        const val MAX_SCALE = 4f

        /** A pinch that ends below this zoom shows the whole video again. */
        const val SNAP_SCALE = 1.05f
    }
}

/**
 * The video's local zoom, shared by the touch engines that pinch it and by
 * every input that points at the video, which maps through it. Only a
 * mirrored monitor zooms: the own screen is drawn 1:1 for the tablet.
 */
class VideoZoom {
    private val _state = MutableStateFlow(Zoom())

    /** The zoom now; the stream screen draws the video with it. */
    val state: StateFlow<Zoom> = _state

    /** The zoom that input maps through now. */
    val current: Zoom get() = _state.value

    /** Whether pinches zoom the video; turning it on or off shows the whole video again. */
    var enabled = false
        set(value) {
            if (field == value) return
            field = value
            reset()
        }

    /** Shows the whole video again. */
    fun reset() {
        _state.value = Zoom()
    }

    /** Shows [zoom], while zooming is enabled. */
    internal fun show(zoom: Zoom) {
        if (enabled) _state.value = zoom
    }
}

/**
 * Tells a pinch from a two-finger scroll and makes a pinch zoom and pan the
 * video. While the video is zoomed, every two-finger movement pans or zooms
 * it. At 1x the fingers pinch when the distance between them changes by
 * [PINCH_SLOP] pixels before their centre moves past the slop, or by more
 * than their centre moved (one finger held still while the other moves);
 * when their centre moves past the slop first, they scroll.
 */
internal class TwoFingerGesture(private val zoom: VideoZoom, private val slop: Float) {
    /** What two fingers do in this gesture. */
    enum class Mode { UNDECIDED, SCROLL, ZOOM }

    /** What the fingers do; once decided, it holds until the gesture ends. */
    var mode = Mode.UNDECIDED
        private set
    private var startCentre = Point(0f, 0f)
    private var startSpan = 0f
    private var startZoom = Zoom()

    /** Whether two fingers may zoom now; otherwise the engines scroll as they always did. */
    val active: Boolean get() = zoom.enabled

    /** Starts a new gesture with nothing decided. */
    fun reset() {
        mode = Mode.UNDECIDED
    }

    /** Two fingers are down from now on: their movement counts from [points]. */
    fun start(points: List<Point>) {
        if (points.size != 2) return
        startCentre = centre(points)
        startSpan = span(points)
        startZoom = zoom.current
    }

    /** The two fingers moved to [points] on a view of [width] x [height] pixels; returns what they do. */
    fun move(points: List<Point>, width: Int, height: Int): Mode {
        if (points.size != 2) return mode
        val c = centre(points)
        val s = span(points)
        if (mode == Mode.UNDECIDED) {
            val travel = hypot(c.x - startCentre.x, c.y - startCentre.y)
            val change = abs(s - startSpan)
            mode = when {
                startZoom.zoomed && (travel > slop || change > PINCH_SLOP) -> Mode.ZOOM
                change > PINCH_SLOP || (travel > slop && change > travel) -> Mode.ZOOM
                travel > slop -> Mode.SCROLL
                else -> Mode.UNDECIDED
            }
        }
        // Measured from where the fingers landed, so the video stays under
        // them however many moves the pinch took.
        if (mode == Mode.ZOOM && startSpan > 0f) {
            zoom.show(startZoom.moved(startCentre.x / width, startCentre.y / height, c.x / width, c.y / height, s / startSpan))
        }
        return mode
    }

    private fun centre(p: List<Point>) = Point((p[0].x + p[1].x) / 2, (p[0].y + p[1].y) / 2)

    private fun span(p: List<Point>) = hypot(p[0].x - p[1].x, p[0].y - p[1].y)

    companion object {
        /**
         * How much the distance between two fingers changes before they
         * pinch, in view pixels: about 3 mm on the Tab S9's 274 ppi screen,
         * twice the Direct engine's slop, so a two-finger tap's jitter still
         * right-clicks.
         */
        const val PINCH_SLOP = 32f
    }
}
