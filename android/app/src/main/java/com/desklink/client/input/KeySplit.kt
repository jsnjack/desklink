package com.desklink.client.input

import com.desklink.client.net.StreamSession

/**
 * Whether keys go to the desktop: only while the stream is on screen and the
 * HUD is closed. While the session connects or reconnects, or the HUD is
 * open, every key stays with Android.
 */
fun keysGoToDesktop(state: StreamSession.State?, hudOpen: Boolean): Boolean =
    state is StreamSession.State.Streaming && !hudOpen

/**
 * Keeps each key's press and release on one side. A key is taken for the
 * desktop or left to Android when it goes down, and its repeats and release
 * follow the press even if the HUD opened or closed, or the stream started or
 * stopped, in between. Without this, Shift pressed while the HUD is open and
 * released after it closed would leave Android with a press and no release,
 * and a key pressed for the desktop would reach Android as a lone release.
 * Keys are identified by an Int the caller derives from the event.
 */
class KeySplit {
    /** What a key event does. */
    enum class Action { PRESS, REPEAT, RELEASE, OTHER }

    private val taken = HashSet<Int>()

    /**
     * Whether to consume a key event. A press goes to the desktop when
     * [open] says keys go there and [send] takes it. A repeat is consumed
     * when its press was. A release is consumed only when its press was, and
     * then handed to [send] so the desktop lets the key go; otherwise it goes
     * to Android, which got the press. Other events go to [send] while [open].
     */
    fun route(key: Int, action: Action, open: () -> Boolean, send: () -> Boolean): Boolean = when (action) {
        Action.PRESS -> {
            // A new press decides afresh, even if an earlier release was missed.
            taken.remove(key)
            (open() && send()).also { if (it) taken += key }
        }
        Action.REPEAT -> key in taken
        Action.RELEASE -> taken.remove(key).also { if (it) send() }
        Action.OTHER -> open() && send()
    }
}
