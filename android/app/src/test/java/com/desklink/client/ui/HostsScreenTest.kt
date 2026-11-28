package com.desklink.client.ui

import android.app.Application
import android.content.ComponentName
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createEmptyComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.core.app.ActivityScenario
import androidx.test.core.app.ApplicationProvider
import com.desklink.client.R
import com.desklink.client.model.HostProfile
import com.desklink.client.model.NearbyHost
import com.desklink.client.model.StreamPreset
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows
import org.robolectric.annotation.Config

/** The main page's list and detail on Robolectric: what the selected host's state shows and what a tap does. */
@RunWith(RobolectricTestRunner::class)
@Config(qualifiers = "w1205dp-h753dp-land-notnight-340dpi")
class HostsScreenTest {
    @get:Rule
    val compose = createEmptyComposeRule()

    private val app: Application = ApplicationProvider.getApplicationContext()
    private val calls = mutableListOf<String>()
    private var scenario: ActivityScenario<ComponentActivity>? = null

    private val cm = HostProfile("a", "cm", "192.168.178.20", peerId = "p", fingerprint = "f", wrappedKey = "k", mac = "aa:bb:cc:dd:ee:ff")
    private val home = HostProfile("b", "home-pc", "192.168.1.30", peerId = "p2", fingerprint = "f2", wrappedKey = "k2", mac = "11:22:33:44:55:66")

    @After
    fun close() {
        scenario?.close()
    }

    @Test
    fun aRefusedHostSaysWhatFailedAndOffersTryAgain() {
        show(HostHealth.Offline("refused", HostHealth.Why.REFUSED, address = "192.168.1.30"))
        compose.onNodeWithText(app.getString(R.string.err_refused_title, "home-pc")).assertIsDisplayed()
        compose.onNodeWithText(app.getString(R.string.action_try_again)).performClick()
        assertEquals(listOf("connect home-pc"), calls)
    }

    @Test
    fun aSleepingHostConnectsWithoutABanner() {
        show(HostHealth.Offline("no answer", HostHealth.Why.NO_ANSWER, tried = 2))
        compose.onNodeWithText(app.getString(R.string.err_did_not_answer, "home-pc")).assertDoesNotExist()
        // In the list and beside the name.
        compose.onAllNodesWithText(app.getString(R.string.status_offline_wakes)).assertCountEquals(2)
        compose.onNodeWithText(app.getString(R.string.action_connect)).performClick()
        assertEquals(listOf("connect home-pc"), calls)
    }

    @Test
    fun aHostThatCannotBeWokenSaysHowManyAddressesWereTried() {
        show(HostHealth.Offline("no answer", HostHealth.Why.NO_ANSWER, tried = 3), mac = null)
        compose.onNodeWithText(app.resources.getQuantityString(R.plurals.err_did_not_answer_body, 3, 3)).assertIsDisplayed()
    }

    @Test
    fun aTapOnARowSelectsItsHost() {
        show(HostHealth.Checking, selected = "b")
        compose.onNodeWithText("cm").performClick()
        assertEquals(listOf("select cm"), calls)
    }

    private fun show(homeHealth: HostHealth, selected: String = "b", mac: String? = home.mac) {
        val state = HostsUiState(
            hosts = listOf(HostItem(cm, HostHealth.Online("cm", "0.65.0", 12)), HostItem(home.copy(mac = mac), homeHealth)),
            selected = selected,
        )
        Shadows.shadowOf(app.packageManager).addActivityIfNotPresent(ComponentName(app, ComponentActivity::class.java))
        scenario = ActivityScenario.launch(ComponentActivity::class.java).onActivity {
            it.setContent { DeskLinkTheme { HostsScreen(state, recorder) } }
        }
    }

    private val recorder = object : HostsActions {
        override fun connect(host: HostProfile) {
            calls += "connect ${host.name}"
        }

        override fun select(host: HostProfile) {
            calls += "select ${host.name}"
        }

        override fun wake(host: HostProfile) = Unit
        override fun pair(host: HostProfile) = Unit
        override fun pairNearby(host: NearbyHost) = Unit
        override fun scanQr() = Unit
        override fun addHost() = Unit
        override fun addComputer() = Unit
        override fun check(host: HostProfile) = Unit
        override fun test(host: HostProfile) = Unit
        override fun forget(host: HostProfile) = Unit
        override fun openSettings() = Unit
        override fun openHostSettings(host: HostProfile) = Unit
        override fun setPreset(host: HostProfile, preset: StreamPreset) = Unit
        override fun setScreenMode(host: HostProfile, mode: String) = Unit
        override fun setPlayAudio(host: HostProfile, play: Boolean) = Unit
        override fun setExactText(host: HostProfile, on: Boolean) = Unit
        override fun setDeskOff(host: HostProfile, off: Boolean) = Unit
    }
}
