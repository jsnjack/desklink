package com.desklink.client.model

import android.app.Application
import android.content.Context
import androidx.test.core.app.ApplicationProvider
import com.desklink.client.input.TouchMode
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/** Each computer's settings in [HostStore], on Robolectric's SharedPreferences. */
@RunWith(RobolectricTestRunner::class)
class HostSettingsTest {
    private val app: Application = ApplicationProvider.getApplicationContext()
    private val prefs = app.getSharedPreferences("desklink", Context.MODE_PRIVATE)

    @Test
    fun aHostSavedBeforeSettingsWerePerComputerKeepsTheSharedOnes() {
        // As 0.60 saved them, for every host at once.
        prefs.edit()
            .putString("preset", "BALANCED").putString("codec", "hevc").putString("screen", ScreenMode.OWN)
            .putBoolean("play_audio", false).putBoolean("sharper_colours", false).putBoolean("exact_text", true)
            .putString("touch_mode", "TRACKPAD").putFloat("sensitivity", 2.5f).putFloat("own_scale", 1.25f)
            .putInt("keep_awake", KeepAwake.FOUR_HOURS).putBoolean("lock_after", true).putBoolean("mic", true)
            .commit()
        val expected = HostSettings(
            preset = StreamPreset.BALANCED, codec = "hevc", screen = ScreenMode.OWN, sound = false, sharperColours = false,
            exactText = true, touch = TouchMode.TRACKPAD, trackpadSpeed = 2.5f, scale = 1.25, keepAwake = KeepAwake.FOUR_HOURS,
            lockAfter = true, mic = true,
        )
        val store = HostStore(app)
        assertEquals(expected, store.settings("office"))
        assertEquals(expected, store.settings("home"))
    }

    @Test
    fun eachHostKeepsItsOwnSettings() {
        val store = HostStore(app)
        store.saveSettings("office", HostSettings(preset = StreamPreset.SHARP, primary = false))
        assertEquals(StreamPreset.SHARP, store.settings("office").preset)
        assertEquals(HostSettings(), store.settings("home"))
        assertEquals(false, HostStore(app).settings("office").primary)
    }

    @Test
    fun aNewHostStartsFromTheHostUsedLast() {
        val store = HostStore(app)
        store.saveSettings("office", HostSettings(screen = ScreenMode.MIRROR, deskOff = true))
        store.startSettings("laptop", from = "office")
        assertEquals(store.settings("office"), store.settings("laptop"))
        // A copy: changing the host used last later leaves the new one as it started.
        store.saveSettings("office", HostSettings())
        assertEquals(ScreenMode.MIRROR, store.settings("laptop").screen)
    }

    @Test
    fun aFirstHostStartsFromTheSharedSettings() {
        prefs.edit().putBoolean("mic", true).commit()
        val store = HostStore(app)
        store.startSettings("first", from = null)
        assertTrue(store.settings("first").mic)
    }

    @Test
    fun aForgottenHostLosesItsSettings() {
        val store = HostStore(app)
        store.saveSettings("office", HostSettings(preset = StreamPreset.OFFICE_SAVER))
        store.forgetSettings("office")
        assertEquals(HostSettings(), store.settings("office"))
    }

    @Test
    fun aValueThisVersionDoesNotKnowFallsBackToItsDefaultOnly() {
        prefs.edit().putString("settings:office", """{"preset":"ULTRA","screen":"mirror","touch":"PEN","sound":false,"later":1}""").commit()
        val s = HostStore(app).settings("office")
        assertEquals(StreamPreset.AUTO, s.preset)
        assertEquals(TouchMode.DIRECT, s.touch)
        assertEquals(ScreenMode.MIRROR, s.screen)
        assertFalse(s.sound)
    }
}
