package com.desklink.client.net

import com.desklink.client.model.ClipboardImages
import com.desklink.client.model.KeepAwake
import com.desklink.client.model.LevelInfo
import com.desklink.client.model.ScreenMode
import com.desklink.client.model.StreamPreset
import com.desklink.client.model.StreamSize
import org.junit.Assert.assertEquals
import org.junit.Test

class HelloBuilderTest {
    @Test
    fun aReconnectAsksForTheSettingsChangedDuringTheSession() {
        val hellos = HelloBuilder("av1", StreamPreset.AUTO, audio = true, screen = ScreenMode.OWN, scale = 2.0, keepAwake = KeepAwake.OFF)
        val first = hellos.build(start = null)
        assertEquals(2.0, first.scale, 0.0)
        assertEquals(KeepAwake.OFF, first.keepAwake)
        // The HUD changes both during the first connection; the tablet then roams to the VPN.
        hellos.scale = 1.5
        hellos.keepAwake = KeepAwake.FOUR_HOURS
        val again = hellos.build(start = LevelInfo(1920, 1200, 60))
        assertEquals(1.5, again.scale, 0.0)
        assertEquals(KeepAwake.FOUR_HOURS, again.keepAwake)
        assertEquals(LevelInfo(1920, 1200, 60), again.start)
        assertEquals(ScreenMode.OWN, again.screen)
    }

    @Test
    fun aReconnectAsksForTheLockAfterSettingAsLastChanged() {
        val hellos = HelloBuilder("av1", StreamPreset.AUTO, audio = false, screen = ScreenMode.OWN, scale = 2.0, keepAwake = KeepAwake.OFF)
        assertEquals(false, hellos.build(null).lockAfter)
        hellos.lockAfter = true
        assertEquals(true, hellos.build(null).lockAfter)
    }

    @Test
    fun everyHelloSaysTheTabletTakesClipboardImages() {
        val hellos = HelloBuilder("hevc", StreamPreset.CRISP, audio = false, screen = ScreenMode.AUTO, scale = 0.0, keepAwake = KeepAwake.OFF)
        assertEquals(ClipboardImages.DEFAULT_LIMIT, hellos.build(start = null).clipboardImages)
    }

    @Test
    fun theHelloAsksForTheViewsSizeWhateverThePreset() {
        val hellos = HelloBuilder("av1", StreamPreset.BALANCED, audio = false, screen = ScreenMode.AUTO, scale = 2.0, keepAwake = KeepAwake.OFF,
            size = StreamSize(2560, 1600))
        val hello = hellos.build(start = null)
        assertEquals(2560, hello.width)
        assertEquals(1600, hello.height)
        // The preset keeps its quality: quantizer and frame rate.
        assertEquals(StreamPreset.BALANCED.qp, hello.qp)
        assertEquals(StreamPreset.BALANCED.fps, hello.fps)
    }

    @Test
    fun beforeTheViewHasASizeThePresetsStandsIn() {
        val hello = HelloBuilder("av1", StreamPreset.AUTO, audio = false, screen = ScreenMode.AUTO, scale = 2.0, keepAwake = KeepAwake.OFF)
            .build(start = null)
        assertEquals(StreamPreset.AUTO.width, hello.width)
        assertEquals(StreamPreset.AUTO.height, hello.height)
    }

    @Test
    fun aReconnectAsksForTheRotatedSizeAndTheChosenMonitor() {
        val hellos = HelloBuilder("av1", StreamPreset.AUTO, audio = false, screen = ScreenMode.MIRROR, scale = 2.0, keepAwake = KeepAwake.OFF,
            size = StreamSize(2560, 1600), monitor = "eDP-1")
        assertEquals("eDP-1", hellos.build(start = null).monitor)
        hellos.size = StreamSize(1600, 2560)
        hellos.monitor = "DP-5"
        val again = hellos.build(start = null)
        assertEquals(1600, again.width)
        assertEquals(2560, again.height)
        assertEquals("DP-5", again.monitor)
    }
}
