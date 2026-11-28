package com.desklink.client.ui

import android.app.Application
import android.content.ComponentName
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createEmptyComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.core.app.ActivityScenario
import androidx.test.core.app.ApplicationProvider
import com.desklink.client.R
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows
import org.robolectric.annotation.Config

/** The connecting page: which stage a failure lands on, and what the page shows and does. */
@RunWith(RobolectricTestRunner::class)
@Config(qualifiers = "w1205dp-h753dp-land-notnight-340dpi")
class ConnectingPageTest {
    @get:Rule
    val compose = createEmptyComposeRule()

    private val app: Application = ApplicationProvider.getApplicationContext()
    private val calls = mutableListOf<String>()
    private var scenario: ActivityScenario<ComponentActivity>? = null

    @After
    fun close() {
        scenario?.close()
    }

    @Test
    fun aFailureLandsOnTheRunningStage() {
        val p = ConnectProgress.starting("a", "cm", 0)
            .with(ConnectStage.REACH, StageState.Done(12))
            .with(ConnectStage.PICTURE, StageState.Running(5))
            .failRunning("UDP is blocked", "check")
        assertEquals(StageState.Failed("UDP is blocked", "check"), p.stages[ConnectStage.PICTURE])
        assertTrue(p.failed)
        assertFalse(p.done)
    }

    @Test
    fun aFailureBetweenStagesLandsOnTheNextOne() {
        val p = ConnectProgress.starting("a", "cm", 0).with(ConnectStage.REACH, StageState.Done(12)).failRunning("closed", "check")
        assertTrue(p.stages[ConnectStage.PICTURE] is StageState.Failed)
    }

    @Test
    fun theFirstFrameEndsThePage() {
        val p = ConnectProgress.starting("a", "cm", 0).with(ConnectStage.FRAME, StageState.Done(40))
        assertTrue(p.done)
    }

    @Test
    fun waitingShowsEveryStageAndCancelStops() {
        show(ConnectProgress.starting("a", "cm", 0).with(ConnectStage.REACH, StageState.Done(12, "192.168.178.11")))
        compose.onNodeWithText(app.getString(R.string.stage_reach_done_at, "192.168.178.11")).assertIsDisplayed()
        compose.onNodeWithText(app.getString(R.string.stage_picture)).assertIsDisplayed()
        compose.onNodeWithText(app.getString(R.string.action_cancel)).performClick()
        assertEquals(listOf("cancel"), calls)
    }

    @Test
    fun aFailureSaysWhatToCheckAndOffersTryAgain() {
        show(
            ConnectProgress.starting("a", "cm", 0)
                .with(ConnectStage.WAKE, StageState.Failed("cm did not wake up", "Check that it is wired.")),
        )
        compose.onNodeWithText(app.getString(R.string.stage_wake_failed)).assertIsDisplayed()
        compose.onNodeWithText("Check that it is wired.").assertIsDisplayed()
        compose.onNodeWithText(app.getString(R.string.action_try_again)).performClick()
        assertEquals(listOf("retry"), calls)
    }

    private fun show(progress: ConnectProgress) {
        Shadows.shadowOf(app.packageManager).addActivityIfNotPresent(ComponentName(app, ComponentActivity::class.java))
        scenario = ActivityScenario.launch(ComponentActivity::class.java).onActivity {
            it.setContent {
                DeskLinkTheme {
                    ConnectingPage(
                        progress,
                        object : ConnectingActions {
                            override fun cancel() {
                                calls += "cancel"
                            }

                            override fun retry() {
                                calls += "retry"
                            }
                        },
                        now = 12,
                    )
                }
            }
        }
    }
}
