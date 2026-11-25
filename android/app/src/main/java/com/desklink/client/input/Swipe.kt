package com.desklink.client.input

import com.desklink.client.input.TouchGestureEngine.Point
import com.desklink.client.model.InputEvent
import kotlin.math.abs

/**
 * GNOME's three-finger touchpad gestures, sent as the keyboard shortcuts
 * that do the same, since the host injects only a keyboard and a mouse.
 */
enum class Swipe(private vararg val keys: Int) {
    /** Fingers up: the activities overview (Super). */
    UP(LinuxKeyCodes.KEY_LEFTMETA),

    /** Fingers left: the next workspace (Super+Page Down), as the workspaces follow the fingers. */
    LEFT(LinuxKeyCodes.KEY_LEFTMETA, LinuxKeyCodes.KEY_PAGEDOWN),

    /** Fingers right: the previous workspace (Super+Page Up). */
    RIGHT(LinuxKeyCodes.KEY_LEFTMETA, LinuxKeyCodes.KEY_PAGEUP),
    ;

    /** The shortcut's key presses, then their releases in reverse order. */
    fun taps(): List<InputEvent> = keys.map { InputEvent.key(it, true) } + keys.reversed().map { InputEvent.key(it, false) }
}

/**
 * Follows three or more fingers and reports one [Swipe] per gesture, once
 * their centre has travelled [SWIPE_PX] along one axis. Callers pass the
 * centre's moves only while the same fingers stay down, so the jump of the
 * centre when a finger lands or lifts is not travel.
 */
internal class SwipeTracker {
    private var dx = 0f
    private var dy = 0f

    /** Whether the fingers travelled far enough to swipe in this gesture. */
    var fired = false
        private set

    /** Starts a new gesture. */
    fun reset() {
        dx = 0f
        dy = 0f
        fired = false
    }

    /**
     * Adds a move of the fingers' centre from [from] to [to] and returns the
     * swipe the first time the travel reaches the threshold. A swipe down
     * returns null: only up, left and right have shortcuts.
     */
    fun move(from: Point, to: Point): Swipe? {
        if (fired) return null
        dx += to.x - from.x
        dy += to.y - from.y
        if (maxOf(abs(dx), abs(dy)) < SWIPE_PX) return null
        fired = true
        return when {
            abs(dx) > abs(dy) -> if (dx < 0) Swipe.LEFT else Swipe.RIGHT
            dy < 0 -> Swipe.UP
            else -> null
        }
    }

    companion object {
        /**
         * How far three fingers travel before they swipe, in view pixels:
         * about 11 mm on the Tab S9's 274 ppi screen, far beyond a tap's
         * jitter (the engines' slop is 12 to 16 px) and short for a flick.
         */
        const val SWIPE_PX = 120f
    }
}
