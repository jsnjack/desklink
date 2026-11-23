package com.desklink.client.input

import com.desklink.client.input.TouchGestureEngine.Action
import com.desklink.client.input.TouchGestureEngine.Point
import com.desklink.client.model.InputEvent
import com.desklink.client.model.InputKind
import kotlin.math.hypot

/**
 * Touchscreen input that clicks where the finger is, like a touchscreen
 * laptop: a tap clicks there, a press held still right-clicks there, one
 * finger dragged drags with the left button (selects text, moves windows),
 * two fingers scroll the window under them or right-click on a tap, and a
 * three-finger tap toggles the HUD. Positions go out as absolute pointer
 * records, 0..ABS_MAX across the view. It has no Android dependencies so the
 * gesture rules are unit-tested.
 */
class DirectTouchEngine(
    private val emit: (List<InputEvent>) -> Unit,
    private val onThreeFingerTap: () -> Unit,
    private val slop: Float = 16f,
) {
    private var width = 1
    private var height = 1
    private var downTime = 0L
    private var maxPointers = 0
    private var moved = false
    private var dragging = false
    private var scrolling = false
    private var anchor = Point(0f, 0f)
    private var last = Point(0f, 0f)
    private var lastTapUp = Long.MIN_VALUE / 2
    private var lastTapAt = Point(0f, 0f)
    private var remWheel = 0f
    private var remHWheel = 0f

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
            Action.POINTER_DOWN -> {
                maxPointers = maxOf(maxPointers, points.size)
                if (!moved) anchor = c
                last = c
            }
            // The anchor stays between all the fingers, so a two-finger tap
            // right-clicks there, not under the finger lifted last.
            Action.POINTER_UP -> last = c
            Action.MOVE -> onMove(points.size, c)
            Action.UP -> onUp(timeMs)
            Action.CANCEL -> {
                if (dragging) emit(listOf(pointer(last), InputEvent.button(InputKind.BUTTON_LEFT, false)))
                reset()
            }
        }
    }

    private fun onMove(count: Int, c: Point) {
        if (!moved && dist(c, anchor) > slop) moved = true
        if (!moved) return
        when {
            dragging -> emit(listOf(pointer(c)))
            count == 1 && maxPointers == 1 -> {
                // The drag starts where the finger went down, not where it
                // crossed the slop, so text selection starts at the right letter.
                dragging = true
                emit(listOf(pointer(anchor), InputEvent.button(InputKind.BUTTON_LEFT, true), pointer(c)))
            }
            count >= 2 -> {
                // Scroll the window under the fingers: the wheel goes to the pointer's window.
                if (!scrolling) {
                    scrolling = true
                    emit(listOf(pointer(c)))
                }
                // Natural scrolling: content follows the fingers.
                remWheel += (c.y - last.y) * TouchGestureEngine.SCROLL_UNITS_PER_PX
                remHWheel -= (c.x - last.x) * TouchGestureEngine.SCROLL_UNITS_PER_PX
                val w = remWheel.toInt()
                val h = remHWheel.toInt()
                remWheel -= w
                remHWheel -= h
                if (w != 0 || h != 0) emit(listOf(InputEvent.scroll(h, w)))
            }
        }
        last = c
    }

    private fun onUp(timeMs: Long) {
        val held = timeMs - downTime
        when {
            dragging -> emit(listOf(pointer(last), InputEvent.button(InputKind.BUTTON_LEFT, false)))
            moved -> Unit // a scroll ended
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

    private fun click(button: Int, at: Point) =
        listOf(pointer(at), InputEvent.button(button, true), InputEvent.button(button, false))

    private fun pointer(p: Point) = InputEvent.pointer(norm(p.x, width), norm(p.y, height))

    private fun norm(v: Float, size: Int): Int = (v / size * InputKind.ABS_MAX).toInt().coerceIn(0, InputKind.ABS_MAX)

    private fun reset() {
        moved = false
        dragging = false
        scrolling = false
        maxPointers = 0
        remWheel = 0f
        remHWheel = 0f
    }

    private fun centroid(points: List<Point>): Point =
        if (points.isEmpty()) last else Point(points.sumOf { it.x.toDouble() }.toFloat() / points.size, points.sumOf { it.y.toDouble() }.toFloat() / points.size)

    private fun dist(a: Point, b: Point) = hypot(a.x - b.x, a.y - b.y)

    companion object {
        /** A one-finger press held this long without moving right-clicks. */
        const val LONG_PRESS_MS = 500L
    }
}
