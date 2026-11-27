package com.desklink.client.input

import com.desklink.client.model.CursorShape
import com.desklink.client.model.InputEvent
import com.desklink.client.model.InputKind

/**
 * The desk's pointer as the tablet draws it, for a session that leaves it
 * out of the video ([com.desklink.client.model.SessionInfo.pointer]).
 *
 * The tablet's own input moves it exactly as the host moves the real one
 * (host/internal/daemon/cursor.go, clientCursor): relative motion in pixels
 * of the client's screen, clamped to the frame, and absolute pointer and pen
 * positions, 0..ABS_MAX across the frame. So it moves with the input, without
 * waiting for the desk. The desk's own reports of the pointer ([hostAt])
 * take over only after [IDLE_MS] without such input, when something else
 * moved it: someone at the desk, or an app. The host's copy adopts them
 * after the same time, so the next move starts from the same place on both
 * sides.
 *
 * Input arrives on the main thread and the desk's reports on the network
 * threads; [listener] runs on whichever changed it.
 */
class LocalPointer(private val now: () -> Long) {
    /** The pointer's hotspot, 0..ABS_MAX across the video frame. */
    @Volatile var x = InputKind.ABS_MAX / 2f
        private set

    @Volatile var y = InputKind.ABS_MAX / 2f
        private set

    /** The pointer's image, null until the desk sends one: then an arrow is drawn. */
    @Volatile var shape: CursorShape? = null
        private set

    /** Called after the position or shape changed. */
    @Volatile var listener: (() -> Unit)? = null

    private var screenW = 1
    private var screenH = 1
    private var lastInput = Long.MIN_VALUE / 2

    /**
     * Starts a session's pointer at the centre, where the host's copy starts,
     * on a client screen of [width] x [height] pixels (the hello's size, which
     * relative motion is measured in), with no shape yet.
     */
    @Synchronized
    fun start(width: Int, height: Int) {
        screenW = width.coerceAtLeast(1)
        screenH = height.coerceAtLeast(1)
        x = InputKind.ABS_MAX / 2f
        y = InputKind.ABS_MAX / 2f
        shape = null
        lastInput = Long.MIN_VALUE / 2
        listener?.invoke()
    }

    /** The client's screen changed size (own screen resized or rotated). */
    @Synchronized
    fun screen(width: Int, height: Int) {
        screenW = width.coerceAtLeast(1)
        screenH = height.coerceAtLeast(1)
    }

    /** Moves the pointer as [events], about to go to the host, will move it there. */
    fun sent(events: List<InputEvent>) {
        var moved = false
        synchronized(this) {
            for (e in events) {
                when (e.kind) {
                    InputKind.REL_MOVE -> {
                        x = (x + e.x.toFloat() * InputKind.ABS_MAX / screenW).coerceIn(0f, InputKind.ABS_MAX.toFloat())
                        y = (y + e.y.toFloat() * InputKind.ABS_MAX / screenH).coerceIn(0f, InputKind.ABS_MAX.toFloat())
                    }
                    InputKind.ABS_POINTER, InputKind.ABS_MOVE -> {
                        x = e.x.coerceIn(0, InputKind.ABS_MAX).toFloat()
                        y = e.y.coerceIn(0, InputKind.ABS_MAX).toFloat()
                    }
                    else -> continue
                }
                moved = true
            }
            if (moved) lastInput = now()
        }
        if (moved) listener?.invoke()
    }

    /** The desk reports the pointer at [ax], [ay]; it counts once the tablet has left the pointer alone. */
    fun hostAt(ax: Int, ay: Int) {
        synchronized(this) {
            if (now() - lastInput < IDLE_MS) return
            val nx = ax.coerceIn(0, InputKind.ABS_MAX).toFloat()
            val ny = ay.coerceIn(0, InputKind.ABS_MAX).toFloat()
            if (nx == x && ny == y) return
            x = nx
            y = ny
        }
        listener?.invoke()
    }

    /** The desk's pointer took [s] as its image. */
    fun shape(s: CursorShape) {
        shape = s
        listener?.invoke()
    }

    companion object {
        /** Matches the host's pointerIdle (host/internal/daemon/pointerapart.go). */
        const val IDLE_MS = 300L
    }
}
