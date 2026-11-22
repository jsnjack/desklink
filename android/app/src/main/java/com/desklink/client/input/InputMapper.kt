package com.desklink.client.input

import android.annotation.SuppressLint
import android.content.Context
import android.text.InputType
import android.view.InputDevice
import android.view.KeyEvent
import android.view.MotionEvent
import android.view.View
import android.view.WindowInsets
import android.view.inputmethod.BaseInputConnection
import android.view.inputmethod.EditorInfo
import android.view.inputmethod.InputConnection
import com.desklink.client.model.InputEvent
import com.desklink.client.model.InputKind
import kotlin.math.abs
import kotlin.math.hypot

/**
 * Turns touch gestures into mouse input, like a laptop trackpad:
 * one finger moves the cursor, a tap clicks, a double tap held down drags
 * with the left button, two fingers scroll or right-click on tap, and a
 * three-finger tap toggles the HUD. It has no Android dependencies so the
 * gesture rules are unit-tested.
 */
class TouchGestureEngine(
    private val emit: (List<InputEvent>) -> Unit,
    private val onThreeFingerTap: () -> Unit,
    /** Host pixels per input unit for one-finger movement. */
    var sensitivity: Float = 1.5f,
    private val slop: Float = 12f,
) {
    enum class Action { DOWN, POINTER_DOWN, MOVE, POINTER_UP, UP, CANCEL }

    data class Point(val x: Float, val y: Float)

    private var downTime = 0L
    private var maxPointers = 0
    private var moved = false
    private var tapSuppressed = false
    private var dragging = false
    private var anchor = Point(0f, 0f)
    private var last = Point(0f, 0f)
    private var lastTapUp = Long.MIN_VALUE / 2
    private var lastTapPos = Point(0f, 0f)
    private var remX = 0f
    private var remY = 0f
    private var remWheel = 0f
    private var remHWheel = 0f

    /** Stops the current gesture from clicking, used when a physical button was pressed. */
    fun suppressTap() {
        tapSuppressed = true
    }

    fun onTouch(action: Action, points: List<Point>, timeMs: Long) {
        val c = centroid(points)
        when (action) {
            Action.DOWN -> {
                reset()
                downTime = timeMs
                maxPointers = 1
                anchor = c
                last = c
                if (timeMs - lastTapUp <= DOUBLE_TAP_MS && dist(c, lastTapPos) <= DOUBLE_TAP_SLOP) {
                    dragging = true
                    emit(listOf(InputEvent.button(InputKind.BUTTON_LEFT, true)))
                }
            }
            Action.POINTER_DOWN, Action.POINTER_UP -> {
                maxPointers = maxOf(maxPointers, points.size)
                if (!moved) anchor = c
                last = c
            }
            Action.MOVE -> onMove(points.size, c)
            Action.UP -> onUp(timeMs, c)
            Action.CANCEL -> {
                if (dragging) emit(listOf(InputEvent.button(InputKind.BUTTON_LEFT, false)))
                reset()
            }
        }
    }

    private fun onMove(count: Int, c: Point) {
        if (!moved && dist(c, anchor) > slop) {
            moved = true
        }
        if (!moved) return
        val dx = c.x - last.x
        val dy = c.y - last.y
        last = c
        when {
            count == 1 && maxPointers == 1 -> {
                remX += dx * sensitivity
                remY += dy * sensitivity
                val ix = remX.toInt()
                val iy = remY.toInt()
                remX -= ix
                remY -= iy
                if (ix != 0 || iy != 0) emit(listOf(InputEvent.move(ix, iy)))
            }
            count == 2 -> {
                // Natural scrolling: content follows the fingers.
                remWheel += dy * SCROLL_UNITS_PER_PX
                remHWheel -= dx * SCROLL_UNITS_PER_PX
                val w = remWheel.toInt()
                val h = remHWheel.toInt()
                remWheel -= w
                remHWheel -= h
                if (w != 0 || h != 0) emit(listOf(InputEvent.scroll(h, w)))
            }
        }
    }

    private fun onUp(timeMs: Long, c: Point) {
        val quick = timeMs - downTime < TAP_MS
        val tap = !moved && !tapSuppressed
        when {
            dragging -> {
                emit(listOf(InputEvent.button(InputKind.BUTTON_LEFT, false)))
                lastTapUp = Long.MIN_VALUE / 2
            }
            tap && quick && maxPointers == 1 -> {
                emit(listOf(InputEvent.button(InputKind.BUTTON_LEFT, true), InputEvent.button(InputKind.BUTTON_LEFT, false)))
                lastTapUp = timeMs
                lastTapPos = c
            }
            tap && quick && maxPointers == 2 -> {
                emit(listOf(InputEvent.button(InputKind.BUTTON_RIGHT, true), InputEvent.button(InputKind.BUTTON_RIGHT, false)))
            }
            tap && timeMs - downTime < THREE_FINGER_TAP_MS && maxPointers >= 3 -> onThreeFingerTap()
        }
        reset()
    }

    private fun reset() {
        moved = false
        tapSuppressed = false
        dragging = false
        maxPointers = 0
        remX = 0f
        remY = 0f
        remWheel = 0f
        remHWheel = 0f
    }

    private fun centroid(points: List<Point>): Point =
        if (points.isEmpty()) last else Point(points.sumOf { it.x.toDouble() }.toFloat() / points.size, points.sumOf { it.y.toDouble() }.toFloat() / points.size)

    private fun dist(a: Point, b: Point) = hypot(a.x - b.x, a.y - b.y)

    companion object {
        const val TAP_MS = 250L
        const val THREE_FINGER_TAP_MS = 400L
        const val DOUBLE_TAP_MS = 300L
        const val DOUBLE_TAP_SLOP = 60f
        /** Two-finger scroll speed in 1/120 notches per pixel. */
        const val SCROLL_UNITS_PER_PX = 3f
    }
}

/**
 * Sticky modifiers from the HUD bar: a latched Ctrl, Alt or Super is held
 * around the next key, then released.
 */
class ModifierLatch {
    private val latched = linkedSetOf<Int>()
    private val applied = HashMap<Int, List<Int>>()

    val active: Set<Int> get() = latched.toSet()

    /** Toggles a modifier and returns whether it is now latched. */
    fun toggle(code: Int): Boolean = if (!latched.remove(code)) latched.add(code) else false

    fun keyDown(code: Int): List<InputEvent> {
        if (latched.isEmpty() || code in MODIFIERS) return listOf(InputEvent.key(code, true))
        val mods = latched.toList()
        latched.clear()
        applied[code] = mods
        return mods.map { InputEvent.key(it, true) } + InputEvent.key(code, true)
    }

    fun keyUp(code: Int): List<InputEvent> {
        val mods = applied.remove(code).orEmpty()
        return listOf(InputEvent.key(code, false)) + mods.reversed().map { InputEvent.key(it, false) }
    }

    /** A full press and release, with Shift when [shift] is set. */
    fun tap(code: Int, shift: Boolean = false): List<InputEvent> {
        val down = keyDown(code)
        val up = keyUp(code)
        return if (shift) {
            listOf(InputEvent.key(LinuxKeyCodes.KEY_LEFTSHIFT, true)) + down + up + InputEvent.key(LinuxKeyCodes.KEY_LEFTSHIFT, false)
        } else {
            down + up
        }
    }

    companion object {
        val MODIFIERS = setOf(LinuxKeyCodes.KEY_LEFTCTRL, LinuxKeyCodes.KEY_LEFTALT, LinuxKeyCodes.KEY_LEFTMETA, LinuxKeyCodes.KEY_LEFTSHIFT, 54, 97, 100, 126)
    }
}

/**
 * Routes every Android input source to the host: hardware keyboard keys,
 * a captured mouse or touchpad, touchscreen trackpad gestures and the S-Pen
 * as an absolute digitizer.
 */
class InputMapper(
    private val send: (List<InputEvent>) -> Unit,
    onHudToggle: () -> Unit,
) {
    val latch = ModifierLatch()
    private val touch = TouchGestureEngine(send, onHudToggle)
    private val touchpad = TouchGestureEngine(send, onHudToggle, sensitivity = 1f)
    private var penButton = false
    private var relRemX = 0f
    private var relRemY = 0f

    var touchSensitivity: Float
        get() = touch.sensitivity
        set(v) {
            touch.sensitivity = v
        }

    fun onKeyEvent(event: KeyEvent): Boolean {
        if (event.keyCode in SYSTEM_KEYS) return false
        if (event.keyCode == KeyEvent.KEYCODE_BACK && !isHardwareKeyboard(event)) return false
        val code = LinuxKeyCodes.fromKeyEvent(event.scanCode, event.keyCode) ?: return false
        when (event.action) {
            KeyEvent.ACTION_DOWN -> if (event.repeatCount == 0) send(latch.keyDown(code))
            KeyEvent.ACTION_UP -> send(latch.keyUp(code))
        }
        return true
    }

    /** Types text from the soft keyboard, one key tap per character. */
    fun typeText(text: CharSequence) {
        val events = ArrayList<InputEvent>()
        for (c in text) {
            val (code, shift) = LinuxKeyCodes.forChar(c) ?: continue
            events += latch.tap(code, shift)
        }
        if (events.isNotEmpty()) send(events)
    }

    fun tapKey(code: Int) = send(latch.tap(code))

    fun onTouchEvent(e: MotionEvent, width: Int, height: Int): Boolean {
        if (isPen(e)) return onPen(e, width, height, hovering = false)
        touch.onTouch(action(e) ?: return true, points(e), e.eventTime)
        return true
    }

    fun onHoverEvent(e: MotionEvent, width: Int, height: Int): Boolean =
        if (isPen(e)) onPen(e, width, height, hovering = true) else false

    fun onCapturedPointer(e: MotionEvent): Boolean {
        when (e.actionMasked) {
            MotionEvent.ACTION_BUTTON_PRESS, MotionEvent.ACTION_BUTTON_RELEASE -> {
                val button = BUTTONS[e.actionButton] ?: return true
                touchpad.suppressTap()
                send(listOf(InputEvent.button(button, e.actionMasked == MotionEvent.ACTION_BUTTON_PRESS)))
                return true
            }
            MotionEvent.ACTION_SCROLL -> {
                val v = (e.getAxisValue(MotionEvent.AXIS_VSCROLL) * InputKind.WHEEL_UNIT).toInt()
                val h = (e.getAxisValue(MotionEvent.AXIS_HSCROLL) * InputKind.WHEEL_UNIT).toInt()
                if (v != 0 || h != 0) send(listOf(InputEvent.scroll(h, v)))
                return true
            }
        }
        if (e.isFromSource(InputDevice.SOURCE_TOUCHPAD)) {
            val range = e.device?.getMotionRange(MotionEvent.AXIS_X)?.range
            touchpad.sensitivity = if (range != null && range > 0) TOUCHPAD_SPAN_PX / range else 1f
            touchpad.onTouch(action(e) ?: return true, points(e), e.eventTime)
            return true
        }
        if (e.actionMasked == MotionEvent.ACTION_MOVE || e.actionMasked == MotionEvent.ACTION_HOVER_MOVE) {
            var dx = e.getAxisValue(MotionEvent.AXIS_RELATIVE_X)
            var dy = e.getAxisValue(MotionEvent.AXIS_RELATIVE_Y)
            for (i in 0 until e.historySize) {
                dx += e.getHistoricalAxisValue(MotionEvent.AXIS_RELATIVE_X, i)
                dy += e.getHistoricalAxisValue(MotionEvent.AXIS_RELATIVE_Y, i)
            }
            relRemX += dx
            relRemY += dy
            val ix = relRemX.toInt()
            val iy = relRemY.toInt()
            relRemX -= ix
            relRemY -= iy
            if (ix != 0 || iy != 0) send(listOf(InputEvent.move(ix, iy)))
        }
        return true
    }

    private fun onPen(e: MotionEvent, width: Int, height: Int, hovering: Boolean): Boolean {
        if (width <= 0 || height <= 0) return true
        val events = ArrayList<InputEvent>()
        val flags = when {
            e.actionMasked == MotionEvent.ACTION_HOVER_EXIT || e.actionMasked == MotionEvent.ACTION_CANCEL -> InputKind.PEN_OUT
            hovering || e.actionMasked == MotionEvent.ACTION_UP -> InputKind.PEN_HOVER
            else -> InputKind.PEN_TOUCH
        }
        if (flags != InputKind.PEN_OUT) {
            for (i in 0 until e.historySize) {
                events += InputEvent.pen(flags, norm(e.getHistoricalX(i), width), norm(e.getHistoricalY(i), height))
            }
        }
        events += InputEvent.pen(flags, norm(e.x, width), norm(e.y, height))
        val barrel = (e.buttonState and MotionEvent.BUTTON_STYLUS_PRIMARY) != 0 && flags != InputKind.PEN_OUT
        if (barrel != penButton) {
            penButton = barrel
            events += InputEvent.button(InputKind.BUTTON_RIGHT, barrel)
        }
        send(events.takeLast(MAX_EVENTS_PER_PACKET))
        return true
    }

    private fun norm(v: Float, size: Int): Int = (v / size * InputKind.ABS_MAX).toInt().coerceIn(0, InputKind.ABS_MAX)

    private fun isPen(e: MotionEvent): Boolean {
        val tool = e.getToolType(0)
        return tool == MotionEvent.TOOL_TYPE_STYLUS || tool == MotionEvent.TOOL_TYPE_ERASER
    }

    private fun isHardwareKeyboard(e: KeyEvent): Boolean {
        val d = e.device ?: return false
        return !d.isVirtual && d.keyboardType == InputDevice.KEYBOARD_TYPE_ALPHABETIC
    }

    private fun action(e: MotionEvent): TouchGestureEngine.Action? = when (e.actionMasked) {
        MotionEvent.ACTION_DOWN -> TouchGestureEngine.Action.DOWN
        MotionEvent.ACTION_POINTER_DOWN -> TouchGestureEngine.Action.POINTER_DOWN
        MotionEvent.ACTION_MOVE -> TouchGestureEngine.Action.MOVE
        MotionEvent.ACTION_POINTER_UP -> TouchGestureEngine.Action.POINTER_UP
        MotionEvent.ACTION_UP -> TouchGestureEngine.Action.UP
        MotionEvent.ACTION_CANCEL -> TouchGestureEngine.Action.CANCEL
        else -> null
    }

    /** Pointer positions, leaving out the one that is going up. */
    private fun points(e: MotionEvent): List<TouchGestureEngine.Point> {
        val lifting = if (e.actionMasked == MotionEvent.ACTION_POINTER_UP) e.actionIndex else -1
        return (0 until e.pointerCount).filter { it != lifting }.map { TouchGestureEngine.Point(e.getX(it), e.getY(it)) }
    }

    companion object {
        private val SYSTEM_KEYS = setOf(KeyEvent.KEYCODE_VOLUME_UP, KeyEvent.KEYCODE_VOLUME_DOWN, KeyEvent.KEYCODE_POWER)
        private val BUTTONS = mapOf(
            MotionEvent.BUTTON_PRIMARY to InputKind.BUTTON_LEFT,
            MotionEvent.BUTTON_SECONDARY to InputKind.BUTTON_RIGHT,
            MotionEvent.BUTTON_TERTIARY to InputKind.BUTTON_MIDDLE,
            MotionEvent.BUTTON_BACK to InputKind.BUTTON_SIDE,
            MotionEvent.BUTTON_FORWARD to InputKind.BUTTON_EXTRA,
        )
        /** A full swipe across a touchpad moves the cursor this many host pixels. */
        private const val TOUCHPAD_SPAN_PX = 1600f
        /** 1248-byte payload / 12-byte events, with room to spare. */
        private const val MAX_EVENTS_PER_PACKET = 100
    }
}

/**
 * A transparent, focusable view over the video. It receives touch, S-Pen
 * hover and captured mouse events, and acts as the text field the soft
 * keyboard types into.
 */
@SuppressLint("ViewConstructor")
class InputView(context: Context, private val mapper: InputMapper) : View(context) {
    var captureEnabled = false
        set(value) {
            field = value
            if (value && hasWindowFocus()) requestPointerCapture() else if (!value) releasePointerCapture()
        }

    init {
        isFocusable = true
        isFocusableInTouchMode = true
        setOnCapturedPointerListener { _, event -> mapper.onCapturedPointer(event) }
    }

    @SuppressLint("ClickableViewAccessibility")
    override fun onTouchEvent(event: MotionEvent): Boolean {
        if (!hasFocus()) requestFocus()
        return mapper.onTouchEvent(event, width, height)
    }

    override fun onHoverEvent(event: MotionEvent): Boolean = mapper.onHoverEvent(event, width, height)

    override fun onAttachedToWindow() {
        super.onAttachedToWindow()
        // The window usually has focus already, so no focus change will request capture.
        post { if (captureEnabled && hasWindowFocus()) reclaim() }
    }

    /** Takes focus back, for example after the HUD's search field had it, and recaptures the pointer. */
    fun reclaim() {
        requestFocus()
        if (captureEnabled) requestPointerCapture()
    }

    override fun onWindowFocusChanged(hasWindowFocus: Boolean) {
        super.onWindowFocusChanged(hasWindowFocus)
        if (hasWindowFocus && captureEnabled) reclaim()
    }

    override fun onCheckIsTextEditor(): Boolean = true

    override fun onCreateInputConnection(outAttrs: EditorInfo): InputConnection {
        outAttrs.inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_VISIBLE_PASSWORD or
            InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS
        outAttrs.imeOptions = EditorInfo.IME_FLAG_NO_FULLSCREEN or EditorInfo.IME_FLAG_NO_EXTRACT_UI or EditorInfo.IME_ACTION_NONE
        return object : BaseInputConnection(this, false) {
            override fun commitText(text: CharSequence, newCursorPosition: Int): Boolean {
                mapper.typeText(text)
                return true
            }

            override fun deleteSurroundingText(beforeLength: Int, afterLength: Int): Boolean {
                repeat(abs(beforeLength)) { mapper.tapKey(LinuxKeyCodes.KEY_BACKSPACE) }
                return true
            }

            override fun sendKeyEvent(event: KeyEvent): Boolean = mapper.onKeyEvent(event)
        }
    }

    fun showKeyboard(show: Boolean) {
        if (show) {
            requestFocus()
            windowInsetsController?.show(WindowInsets.Type.ime())
        } else {
            windowInsetsController?.hide(WindowInsets.Type.ime())
        }
    }
}
