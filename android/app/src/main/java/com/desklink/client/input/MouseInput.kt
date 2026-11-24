package com.desklink.client.input

import android.view.MotionEvent
import com.desklink.client.model.InputEvent
import com.desklink.client.model.InputKind

/** Android mouse buttons the desktop gets, as host buttons. Back is not one: it toggles the HUD ([MouseBack]). */
internal val MOUSE_BUTTONS = mapOf(
    MotionEvent.BUTTON_PRIMARY to InputKind.BUTTON_LEFT,
    MotionEvent.BUTTON_SECONDARY to InputKind.BUTTON_RIGHT,
    MotionEvent.BUTTON_TERTIARY to InputKind.BUTTON_MIDDLE,
    MotionEvent.BUTTON_FORWARD to InputKind.BUTTON_EXTRA,
)

/**
 * Wheel steps as host scroll units ([InputKind.WHEEL_UNIT] per notch). A
 * high-resolution or smooth-scrolling wheel reports fractions of a notch;
 * the part that does not make a whole unit is kept for the next step, so
 * slow scrolling adds up instead of being dropped.
 */
class WheelAccumulator {
    private var restH = 0f
    private var restV = 0f

    /** The scroll for a step of [h] and [v] notches (Android's AXIS_HSCROLL and AXIS_VSCROLL), or null if under a unit. */
    fun add(h: Float, v: Float): InputEvent? {
        restH += h * InputKind.WHEEL_UNIT
        restV += v * InputKind.WHEEL_UNIT
        val ih = restH.toInt()
        val iv = restV.toInt()
        restH -= ih
        restV -= iv
        return if (ih != 0 || iv != 0) InputEvent.scroll(ih, iv) else null
    }
}

/**
 * A mouse that Android does not capture, as when pointer capture was lost
 * or while the HUD is open: it points at absolute positions on the video,
 * like the S-Pen, and its buttons come as Android's button state on every
 * event. Only changes reach the host.
 */
class AbsoluteMouse(private val send: (List<InputEvent>) -> Unit) {
    private var x = -1
    private var y = -1
    private var buttons = 0

    /**
     * The mouse is at [x], [y] (0..[InputKind.ABS_MAX]) with Android's
     * [buttonState]; [moved] is false for an event without a position worth
     * following, such as the pointer leaving the view.
     */
    fun update(x: Int, y: Int, buttonState: Int, moved: Boolean = true) {
        val out = mutableListOf<InputEvent>()
        if (moved && (x != this.x || y != this.y)) {
            this.x = x
            this.y = y
            out += InputEvent.pointer(x, y)
        }
        for ((bit, button) in MOUSE_BUTTONS) {
            val down = buttonState and bit != 0
            if (down != (buttons and bit != 0)) out += InputEvent.button(button, down)
        }
        buttons = buttonState
        if (out.isNotEmpty()) send(out)
    }
}

/**
 * The mouse's Back (thumb) button opens and closes the HUD, once a press.
 * Android reports the press as a button, as a Back key it makes from the
 * button, or both, depending on pointer capture and on where the pointer is;
 * the key toggles only when no button press came with it.
 */
class MouseBack(private val toggle: () -> Unit) {
    private var buttonAt = Long.MIN_VALUE / 2

    /** The Back button went down at [eventTime] (uptime milliseconds). */
    fun onButton(eventTime: Long) {
        buttonAt = eventTime
        toggle()
    }

    /** A Back key made from the mouse was released; it went down at [downTime]. */
    fun onKeyUp(downTime: Long, canceled: Boolean) {
        if (!canceled && buttonAt < downTime - SAME_PRESS_MS) toggle()
    }

    companion object {
        /** A button and a key this close are the same press. */
        const val SAME_PRESS_MS = 300L
    }
}
