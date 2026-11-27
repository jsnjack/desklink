package com.desklink.client.input

import com.desklink.client.input.TouchGestureEngine.Action
import com.desklink.client.input.TouchGestureEngine.Point
import com.desklink.client.model.InputEvent
import com.desklink.client.model.InputKind
import kotlin.math.hypot

/**
 * Touchscreen input that acts where the finger is, as on a phone: a tap
 * clicks there, one or two fingers dragged scroll the window under them (the
 * content follows the fingers, [FingerScroll]), a press held still for
 * [LONG_PRESS_MS] right-clicks when it lifts, and the same press dragged
 * after that drags with the left button (selects text, moves windows). Two
 * fingers tapped right-click, a three-finger tap toggles the HUD, and three
 * fingers swiped up, left or right send GNOME's overview and workspace
 * shortcuts ([Swipe]). While the zoom is enabled, two fingers pinch to zoom
 * the video and, once it is zoomed, pan it instead of scrolling
 * ([TwoFingerGesture]). Positions go out as absolute pointer records,
 * 0..ABS_MAX across the video, at the point of the video shown under the
 * finger. It has no Android dependencies so the gesture rules are
 * unit-tested.
 */
class DirectTouchEngine(
    private val emit: (List<InputEvent>) -> Unit,
    private val onThreeFingerTap: () -> Unit,
    private val slop: Float = 16f,
    /** The video's local zoom: positions map through it, and pinches change it. */
    private val zoom: VideoZoom = VideoZoom(),
    /** Whether the host scrolls like a touchpad ([FingerScroll]). */
    scrollPixels: () -> Boolean = { false },
) {
    /** What the moving fingers do; decided when they first move past the slop. */
    private enum class Mode { NONE, SCROLL, DRAG }

    private val swipe = SwipeTracker()
    private val pinch = TwoFingerGesture(zoom, slop)
    private val scroll = FingerScroll(scrollPixels)
    private var width = 1
    private var height = 1
    private var downTime = 0L
    private var maxPointers = 0
    private var moved = false
    private var mode = Mode.NONE

    /** Set once two fingers zoomed or panned the video: the gesture stays on the tablet. */
    private var zoomed = false
    private var anchor = Point(0f, 0f)
    private var last = Point(0f, 0f)
    private var lastTapUp = Long.MIN_VALUE / 2
    private var lastTapAt = Point(0f, 0f)

    /**
     * Whether the touch that went down at [downTime] is still one finger held
     * where it landed, so lifting it now right-clicks and dragging it now
     * drags; the caller confirms the hold with a vibration once
     * [LONG_PRESS_MS] have passed.
     */
    fun holdingStill(downTime: Long): Boolean =
        maxPointers == 1 && this.downTime == downTime && !moved && mode == Mode.NONE

    /** Feeds one touch event on a view of [width] x [height] pixels. */
    fun onTouch(action: Action, points: List<Point>, timeMs: Long, width: Int, height: Int) {
        this.width = width.coerceAtLeast(1)
        this.height = height.coerceAtLeast(1)
        val c = centroid(points)
        when (action) {
            Action.DOWN -> {
                reset()
                downTime = timeMs
                maxPointers = 1
                anchor = c
                last = c
            }
            Action.POINTER_DOWN -> onPointerDown(points, c)
            // The anchor stays between all the fingers, so a two-finger tap
            // right-clicks there, not under the finger lifted last; the
            // fingers left go on scrolling from where they are.
            Action.POINTER_UP -> last = c
            Action.MOVE -> onMove(points, c, timeMs)
            Action.UP -> onUp(timeMs)
            Action.CANCEL -> {
                finish()
                reset()
            }
        }
    }

    private fun onPointerDown(points: List<Point>, c: Point) {
        maxPointers = maxOf(maxPointers, points.size)
        when {
            // GNOME's gestures take three fingers; whatever they did ends.
            maxPointers >= 3 -> {
                finish()
                mode = Mode.NONE
            }
            mode == Mode.DRAG -> {
                // A second finger ends the drag and scrolls from here.
                emit(listOf(pointer(last), InputEvent.button(InputKind.BUTTON_LEFT, false)))
                mode = Mode.SCROLL
            }
        }
        if (!moved) anchor = c
        if (points.size == 2) pinch.start(points)
        last = c
    }

    private fun onMove(points: List<Point>, c: Point, timeMs: Long) {
        val count = points.size
        if (!moved && dist(c, anchor) > slop) moved = true
        if (maxPointers >= 3) {
            // GNOME's gestures: a swipe's shortcut reaches the desktop, the
            // fingers' movement never does.
            if (count >= 3) swipe.move(last, c)?.let { emit(it.taps()) }
            last = c
            return
        }
        if (count == 2 && pinch.active) {
            when (pinch.move(points, width, height)) {
                TwoFingerGesture.Mode.UNDECIDED -> return
                // Zooming and panning the video stays on the tablet.
                TwoFingerGesture.Mode.ZOOM -> {
                    if (mode == Mode.SCROLL) emitAny(scroll.end())
                    mode = Mode.NONE
                    zoomed = true
                    moved = true
                    return
                }
                TwoFingerGesture.Mode.SCROLL -> moved = true
            }
        }
        if (zoomed || !moved) return
        when (mode) {
            Mode.DRAG -> emit(listOf(pointer(c)))
            Mode.SCROLL -> emitAny(scroll.move(c.x - last.x, c.y - last.y))
            Mode.NONE -> if (count == 1 && maxPointers == 1 && timeMs - downTime >= LONG_PRESS_MS) {
                // Held, then dragged: the drag starts where the finger went
                // down, so text selection starts at the right letter.
                mode = Mode.DRAG
                emit(listOf(pointer(anchor), InputEvent.button(InputKind.BUTTON_LEFT, true), pointer(c)))
            } else {
                // Scroll the window under the fingers, where they went down:
                // the scroll goes to the pointer's window.
                mode = Mode.SCROLL
                emit(listOf(pointer(last)) + scroll.move(c.x - last.x, c.y - last.y))
            }
        }
        last = c
    }

    private fun onUp(timeMs: Long) {
        val held = timeMs - downTime
        when {
            mode != Mode.NONE -> finish()
            moved -> Unit // a zoom, pan or swipe ended
            maxPointers == 1 && held >= LONG_PRESS_MS -> emit(click(InputKind.BUTTON_RIGHT, anchor))
            maxPointers == 1 -> {
                // A second tap near the first reuses its position, so the
                // desktop sees a double click despite finger jitter.
                val near = timeMs - lastTapUp <= TouchGestureEngine.DOUBLE_TAP_MS &&
                    dist(anchor, lastTapAt) <= TouchGestureEngine.DOUBLE_TAP_SLOP
                val at = if (near) lastTapAt else anchor
                emit(click(InputKind.BUTTON_LEFT, at))
                lastTapUp = timeMs
                lastTapAt = at
            }
            maxPointers == 2 && held < LONG_PRESS_MS -> emit(click(InputKind.BUTTON_RIGHT, anchor))
            maxPointers >= 3 && held < TouchGestureEngine.THREE_FINGER_TAP_MS -> onThreeFingerTap()
        }
        reset()
    }

    /** Ends a drag or scroll in progress: releases the button, or lets the scroll run on. */
    private fun finish() {
        when (mode) {
            Mode.DRAG -> emit(listOf(pointer(last), InputEvent.button(InputKind.BUTTON_LEFT, false)))
            Mode.SCROLL -> emitAny(scroll.end())
            Mode.NONE -> Unit
        }
    }

    /** Sends [events] unless there are none: a scroll under one unit sends nothing. */
    private fun emitAny(events: List<InputEvent>) {
        if (events.isNotEmpty()) emit(events)
    }

    private fun click(button: Int, at: Point) =
        listOf(pointer(at), InputEvent.button(button, true), InputEvent.button(button, false))

    private fun pointer(p: Point): InputEvent {
        val z = zoom.current
        return InputEvent.pointer(z.absX(p.x, width), z.absY(p.y, height))
    }

    private fun reset() {
        moved = false
        mode = Mode.NONE
        zoomed = false
        maxPointers = 0
        scroll.end()
        swipe.reset()
        pinch.reset()
    }

    private fun centroid(points: List<Point>): Point =
        if (points.isEmpty()) last else Point(points.sumOf { it.x.toDouble() }.toFloat() / points.size, points.sumOf { it.y.toDouble() }.toFloat() / points.size)

    private fun dist(a: Point, b: Point) = hypot(a.x - b.x, a.y - b.y)

    companion object {
        /**
         * A finger held this long without moving right-clicks when it lifts,
         * or drags with the left button when it then moves.
         */
        const val LONG_PRESS_MS = 500L
    }
}
