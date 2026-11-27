package com.desklink.client.screenshots

import android.app.Application
import android.content.ComponentName
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.junit4.createEmptyComposeRule
import androidx.compose.ui.test.longClick
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTouchInput
import androidx.test.core.app.ActivityScenario
import androidx.test.core.app.ApplicationProvider
import com.desklink.client.R
import com.desklink.client.model.HostSettings
import com.desklink.client.net.LinkStats
import com.desklink.client.net.StreamSession
import com.desklink.client.ui.HudSheet
import com.desklink.client.ui.hudUiState
import com.github.takahirom.roborazzi.captureScreenRoboImage
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import java.io.File

/**
 * Renders the HUD with a popup open, the Keep awake menu or a long press's
 * explanation: a gesture opens it, and the capture takes every window, the
 * popup's included. Like [ScreenshotTest], only for `make screenshots`.
 */
@RunWith(RobolectricTestRunner::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class HudPopupScreenshotTest {
    @get:Rule
    val compose = createEmptyComposeRule()

    private val app: Application = ApplicationProvider.getApplicationContext()

    @Test
    @Config(qualifiers = ScreenshotTest.LANDSCAPE_DARK)
    fun keepAwakeMenu() = capture("hud-keep-awake-menu") {
        compose.onNodeWithText(app.getString(R.string.hud_keep_awake)).performClick()
        compose.waitForIdle()
    }

    @Test
    @Config(qualifiers = ScreenshotTest.LANDSCAPE_DARK)
    fun longPressTip() = capture("hud-long-press-tip") {
        compose.onNodeWithText(app.getString(R.string.label_desk_off)).performTouchInput { longClick() }
        compose.waitForIdle()
    }

    /** Shows the own-screen session's HUD, runs [open] and captures the screen with its popups. */
    private fun capture(name: String, open: () -> Unit) {
        val dir = System.getProperty("desklink.screenshots") ?: error("run through make screenshots")
        // Robolectric knows only the app's own activities; this plain one hosts the sheet.
        Shadows.shadowOf(app.packageManager).addActivityIfNotPresent(ComponentName(app, ComponentActivity::class.java))
        ActivityScenario.launch(ComponentActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                activity.setContent {
                    MaterialTheme(colorScheme = darkColorScheme()) {
                        Desktop {
                            HudSheet(
                                hudUiState(
                                    "cm", StreamSession.State.Streaming(ownScreenSession), LinkStats(rttMs = 9, fps = 120),
                                    keyboardShown = false, statsShown = true, settings = HostSettings(), av1Available = true,
                                ),
                                NoHudActions, Modifier.align(Alignment.TopEnd),
                            )
                        }
                    }
                }
            }
            open()
            captureScreenRoboImage(File(dir, "$name.png").path)
        }
    }
}
