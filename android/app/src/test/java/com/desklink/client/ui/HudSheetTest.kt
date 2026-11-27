package com.desklink.client.ui

import android.app.Application
import android.content.ComponentName
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createEmptyComposeRule
import androidx.compose.ui.test.longClick
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performTouchInput
import androidx.test.core.app.ActivityScenario
import androidx.test.core.app.ApplicationProvider
import com.desklink.client.R
import com.desklink.client.model.KeepAwake
import com.desklink.client.model.ScreenMode
import com.desklink.client.model.StreamPreset
import com.desklink.client.net.LinkQuality
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows
import org.robolectric.annotation.Config

/** The HUD sheet's rows on Robolectric: what a tap, a long press and a menu choice do. */
@RunWith(RobolectricTestRunner::class)
@Config(qualifiers = "w1205dp-h753dp-land-night-340dpi")
class HudSheetTest {
    @get:Rule
    val compose = createEmptyComposeRule()

    private val app: Application = ApplicationProvider.getApplicationContext()
    private val actions = RecordedActions()
    private lateinit var scenario: ActivityScenario<ComponentActivity>

    private val state = HudUiState(
        "cm", HudLink.Live(LinkQuality.EXCELLENT, 9, 120),
        screen = HudScreen(ScreenMode.OWN, scale = 2.0, primary = false, deskOff = false),
        session = HudSession(mic = false, keepAwake = KeepAwake.FOUR_HOURS, lockAfter = true),
        picture = HudPicture(StreamPreset.AUTO, "av1", av1Available = true, sharperColours = true, exactText = false),
    )

    @Before
    fun show() {
        // Robolectric knows only the app's own activities; this plain one hosts the sheet.
        Shadows.shadowOf(app.packageManager).addActivityIfNotPresent(ComponentName(app, ComponentActivity::class.java))
        scenario = ActivityScenario.launch(ComponentActivity::class.java)
        scenario.onActivity { it.setContent { MaterialTheme(colorScheme = darkColorScheme()) { HudSheet(state, actions) } } }
    }

    @After
    fun close() = scenario.close()

    @Test
    fun aTapFlipsASwitch() {
        compose.onNodeWithText(app.getString(R.string.label_lock_after)).performScrollTo().performClick()
        assertEquals(listOf("lockAfter false"), actions.calls)
    }

    @Test
    fun theSoundSwitchStopsPlaying() {
        compose.onNodeWithText(app.getString(R.string.label_sound)).performScrollTo().performClick()
        assertEquals(listOf("sound false"), actions.calls)
    }

    @Test
    fun aMenuChoiceChangesTheQuality() {
        compose.onNodeWithText(app.getString(R.string.label_quality)).performScrollTo().performClick()
        compose.onNodeWithText(app.getString(R.string.preset_balanced)).performClick()
        assertEquals(listOf("preset ${StreamPreset.BALANCED}"), actions.calls)
    }

    @Test
    fun aLongPressExplainsASwitchWithoutFlippingIt() {
        compose.onNodeWithText(app.getString(R.string.label_desk_off)).performTouchInput { longClick() }
        compose.onNodeWithText(app.getString(R.string.desk_off_explain)).assertIsDisplayed()
        assertEquals(emptyList<String>(), actions.calls)
    }

    @Test
    fun aMenuChoiceSetsKeepAwake() {
        compose.onNodeWithText(app.getString(R.string.hud_keep_awake)).performScrollTo().performClick()
        compose.onNodeWithText(app.getString(R.string.keep_awake_sleep)).performClick()
        assertEquals(listOf("keepAwake ${KeepAwake.SUSPEND}"), actions.calls)
    }

    @Test
    fun theOtherModeSwitchesTheScreen() {
        compose.onNodeWithText(app.getString(R.string.screen_mirror)).performClick()
        compose.onNodeWithText(app.getString(R.string.screen_own)).performClick()
        assertEquals(listOf("screen ${ScreenMode.MIRROR}"), actions.calls)
    }

    /** Records each call with its argument. */
    private class RecordedActions : HudActions {
        val calls = mutableListOf<String>()
        override fun close() { calls += "close" }
        override fun toggleKeyboard() { calls += "keyboard" }
        override fun toggleTrackpad() { calls += "trackpad" }
        override fun apps() { calls += "apps" }
        override fun overview() { calls += "overview" }
        override fun sendClipboard() { calls += "send" }
        override fun getClipboard() { calls += "get" }
        override fun toggleStats() { calls += "stats" }
        override fun printScreen() { calls += "print" }
        override fun setScreen(mode: String) { calls += "screen $mode" }
        override fun setPreset(preset: StreamPreset) { calls += "preset $preset" }
        override fun setCodec(codec: String) { calls += "codec $codec" }
        override fun setSharperColours(on: Boolean) { calls += "sharperColours $on" }
        override fun setExactText(on: Boolean) { calls += "exactText $on" }
        override fun setSound(on: Boolean) { calls += "sound $on" }
        override fun setTrackpadSpeed(speed: Float) { calls += "trackpadSpeed $speed" }
        override fun chooseMonitor(connector: String) { calls += "monitor $connector" }
        override fun changeScale(direction: Int) { calls += "scale $direction" }
        override fun setPrimary(on: Boolean) { calls += "primary $on" }
        override fun setDeskOff(off: Boolean) { calls += "deskOff $off" }
        override fun setMic(on: Boolean) { calls += "mic $on" }
        override fun setKeepAwake(minutes: Int) { calls += "keepAwake $minutes" }
        override fun setLockAfter(on: Boolean) { calls += "lockAfter $on" }
        override fun disconnect() { calls += "disconnect" }
    }
}
