package com.desklink.client.ui

import com.desklink.client.input.TouchMode
import com.desklink.client.model.HostSettings
import com.desklink.client.model.KeepAwake
import com.desklink.client.model.MonitorInfo
import com.desklink.client.model.ScreenMode
import com.desklink.client.model.SessionInfo
import com.desklink.client.model.StreamPreset
import com.desklink.client.net.LinkQuality
import com.desklink.client.net.LinkStats
import com.desklink.client.net.StreamSession
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class HudStateTest {
    private val monitors = listOf(MonitorInfo("DP-1", 2560, 1440, primary = true), MonitorInfo("HDMI-A-1", 1920, 1080))
    private val own = SessionInfo(
        sessionId = 1, udpPort = 9444, codec = "av1", width = 2560, height = 1600, fps = 120, qp = 12,
        screen = ScreenMode.OWN, scale = 1.5, keepAwake = KeepAwake.FOUR_HOURS, lockAfter = true,
        primary = false, deskOff = true, mic = true, monitors = monitors,
    )
    private val mirror = own.copy(screen = ScreenMode.MIRROR, scale = 0.0, primary = null, deskOff = null, mirroring = "DP-1")
    private val stats = LinkStats(rttMs = 9, fps = 120)

    private fun hud(
        state: StreamSession.State,
        micWanted: Boolean = true,
        settings: HostSettings = HostSettings(touch = TouchMode.TRACKPAD, mic = micWanted),
        av1Available: Boolean = true,
    ) = hudUiState("cm", state, stats, keyboardShown = false, statsShown = true, settings = settings, av1Available = av1Available)

    @Test
    fun showsTheLinkGradeRoundTripAndFrameRateWhileStreaming() {
        assertEquals(HudLink.Live(LinkQuality.EXCELLENT, 9, 120), hud(StreamSession.State.Streaming(own)).link)
    }

    @Test
    fun showsOnlyTheStatusAndActionsBeforeTheSessionStreams() {
        val connecting = hud(StreamSession.State.Connecting)
        assertEquals(HudLink.Connecting, connecting.link)
        assertNull(connecting.screen)
        assertNull(connecting.session)
        val reconnecting = hud(StreamSession.State.Reconnecting(3, 10))
        assertEquals(HudLink.Reconnecting(3, 10), reconnecting.link)
        assertNull(reconnecting.screen)
        assertNull(reconnecting.session)
        assertTrue(reconnecting.trackpad)
        assertTrue(reconnecting.statsShown)
    }

    @Test
    fun offersTheScaleAndDeskSwitchesOnTheOwnScreenButNoMonitors() {
        val screen = hud(StreamSession.State.Streaming(own)).screen!!
        assertEquals(1.5, screen.scale)
        assertFalse(screen.sharp)
        assertEquals(false, screen.primary)
        assertEquals(true, screen.deskOff)
        assertTrue(screen.monitors.isEmpty())
    }

    @Test
    fun offersMonitorsOnlyWhileMirroringMoreThanOne() {
        val screen = hud(StreamSession.State.Streaming(mirror)).screen!!
        assertEquals(monitors, screen.monitors)
        assertEquals("DP-1", screen.mirroring)
        assertNull(screen.scale)
        assertNull(screen.primary)
        assertNull(screen.deskOff)
        assertTrue(hud(StreamSession.State.Streaming(mirror.copy(monitors = monitors.take(1)))).screen!!.monitors.isEmpty())
    }

    @Test
    fun leavesTheScreenSectionOutWhenTheHostDoesNotReportIt() {
        assertNull(hud(StreamSession.State.Streaming(own.copy(screen = ""))).screen)
    }

    @Test
    fun marksOnlyWholeScalesSharpAndStopsAtTheEnds() {
        val full = HudScreen(ScreenMode.OWN, scale = 2.0)
        assertTrue(full.sharp)
        assertTrue(full.canShrink)
        assertFalse(full.canGrow)
        val least = HudScreen(ScreenMode.OWN, scale = 1.0)
        assertTrue(least.sharp)
        assertFalse(least.canShrink)
        assertTrue(least.canGrow)
        assertFalse(HudScreen(ScreenMode.OWN, scale = 1.25).sharp)
    }

    @Test
    fun turnsTheMicrophoneOnOnlyWhileWantedAndPlayedByTheHost() {
        assertEquals(true, hud(StreamSession.State.Streaming(own)).session!!.mic)
        assertEquals(false, hud(StreamSession.State.Streaming(own), micWanted = false).session!!.mic)
        assertEquals(false, hud(StreamSession.State.Streaming(own.copy(mic = false))).session!!.mic)
        assertNull(hud(StreamSession.State.Streaming(own.copy(mic = null))).session!!.mic)
    }

    @Test
    fun showsTheKeepAwakeAndLockAfterTheHostReports() {
        val session = hud(StreamSession.State.Streaming(own)).session!!
        assertEquals(KeepAwake.FOUR_HOURS, session.keepAwake)
        assertTrue(session.lockAfter)
    }

    @Test
    fun showsTheHostsPictureSettingsWithTheCodecTheStreamRunsIn() {
        val settings = HostSettings(preset = StreamPreset.BALANCED, codec = "av1", sharperColours = false, exactText = true)
        // The host settled on HEVC, as for a tablet whose AV1 choice it could not keep.
        val picture = hud(StreamSession.State.Streaming(own.copy(codec = "hevc")), settings = settings, av1Available = false).picture!!
        assertEquals(HudPicture(StreamPreset.BALANCED, "hevc", av1Available = false, sharperColours = false, exactText = true), picture)
        assertNull(hud(StreamSession.State.Connecting).picture)
    }

    @Test
    fun showsTheSoundSwitchAndTheTrackpadAsTheHostsSettingsHaveThem() {
        val muted = hud(StreamSession.State.Streaming(own), settings = HostSettings(sound = false, trackpadSpeed = 2.5f, scrollSpeed = 0.8f))
        assertEquals(false, muted.session!!.sound)
        assertFalse(muted.trackpad)
        assertEquals(2.5f, muted.trackpadSpeed, 0f)
        assertEquals(0.8f, muted.scrollSpeed, 0f)
        assertTrue(hud(StreamSession.State.Streaming(own)).trackpad)
    }
}
