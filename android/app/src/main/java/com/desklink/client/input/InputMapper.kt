package com.desklink.client.input

import android.annotation.SuppressLint
import android.content.Context
import android.os.Build
import android.os.Handler
import android.os.Looper
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
import com.desklink.client.model.MouseInfo
import com.desklink.client.model.Wire
import kotlin.math.abs
import kotlin.math.hypot

/**
 * Turns touch gestures into mouse input, like a laptop trackpad:
 * one finger moves the cursor, a tap clicks, a double tap held down drags
 * with the left button, two fingers scroll or right-click on tap, and a
 * three-finger tap toggles the HUD. On the touchscreen, three fingers swiped
 * up, left or right also send GNOME's overview and workspace shortcuts
 * ([Swipe]), and while the zoom is enabled two fingers pinch to zoom the
 * video and, once it is zoomed, pan it instead of scrolling
 * ([TwoFingerGesture]). It has no Android dependencies so the gesture rules
 * are unit-tested.
 */
class TouchGestureEngine(
    private val emit: (List<InputEvent>) -> Unit,
    private val onThreeFingerTap: () -> Unit,
    /** Host pixels per input unit for one-finger movement. */
    var sensitivity: Float = 1.5f,
    private val slop: Float = 12f,
    /** The video's local zoom, which pinches change; a touchpad keeps the default, never enabled. */
    zoom: VideoZoom = VideoZoom(),
    /** Whether three fingers swipe; on for the touchscreen, off for a touchpad, whose units differ. */
    private val swipes: Boolean = false,
) {
    /** Touch actions, mirroring MotionEvent's masked actions. */
    enum class Action { DOWN, POINTER_DOWN, MOVE, POINTER_UP, UP, CANCEL }

    /** A pointer position in view pixels. */
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
    private var width = 1
    private var height = 1
    private val swipe = SwipeTracker()
    private val pinch = TwoFingerGesture(zoom, slop)

    /** Stops the current gesture from clicking, used when a physical button was pressed. */
    fun suppressTap() {
        tapSuppressed = true
    }

    /**
     * Feeds one touch event with the positions of the pointers still down,
     * on a view of [width] x [height] pixels, which only zooming needs.
     */
    fun onTouch(action: Action, points: List<Point>, timeMs: Long, width: Int = 1, height: Int = 1) {
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
                if (timeMs - lastTapUp <= DOUBLE_TAP_MS && dist(c, lastTapPos) <= DOUBLE_TAP_SLOP) {
                    dragging = true
                    emit(listOf(InputEvent.button(InputKind.BUTTON_LEFT, true)))
                }
            }
            Action.POINTER_DOWN, Action.POINTER_UP -> {
                maxPointers = maxOf(maxPointers, points.size)
                if (!moved) anchor = c
                if (action == Action.POINTER_DOWN && points.size == 2) pinch.start(points)
                last = c
            }
            Action.MOVE -> onMove(points, c)
            Action.UP -> onUp(timeMs, c)
            Action.CANCEL -> {
                if (dragging) emit(listOf(InputEvent.button(InputKind.BUTTON_LEFT, false)))
                reset()
            }
        }
    }

    private fun onMove(points: List<Point>, c: Point) {
        val count = points.size
        if (!moved && dist(c, anchor) > slop) {
            moved = true
        }
        if (swipes && maxPointers >= 3) {
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
                    moved = true
                    return
                }
                TwoFingerGesture.Mode.SCROLL -> moved = true
            }
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
        swipe.reset()
        pinch.reset()
    }

    private fun centroid(points: List<Point>): Point =
        if (points.isEmpty()) last else Point(points.sumOf { it.x.toDouble() }.toFloat() / points.size, points.sumOf { it.y.toDouble() }.toFloat() / points.size)

    private fun dist(a: Point, b: Point) = hypot(a.x - b.x, a.y - b.y)

    companion object {
        // Gesture timing and distance limits.
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

    /** The latched modifier codes, for the HUD's highlighting. */
    val active: Set<Int> get() = latched.toSet()

    /** Toggles a modifier and returns whether it is now latched. */
    fun toggle(code: Int): Boolean = if (!latched.remove(code)) latched.add(code) else false

    /** The events for a key press, with any latched modifiers pressed first. */
    fun keyDown(code: Int): List<InputEvent> {
        if (latched.isEmpty() || code in MODIFIERS) return listOf(InputEvent.key(code, true))
        val mods = latched.toList()
        latched.clear()
        applied[code] = mods
        return mods.map { InputEvent.key(it, true) } + InputEvent.key(code, true)
    }

    /** The events for a key release, releasing the modifiers its press applied. */
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
        /** Keys that are modifiers themselves and never consume the latch. */
        val MODIFIERS = with(LinuxKeyCodes) {
            setOf(KEY_LEFTCTRL, KEY_LEFTALT, KEY_LEFTMETA, KEY_LEFTSHIFT, KEY_RIGHTSHIFT, KEY_RIGHTCTRL, KEY_RIGHTALT, KEY_RIGHTMETA)
        }

        /**
         * Packs event groups into packets of at most [max] events without
         * splitting a group, so a key's press and release travel together.
         */
        fun pack(groups: List<List<InputEvent>>, max: Int): List<List<InputEvent>> {
            val out = ArrayList<List<InputEvent>>()
            var current = ArrayList<InputEvent>()
            for (g in groups) {
                if (current.isNotEmpty() && current.size + g.size > max) {
                    out += current
                    current = ArrayList()
                }
                current += g
            }
            if (current.isNotEmpty()) out += current
            return out
        }
    }
}

/** A key's identity for [KeySplit]: its scan code and key code, which its press and release share. */
internal fun keyIdentity(e: KeyEvent): Int = (e.scanCode shl 16) or (e.keyCode and 0xFFFF)

/** What [e] does, for [KeySplit]. */
internal fun splitAction(e: KeyEvent): KeySplit.Action = when (e.action) {
    KeyEvent.ACTION_DOWN -> if (e.repeatCount == 0) KeySplit.Action.PRESS else KeySplit.Action.REPEAT
    KeyEvent.ACTION_UP -> KeySplit.Action.RELEASE
    else -> KeySplit.Action.OTHER
}

/** How one-finger touches on the video act. */
enum class TouchMode {
    /** A tap clicks where the finger is ([DirectTouchEngine]). */
    DIRECT,

    /** The screen is a trackpad that moves the cursor ([TouchGestureEngine]). */
    TRACKPAD,
}

/**
 * Routes every Android input source to the host: hardware keyboard keys,
 * a captured mouse or touchpad, touchscreen trackpad gestures and the S-Pen
 * as an absolute digitizer.
 */
class InputMapper(
    private val send: (List<InputEvent>) -> Unit,
    private val onHudToggle: () -> Unit,
    /** Whether keys go to the desktop now ([keysGoToDesktop]); otherwise they stay with Android. */
    private val keysOpen: () -> Boolean = { true },
    /** Sends text for the host to type, in order with the input sent before and after it. */
    private val sendText: (String) -> Unit = {},
    /** Whether the host types text ([com.desklink.client.model.SessionInfo.keysyms]). */
    private val typesText: () -> Boolean = { false },
    /** How the desk moves its pointer for a mouse ([com.desklink.client.model.SessionInfo.mouse]). */
    private val mouseInfo: () -> MouseInfo? = { null },
) {
    /** The HUD bar's sticky modifiers, applied to the next key from any source. */
    val latch = ModifierLatch()

    /**
     * The video's local zoom: pinches on the touchscreen change it, and
     * touches, the S-Pen and an uncaptured mouse point through it at the part
     * of the desktop shown under them.
     */
    val zoom = VideoZoom()

    /** Which keys' presses the activity's window took, so their releases follow. */
    private val split = KeySplit()
    private val touch = TouchGestureEngine(send, onHudToggle, zoom = zoom, swipes = true)
    private val direct = DirectTouchEngine(send, onHudToggle, zoom = zoom)

    /** How touches on the video act; a gesture in progress finishes in its old mode. */
    @Volatile
    var touchMode = TouchMode.DIRECT
    private val touchpad = TouchGestureEngine(send, onHudToggle, sensitivity = 1f)
    private var penButton = false

    /** Holds S-Pen taps still, so a double tap selects a word. */
    private val penTap = PenTapSteadier()

    /** The last pen tilt sent, so unchanged tilt is not repeated. */
    private var lastTilt: Pair<Int, Int>? = null
    private val main = Handler(Looper.getMainLooper())
    // Android reports HOVER_EXIT right before a hovering stylus touches down;
    // leaving proximity only counts if no touch follows.
    private val penLeft = Runnable {
        // The next approach sends its tilt again, also to a new session.
        lastTilt = null
        val events = mutableListOf(InputEvent.pen(InputKind.PEN_OUT, lastPenX, lastPenY))
        if (penButton) {
            penButton = false
            events += InputEvent.button(InputKind.BUTTON_RIGHT, false)
        }
        send(events)
    }
    private var gestureMode = TouchMode.DIRECT
    private var lastPenX = 0
    private var lastPenY = 0
    private var relRemX = 0f
    private var relRemY = 0f
    private val acceleration = MouseAcceleration()
    private val wheel = WheelAccumulator()
    private val mouse = AbsoluteMouse(send)
    private val mouseBack = MouseBack(onHudToggle)

    /** Touchscreen trackpad speed in host pixels per screen pixel. */
    var touchSensitivity: Float
        get() = touch.sensitivity
        set(v) {
            touch.sensitivity = v
        }

    /**
     * Sends a hardware or soft-keyboard key to the desktop whatever the
     * session's state; returns false for keys Android keeps. Callers decide
     * first whether keys go to the desktop at all.
     */
    fun sendKey(event: KeyEvent): Boolean {
        if (event.keyCode in SYSTEM_KEYS) return false
        if (event.keyCode == KeyEvent.KEYCODE_BACK && !isHardwareKeyboard(event)) return false
        val escFirst = event.device?.let { LinuxKeyCodes.escFirstKeyboard(it.name, it.vendorId) } == true
        val code = LinuxKeyCodes.fromKeyEvent(event.scanCode, event.keyCode, escFirst) ?: return false
        when (event.action) {
            KeyEvent.ACTION_DOWN -> if (event.repeatCount == 0) send(latch.keyDown(code))
            KeyEvent.ACTION_UP -> send(latch.keyUp(code))
        }
        return true
    }

    /**
     * A key the activity's window received: taken for the desktop and true,
     * or false to leave it to Android. A press is taken only while [keysOpen]
     * (streaming, HUD closed); its repeats and release follow it ([KeySplit]).
     */
    fun onKeyEvent(event: KeyEvent): Boolean {
        if (isMouseButtonKey(event)) return onMouseButtonKey(event)
        return split.route(keyIdentity(event), splitAction(event), keysOpen) { sendKey(event) }
    }

    /**
     * Android also makes Back and Forward keys from a mouse's Back and
     * Forward buttons. The buttons are handled as buttons: Forward reaches the
     * desktop and Back toggles the HUD ([MouseBack]), so these keys must not
     * act a second time; a Back key with no button press seen toggles.
     */
    private fun onMouseButtonKey(event: KeyEvent): Boolean {
        if (event.keyCode == KeyEvent.KEYCODE_BACK && event.action == KeyEvent.ACTION_UP) {
            mouseBack.onKeyUp(event.downTime, event.isCanceled)
        }
        return true
    }

    private fun isMouseButtonKey(e: KeyEvent): Boolean =
        (e.keyCode == KeyEvent.KEYCODE_BACK || e.keyCode == KeyEvent.KEYCODE_FORWARD) &&
            (e.isFromSource(InputDevice.SOURCE_MOUSE) || e.isFromSource(InputDevice.SOURCE_MOUSE_RELATIVE))

    /**
     * Handles a physical keyboard's key before the soft keyboard app sees it.
     * Samsung Keyboard stays active while a Book Cover keyboard is attached and
     * turns most physical keys into composing text, so they must not reach it.
     */
    fun onKeyEventPreIme(event: KeyEvent): Boolean = isHardwareKeyboard(event) && onKeyEvent(event)

    /**
     * Applies one soft-keyboard edit ([TextRoute]): key taps for the
     * backspaces and plain ASCII, or one `type` message per
     * [com.desklink.client.model.TypeText.MAX_CHARS] characters, backspaces
     * included, for other text when the host types text.
     */
    fun edit(edit: TextEdit) {
        if (TextRoute.asText(edit, typesText(), latched = latch.active.isNotEmpty())) {
            TextRoute.chunks(TextRoute.text(edit)).forEach(sendText)
            return
        }
        val taps = List(edit.delete) { latch.tap(LinuxKeyCodes.KEY_BACKSPACE) } +
            edit.text.mapNotNull { c -> LinuxKeyCodes.forChar(c)?.let { (code, shift) -> latch.tap(code, shift) } }
        ModifierLatch.pack(taps, Wire.MAX_EVENTS_PER_PACKET).forEach(send)
    }

    /** Presses and releases one key, with any latched modifiers. */
    fun tapKey(code: Int) = send(latch.tap(code))

    /** Handles a touchscreen or S-Pen touch on the video, sized [width] x [height]. */
    fun onTouchEvent(e: MotionEvent, width: Int, height: Int): Boolean {
        if (isPen(e)) return onPen(e, width, height, hovering = false)
        if (isMouse(e)) return onMouse(e, width, height)
        val a = action(e) ?: return true
        if (a == TouchGestureEngine.Action.DOWN) gestureMode = touchMode
        when (gestureMode) {
            TouchMode.DIRECT -> direct.onTouch(a, points(e), e.eventTime, width, height)
            TouchMode.TRACKPAD -> touch.onTouch(a, points(e), e.eventTime, width, height)
        }
        return true
    }

    /** Handles S-Pen and uncaptured mouse hover over the video. */
    fun onHoverEvent(e: MotionEvent, width: Int, height: Int): Boolean = when {
        isPen(e) -> onPen(e, width, height, hovering = true)
        isMouse(e) -> onMouse(e, width, height)
        else -> false
    }

    /** Handles an uncaptured mouse's wheel and button presses over the video. */
    fun onGenericMotionEvent(e: MotionEvent, width: Int, height: Int): Boolean =
        if (isMouse(e)) onMouse(e, width, height) else false

    /**
     * An uncaptured mouse, as when pointer capture was lost or while the HUD
     * is open: it points at absolute positions on the video ([AbsoluteMouse])
     * and never goes through the finger gestures, so every button works and a
     * click is a click, not a tap.
     */
    private fun onMouse(e: MotionEvent, width: Int, height: Int): Boolean {
        if (width <= 0 || height <= 0) return true
        when (e.actionMasked) {
            MotionEvent.ACTION_SCROLL -> {
                scroll(e)
                return true
            }
            MotionEvent.ACTION_BUTTON_PRESS -> if (e.actionButton == MotionEvent.BUTTON_BACK) {
                mouseBack.onButton(e.eventTime)
                return true
            }
        }
        val moved = e.actionMasked != MotionEvent.ACTION_HOVER_EXIT
        mouse.update(normX(e.x, width), normY(e.y, height), e.buttonState, moved)
        return true
    }

    private fun scroll(e: MotionEvent) {
        wheel.add(e.getAxisValue(MotionEvent.AXIS_HSCROLL), e.getAxisValue(MotionEvent.AXIS_VSCROLL))?.let { send(listOf(it)) }
    }

    private fun isMouse(e: MotionEvent): Boolean =
        e.getToolType(0) == MotionEvent.TOOL_TYPE_MOUSE || e.isFromSource(InputDevice.SOURCE_MOUSE)

    /** Handles a captured mouse or touchpad event. */
    fun onCapturedPointer(e: MotionEvent): Boolean {
        when (e.actionMasked) {
            MotionEvent.ACTION_BUTTON_PRESS, MotionEvent.ACTION_BUTTON_RELEASE -> {
                val press = e.actionMasked == MotionEvent.ACTION_BUTTON_PRESS
                if (e.actionButton == MotionEvent.BUTTON_BACK) {
                    if (press) mouseBack.onButton(e.eventTime)
                    return true
                }
                val button = MOUSE_BUTTONS[e.actionButton] ?: return true
                touchpad.suppressTap()
                send(listOf(InputEvent.button(button, press)))
                return true
            }
            MotionEvent.ACTION_SCROLL -> {
                scroll(e)
                return true
            }
        }
        if (e.isFromSource(InputDevice.SOURCE_TOUCHPAD)) {
            val range = e.device?.getMotionRange(MotionEvent.AXIS_X)?.range
            touchpad.sensitivity = if (range != null && range > 0) TOUCHPAD_SPAN_PX / range else 1f
            touchpad.onTouch(action(e) ?: return true, points(e), e.eventTime)
            return true
        }
        if (e.actionMasked in RELATIVE_MOTION) {
            // Each sample is accelerated at its own time, as libinput does
            // for each of the mouse's reports.
            val mouse = mouseInfo()
            for (i in 0 until e.historySize) {
                val (x, y) = acceleration.accelerate(
                    e.getHistoricalAxisValue(MotionEvent.AXIS_RELATIVE_X, i),
                    e.getHistoricalAxisValue(MotionEvent.AXIS_RELATIVE_Y, i),
                    historicalTimeNs(e, i), mouse,
                )
                relRemX += x
                relRemY += y
            }
            val (x, y) = acceleration.accelerate(
                e.getAxisValue(MotionEvent.AXIS_RELATIVE_X), e.getAxisValue(MotionEvent.AXIS_RELATIVE_Y), timeNs(e), mouse,
            )
            relRemX += x
            relRemY += y
            val ix = relRemX.toInt()
            val iy = relRemY.toInt()
            relRemX -= ix
            relRemY -= iy
            if (ix != 0 || iy != 0) send(listOf(InputEvent.move(ix, iy)))
        }
        return true
    }

    /** When [e] happened, in nanoseconds; Android before 14 gives milliseconds only. */
    private fun timeNs(e: MotionEvent): Long =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) e.eventTimeNanos else e.eventTime * 1_000_000

    /** When [e]'s historical sample [pos] happened, in nanoseconds. */
    private fun historicalTimeNs(e: MotionEvent, pos: Int): Long =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            e.getHistoricalEventTimeNanos(pos)
        } else {
            e.getHistoricalEventTime(pos) * 1_000_000
        }

    private fun onPen(e: MotionEvent, width: Int, height: Int, hovering: Boolean): Boolean {
        if (width <= 0 || height <= 0) return true
        main.removeCallbacks(penLeft)
        lastPenX = normX(e.x, width)
        lastPenY = normY(e.y, height)
        if (e.actionMasked == MotionEvent.ACTION_HOVER_EXIT) {
            main.postDelayed(penLeft, PEN_EXIT_DELAY_MS)
            return true
        }
        val events = ArrayList<InputEvent>()
        val flags = when {
            e.actionMasked == MotionEvent.ACTION_HOVER_EXIT || e.actionMasked == MotionEvent.ACTION_CANCEL -> InputKind.PEN_OUT
            hovering || e.actionMasked == MotionEvent.ACTION_UP -> InputKind.PEN_HOVER
            else -> InputKind.PEN_TOUCH
        }
        if (flags != InputKind.PEN_OUT) {
            for (i in 0 until e.historySize) {
                val p = steadied(e.actionMasked, hovering, false, e.getHistoricalX(i), e.getHistoricalY(i), e.getHistoricalEventTime(i))
                addPenSample(
                    events, flags, normX(p.x, width), normY(p.y, height),
                    e.getHistoricalPressure(i),
                    e.getHistoricalAxisValue(MotionEvent.AXIS_TILT, i), e.getHistoricalAxisValue(MotionEvent.AXIS_ORIENTATION, i),
                )
            }
        }
        val p = steadied(e.actionMasked, hovering, true, e.x, e.y, e.eventTime)
        addPenSample(
            events, flags, normX(p.x, width), normY(p.y, height),
            e.pressure, e.getAxisValue(MotionEvent.AXIS_TILT), e.getAxisValue(MotionEvent.AXIS_ORIENTATION),
        )
        val barrel = (e.buttonState and MotionEvent.BUTTON_STYLUS_PRIMARY) != 0 && flags != InputKind.PEN_OUT
        if (barrel != penButton) {
            penButton = barrel
            events += InputEvent.button(InputKind.BUTTON_RIGHT, barrel)
        }
        send(events.takeLast(MAX_EVENTS_PER_PACKET))
        return true
    }

    /**
     * One pen sample's position with taps held still ([PenTapSteadier]); a
     * sample is the event's [last] one or one of its history. Hover passes
     * through.
     */
    private fun steadied(action: Int, hovering: Boolean, last: Boolean, x: Float, y: Float, timeMs: Long): PenTapSteadier.Point = when {
        hovering -> PenTapSteadier.Point(x, y)
        action == MotionEvent.ACTION_CANCEL -> PenTapSteadier.Point(x, y).also { penTap.cancel() }
        action == MotionEvent.ACTION_DOWN && last -> penTap.down(x, y, timeMs)
        action == MotionEvent.ACTION_UP && last -> penTap.up(x, y, timeMs)
        else -> penTap.move(x, y, timeMs)
    }

    /**
     * Adds one pen sample: a tilt record first when the tilt moved by half a
     * degree or more since the last one sent, then the position with the
     * pressure while touching.
     */
    private fun addPenSample(
        out: MutableList<InputEvent>, flags: Int, x: Int, y: Int,
        pressure: Float, tilt: Float, orientation: Float,
    ) {
        if (flags != InputKind.PEN_OUT) {
            val (tx, ty) = PenAxes.tilt(tilt, orientation)
            val last = lastTilt
            if (last == null || abs(tx - last.first) >= TILT_STEP || abs(ty - last.second) >= TILT_STEP) {
                lastTilt = tx to ty
                out += InputEvent.penTilt(tx, ty)
            }
        }
        out += InputEvent.pen(flags, x, y, if (flags == InputKind.PEN_TOUCH) PenAxes.pressure(pressure) else 0)
    }

    // Positions on the video under a view pixel, through the zoom.
    private fun normX(v: Float, width: Int): Int = zoom.current.absX(v, width)

    private fun normY(v: Float, height: Int): Int = zoom.current.absY(v, height)

    private fun isPen(e: MotionEvent): Boolean {
        val tool = e.getToolType(0)
        return tool == MotionEvent.TOOL_TYPE_STYLUS || tool == MotionEvent.TOOL_TYPE_ERASER
    }

    /** Whether the key comes from a physical keyboard rather than the soft keyboard. */
    fun isHardwareKeyboard(e: KeyEvent): Boolean {
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
        /** Captured mouse actions that may carry relative motion, a press or release included. */
        private val RELATIVE_MOTION = setOf(
            MotionEvent.ACTION_MOVE, MotionEvent.ACTION_HOVER_MOVE, MotionEvent.ACTION_DOWN, MotionEvent.ACTION_UP,
        )
        private const val PEN_EXIT_DELAY_MS = 80L

        /** A full swipe across a touchpad moves the cursor this many host pixels. */
        private const val TOUCHPAD_SPAN_PX = 1600f
        /** 1248-byte payload / 12-byte events, with room to spare. */
        private const val MAX_EVENTS_PER_PACKET = 100

        /** Half a degree: smaller tilt changes are not sent. */
        private const val TILT_STEP = 50
    }
}

/**
 * A transparent, focusable view over the video. It receives touch, S-Pen
 * hover and captured mouse events, and acts as the text field the soft
 * keyboard types into.
 */
@SuppressLint("ViewConstructor")
class InputView(context: Context, private val mapper: InputMapper) : View(context) {
    /** Whether this view holds pointer capture while its window has focus. */
    var captureEnabled = false
        set(value) {
            field = value
            if (value && hasWindowFocus()) requestPointerCapture() else if (!value) releasePointerCapture()
        }

    init {
        isFocusable = true
        isFocusableInTouchMode = true
        setOnCapturedPointerListener { _, event -> mapper.onCapturedPointer(event) }
        // Android batches motion events and hands them over once per frame,
        // so a touch, the S-Pen or a mouse would wait up to a refresh (8 ms
        // at 120 Hz) before going to the host; unbuffered, each event comes
        // as the device reports it.
        requestUnbufferedDispatch(UNBUFFERED_SOURCES)
    }

    @SuppressLint("ClickableViewAccessibility")
    override fun onTouchEvent(event: MotionEvent): Boolean {
        if (!hasFocus()) requestFocus()
        // Moonlight found the per-source request above not enough on its own
        // for touch streams, so each touch-down asks for its gesture too.
        if (event.actionMasked == MotionEvent.ACTION_DOWN) requestUnbufferedDispatch(event)
        return mapper.onTouchEvent(event, width, height)
    }

    override fun onHoverEvent(event: MotionEvent): Boolean = mapper.onHoverEvent(event, width, height)

    override fun onGenericMotionEvent(event: MotionEvent): Boolean =
        mapper.onGenericMotionEvent(event, width, height) || super.onGenericMotionEvent(event)

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
            private val composing = ComposingText()

            override fun setComposingText(text: CharSequence, newCursorPosition: Int): Boolean {
                mapper.edit(composing.replace(text.toString()))
                return true
            }

            override fun finishComposingText(): Boolean {
                composing.finish()
                return true
            }

            override fun commitText(text: CharSequence, newCursorPosition: Int): Boolean {
                // Committed text replaces the composing text, if any.
                mapper.edit(composing.commit(text.toString()))
                return true
            }

            override fun deleteSurroundingText(beforeLength: Int, afterLength: Int): Boolean {
                composing.finish()
                mapper.edit(TextEdit(abs(beforeLength), ""))
                return true
            }

            override fun sendKeyEvent(event: KeyEvent): Boolean = mapper.onKeyEvent(event)
        }
    }

    override fun dispatchKeyEventPreIme(event: KeyEvent): Boolean =
        mapper.onKeyEventPreIme(event) || super.dispatchKeyEventPreIme(event)

    /** Shows or hides the soft keyboard typing into this view. */
    fun showKeyboard(show: Boolean) {
        if (show) {
            requestFocus()
            windowInsetsController?.show(WindowInsets.Type.ime())
        } else {
            windowInsetsController?.hide(WindowInsets.Type.ime())
        }
    }

    private companion object {
        /**
         * Keyboards (buttons), the touchscreen, S-Pen and an uncaptured mouse
         * (pointer), a captured touchpad (position) and a captured mouse
         * (trackball, which covers relative mouse motion).
         */
        const val UNBUFFERED_SOURCES = InputDevice.SOURCE_CLASS_BUTTON or InputDevice.SOURCE_CLASS_POINTER or
            InputDevice.SOURCE_CLASS_POSITION or InputDevice.SOURCE_CLASS_TRACKBALL
    }
}
