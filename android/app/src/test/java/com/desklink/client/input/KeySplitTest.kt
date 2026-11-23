package com.desklink.client.input

import com.desklink.client.input.KeySplit.Action
import com.desklink.client.model.SessionInfo
import com.desklink.client.net.StreamSession
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class KeySplitTest {
    private val split = KeySplit()
    private val sent = mutableListOf<Pair<Int, Action>>()
    private var open = true

    /** Routes one event; the desktop takes every key, as the mapper does for keyboard keys. */
    private fun key(code: Int, action: Action, takes: Boolean = true): Boolean =
        split.route(code, action, { open }) { sent += code to action; takes }

    @Test
    fun aKeyPressedForTheDesktopIsReleasedThereAfterTheHudOpened() {
        assertTrue(key(SHIFT, Action.PRESS))
        open = false
        assertTrue("the repeat stays away from Android", key(SHIFT, Action.REPEAT))
        assertTrue("Android never saw the press, so it must not get the release", key(SHIFT, Action.RELEASE))
        assertEquals(listOf(SHIFT to Action.PRESS, SHIFT to Action.RELEASE), sent)
    }

    @Test
    fun aKeyPressedWhileTheHudWasOpenIsReleasedInAndroidAfterItClosed() {
        open = false
        assertFalse(key(SHIFT, Action.PRESS))
        open = true
        assertFalse("the repeat goes to Android with its press", key(SHIFT, Action.REPEAT))
        assertFalse("Android got the press, so it gets the release", key(SHIFT, Action.RELEASE))
        assertEquals(emptyList<Pair<Int, Action>>(), sent)
    }

    @Test
    fun aKeyTheDesktopDoesNotTakeStaysWithAndroid() {
        assertFalse(key(VOLUME, Action.PRESS, takes = false))
        assertFalse(key(VOLUME, Action.RELEASE, takes = false))
        assertEquals(listOf(VOLUME to Action.PRESS), sent)
    }

    @Test
    fun aReleaseWithoutASeenPressGoesToAndroid() {
        assertFalse(key(A, Action.RELEASE))
        assertFalse(key(A, Action.REPEAT))
        assertEquals(emptyList<Pair<Int, Action>>(), sent)
    }

    @Test
    fun eachPressDecidesAfresh() {
        assertTrue(key(A, Action.PRESS))
        // The release was lost; the next press comes while the HUD is open.
        open = false
        assertFalse(key(A, Action.PRESS))
        assertFalse(key(A, Action.RELEASE))
        assertEquals(listOf(A to Action.PRESS), sent)
    }

    @Test
    fun keysAreTrackedOneByOne() {
        assertTrue(key(CTRL, Action.PRESS))
        open = false
        assertFalse(key(A, Action.PRESS))
        open = true
        assertFalse(key(A, Action.RELEASE))
        assertTrue(key(CTRL, Action.RELEASE))
        assertEquals(listOf(CTRL to Action.PRESS, CTRL to Action.RELEASE), sent)
    }

    @Test
    fun keysGoToTheDesktopOnlyWhileStreamingWithTheHudClosed() {
        val streaming = StreamSession.State.Streaming(SessionInfo(sessionId = 1, udpPort = 9444, codec = "av1", width = 1, height = 1, fps = 60, qp = 20))
        assertTrue(keysGoToDesktop(streaming, hudOpen = false))
        assertFalse(keysGoToDesktop(streaming, hudOpen = true))
        assertFalse(keysGoToDesktop(StreamSession.State.Connecting, hudOpen = false))
        assertFalse(keysGoToDesktop(StreamSession.State.Reconnecting(1, 3), hudOpen = false))
        assertFalse(keysGoToDesktop(null, hudOpen = false))
    }

    private companion object {
        const val SHIFT = 42
        const val CTRL = 29
        const val A = 30
        const val VOLUME = 115
    }
}
