package com.desklink.client.input

import kotlin.math.hypot

/**
 * Holds S-Pen taps still, so the desktop sees a click rather than a tiny
 * drag, and a double tap on a word selects it. A pen wobbles a few pixels as
 * it lands and lifts, while GTK takes two clicks for a double click only
 * within about 5 logical pixels of each other, some 1 mm on the Tab S9. For
 * [holdMs] after the pen touches down, a position within [holdPx] of where
 * it landed is reported as the touch-down position; once the pen moves
 * further or the time is up, positions pass through until it lifts. When
 * the last touch stayed a tap (it lifted while still held), a touch within
 * [repeatMs] of that lift and [repeatPx] of where it lifted lands where the
 * lift was reported; after a stroke the next touch lands where it touches,
 * so handwriting does not join one stroke to the next. Hover passes through
 * untouched, and pressure and tilt are not this class's business. Positions
 * are in view pixels.
 */
class PenTapSteadier(
    private val holdMs: Long = HOLD_MS,
    private val holdPx: Float = HOLD_PX,
    private val repeatMs: Long = REPEAT_MS,
    private val repeatPx: Float = REPEAT_PX,
) {
    /** A pen position in view pixels. */
    data class Point(val x: Float, val y: Float)

    // Where the pen physically touched down, null while it is up; the
    // position reported while the touch is held still.
    private var landed: Point? = null
    private var shown = Point(0f, 0f)
    private var downAt = 0L
    private var holding = false

    // The position reported when the pen last lifted from a tap, and when;
    // null after a stroke.
    private var lifted: Point? = null
    private var liftedAt = 0L

    /** The pen touched down at [x], [y] at [timeMs]; returns the position to send. */
    fun down(x: Float, y: Float, timeMs: Long): Point {
        val p = Point(x, y)
        val last = lifted
        shown = if (last != null && timeMs - liftedAt <= repeatMs && dist(p, last) <= repeatPx) last else p
        landed = p
        downAt = timeMs
        holding = true
        return shown
    }

    /** The touching pen is at [x], [y] at [timeMs]; returns the position to send. */
    fun move(x: Float, y: Float, timeMs: Long): Point {
        val p = Point(x, y)
        val origin = landed ?: return p
        if (holding && (timeMs - downAt > holdMs || dist(p, origin) > holdPx)) holding = false
        return if (holding) shown else p
    }

    /** The pen lifted at [x], [y] at [timeMs]; returns the position to send. */
    fun up(x: Float, y: Float, timeMs: Long): Point {
        val p = move(x, y, timeMs)
        lifted = p.takeIf { holding }
        liftedAt = timeMs
        landed = null
        holding = false
        return p
    }

    /** The touch was cancelled: the next touch neither holds nor snaps to this one. */
    fun cancel() {
        landed = null
        holding = false
        lifted = null
    }

    private fun dist(a: Point, b: Point) = hypot(a.x - b.x, a.y - b.y)

    /** Hold and snap limits. */
    companion object {
        /** How long a touch-down is held still: longer than most taps last. */
        const val HOLD_MS = 100L

        /** How far the pen may wobble while held, in view pixels (about 2 mm on the Tab S9). */
        const val HOLD_PX = 20f

        /** How soon after a lift a touch counts as the second tap of a double tap. */
        const val REPEAT_MS = 150L

        /** How close to the last lift that second tap must land, in view pixels (about 5 mm). */
        const val REPEAT_PX = 50f
    }
}
