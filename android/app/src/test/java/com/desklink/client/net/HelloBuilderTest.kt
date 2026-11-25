package com.desklink.client.net

import com.desklink.client.model.KeepAwake
import com.desklink.client.model.LevelInfo
import com.desklink.client.model.ScreenMode
import com.desklink.client.model.StreamPreset
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
}
