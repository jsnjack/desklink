package com.desklink.client.input

import com.desklink.client.model.InputEvent
import com.desklink.client.model.InputKind

/**
 * Scrolls the desktop by how far fingers moved its content (natural
 * scrolling: the content follows the fingers).
 *
 * A host that scrolls like a touchpad ([com.desklink.client.model.SessionInfo.scrollPx])
 * gets the movement in pixels of the tablet's screen and moves the content
 * by the same distance, smoothly, and an end mark when the fingers lift, so
 * apps can let a fast scroll run on. Wheel steps instead are coarse, and
 * browsers animate each one, so the page trailed the fingers; older hosts
 * still get them, [TouchGestureEngine.SCROLL_UNITS_PER_PX] 1/120 notches a
 * pixel.
 */
class FingerScroll(private val pixels: () -> Boolean) {
    /** Fractions of the record units not sent yet. */
    private var remX = 0f
    private var remY = 0f
    private var sentPixels = false

    /** The fingers moved the content by [dx], [dy] screen pixels (positive right and down). */
    fun move(dx: Float, dy: Float): List<InputEvent> {
        if (dx == 0f && dy == 0f) return emptyList()
        val px = pixels()
        if (px) {
            remX += dx * InputKind.SCROLL_PX_UNIT
            remY += dy * InputKind.SCROLL_PX_UNIT
        } else {
            // Wheel units count up and right: content following the fingers
            // down is a scroll up, to the right a scroll left.
            remX -= dx * TouchGestureEngine.SCROLL_UNITS_PER_PX
            remY += dy * TouchGestureEngine.SCROLL_UNITS_PER_PX
        }
        val x = remX.toInt()
        val y = remY.toInt()
        remX -= x
        remY -= y
        if (x == 0 && y == 0) return emptyList()
        if (!px) return listOf(InputEvent.scroll(x, y))
        sentPixels = true
        return listOf(InputEvent.scrollPx(x, y))
    }

    /** The scroll is over (the fingers lifted): ends a pixel scroll, so a fast one can run on. */
    fun end(): List<InputEvent> {
        val out = if (sentPixels) listOf(InputEvent.scrollPx(0, 0, finish = true)) else emptyList()
        sentPixels = false
        remX = 0f
        remY = 0f
        return out
    }
}
