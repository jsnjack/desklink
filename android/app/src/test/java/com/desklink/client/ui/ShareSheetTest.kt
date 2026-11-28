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
import com.desklink.client.model.HostProfile
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(qualifiers = "w753dp-h1205dp-port-notnight-340dpi")
class ShareSheetTest {
    @get:Rule
    val compose = createEmptyComposeRule()

    private val app: Application = ApplicationProvider.getApplicationContext()
    private val workstation = HostProfile("work", "Workstation", HostProfile.DEFAULT_IP)
    private val studio = HostProfile("studio", "Studio", HostProfile.DEFAULT_IP)
    private val file = ShareItem(0, SharedSource.Text("Notes.txt", "notes"), "Notes.txt", 5)
    private val calls = mutableListOf<String>()
    private var scenario: ActivityScenario<ComponentActivity>? = null

    @After
    fun close() {
        scenario?.close()
    }

    @Test
    fun showsTheFileAndWaitsForConfirmationWithOneComputer() {
        show(ShareUiState(listOf(workstation), listOf(file)))

        compose.onNodeWithText(file.name).assertIsDisplayed()
        compose.onNodeWithText(app.getString(R.string.share_downloads)).assertIsDisplayed()
        assertEquals(emptyList<String>(), calls)
        compose.onNodeWithText(app.getString(R.string.share_send_to, workstation.name)).performClick()
        assertEquals(listOf(workstation.id), calls)
    }

    @Test
    fun sendsOnlyToTheSelectedComputer() {
        show(ShareUiState(listOf(workstation, studio), listOf(file)))

        compose.onNodeWithText(studio.name).performClick()
        compose.onNodeWithText(app.getString(R.string.share_send_to, workstation.name)).assertDoesNotExist()
        compose.onNodeWithText(app.getString(R.string.share_send_to, studio.name)).performClick()
        assertEquals(listOf(studio.id), calls)
    }

    @Test
    fun showsTheComputerBeingReachedAndOffersCancel() {
        show(ShareUiState(listOf(workstation, studio), listOf(file), target = studio, reaching = true))

        compose.onNodeWithText(app.getString(R.string.share_reaching, studio.name)).assertIsDisplayed()
        compose.onNodeWithText(app.getString(R.string.share_send_to, studio.name)).assertDoesNotExist()
        compose.onNodeWithText(app.getString(R.string.action_cancel)).performClick()
        assertEquals(listOf("cancel"), calls)
    }

    private fun show(state: ShareUiState) {
        Shadows.shadowOf(app.packageManager).addActivityIfNotPresent(ComponentName(app, ComponentActivity::class.java))
        scenario = ActivityScenario.launch(ComponentActivity::class.java).onActivity {
            it.setContent {
                DeskLinkTheme {
                    ShareSheet(state, { host -> calls += host.id }, { calls += "cancel" }, { calls += "close" })
                }
            }
        }
    }
}
