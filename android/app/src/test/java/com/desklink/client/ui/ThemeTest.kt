package com.desklink.client.ui

import android.app.Application
import android.content.ComponentName
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.test.junit4.createEmptyComposeRule
import androidx.core.view.WindowCompat
import androidx.test.core.app.ActivityScenario
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows
import org.robolectric.annotation.Config

/**
 * The bars' icons on Robolectric: the window's theme is dark, so without
 * DeskLinkTheme the clock and icons were white on the light theme's pages.
 */
@RunWith(RobolectricTestRunner::class)
class ThemeTest {
    @get:Rule
    val compose = createEmptyComposeRule()

    private val app: Application = ApplicationProvider.getApplicationContext()

    @Test
    @Config(qualifiers = "notnight")
    fun theLightThemeGetsDarkIcons() = assertEquals(Bars(status = true, navigation = true), bars(overVideo = false))

    @Test
    @Config(qualifiers = "night")
    fun theDarkThemeGetsLightIcons() = assertEquals(Bars(status = false, navigation = false), bars(overVideo = false))

    @Test
    @Config(qualifiers = "notnight")
    fun theStreamGetsLightIconsInTheLightTheme() = assertEquals(Bars(status = false, navigation = false), bars(overVideo = true))

    @Test
    @Config(qualifiers = "notnight")
    fun leavingTheStreamGivesThePageDarkIconsAgain() {
        var overVideo by mutableStateOf(true)
        launch { DeskLinkTheme(overVideo = overVideo) {} }.use { scenario ->
            compose.waitForIdle()
            overVideo = false
            compose.waitForIdle()
            assertEquals(Bars(status = true, navigation = true), read(scenario))
        }
    }

    private data class Bars(val status: Boolean, val navigation: Boolean)

    private fun bars(overVideo: Boolean): Bars = launch { DeskLinkTheme(overVideo = overVideo) {} }.use { scenario ->
        compose.waitForIdle()
        read(scenario)
    }

    private fun launch(content: @Composable () -> Unit): ActivityScenario<ComponentActivity> {
        // Robolectric knows only the app's own activities; this plain one hosts the theme.
        Shadows.shadowOf(app.packageManager).addActivityIfNotPresent(ComponentName(app, ComponentActivity::class.java))
        return ActivityScenario.launch(ComponentActivity::class.java).onActivity { it.setContent(content = content) }
    }

    private fun read(scenario: ActivityScenario<ComponentActivity>): Bars {
        var bars: Bars? = null
        scenario.onActivity {
            val controller = WindowCompat.getInsetsController(it.window, it.window.decorView)
            bars = Bars(controller.isAppearanceLightStatusBars, controller.isAppearanceLightNavigationBars)
        }
        return bars!!
    }
}
