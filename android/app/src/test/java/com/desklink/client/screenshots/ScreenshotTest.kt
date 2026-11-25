package com.desklink.client.screenshots

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxScope
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import com.desklink.client.input.TouchMode
import com.desklink.client.model.HostProfile
import com.desklink.client.model.LevelInfo
import com.desklink.client.model.NearbyHost
import com.desklink.client.model.ScreenMode
import com.desklink.client.model.SessionInfo
import com.desklink.client.net.LinkStats
import com.desklink.client.net.StatsHistory
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import com.desklink.client.ui.HostHealth
import com.desklink.client.ui.HostItem
import com.desklink.client.ui.HostsActions
import com.desklink.client.ui.HostsScreen
import com.desklink.client.ui.HostsUiState
import com.desklink.client.ui.PanelState
import com.desklink.client.ui.SettingsActions
import com.desklink.client.ui.SettingsScreen
import com.desklink.client.ui.SettingsUiState
import com.desklink.client.ui.StatsDetails
import com.desklink.client.ui.StatsPanel
import com.desklink.client.ui.StatsPill
import com.desklink.client.ui.VideoRect
import com.desklink.client.model.StreamPreset
import com.desklink.client.net.LinkQuality
import com.desklink.client.video.DecoderOption
import com.desklink.client.video.DecoderRating
import com.desklink.client.video.DecoderReport
import com.desklink.client.video.RenderSecond
import com.desklink.client.video.VideoRendering
import com.github.takahirom.roborazzi.captureRoboImage
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import java.io.File
import kotlin.math.sin

/**
 * Renders the main page, the settings and the stats overlay with made-up
 * state, as the Galaxy Tab S9 shows them, into the directory `make
 * screenshots` names. They are for looking at: nothing compares them.
 */
@RunWith(RobolectricTestRunner::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class ScreenshotTest {
    @Test
    @Config(qualifiers = LANDSCAPE_DARK)
    fun mainLandscapeDark() = capture("main-landscape-dark") { HostsScreen(hosts(), NoHostsActions) }

    @Test
    @Config(qualifiers = LANDSCAPE_LIGHT)
    fun mainLandscapeLight() = capture("main-landscape-light") {
        HostsScreen(hosts().copy(nearby = listOf(nearby), metered = true), NoHostsActions)
    }

    @Test
    @Config(qualifiers = LANDSCAPE_DARK)
    fun mainLandscapeOfflineDark() = capture("main-landscape-offline-dark") {
        HostsScreen(hosts().copy(selectedId = home.id, preset = StreamPreset.BALANCED, screenMode = ScreenMode.OWN), NoHostsActions)
    }

    @Test
    @Config(qualifiers = LANDSCAPE_DARK)
    fun mainLandscapeUnpairedDark() = capture("main-landscape-unpaired-dark") {
        val fresh = HostProfile("c", "Desktop", HostProfile.DEFAULT_IP)
        HostsScreen(
            hosts().let { it.copy(hosts = it.hosts + HostItem(fresh, HostHealth.Checking), selectedId = fresh.id, nearby = listOf(nearby)) },
            NoHostsActions,
        )
    }

    @Test
    @Config(qualifiers = PORTRAIT_DARK)
    fun mainPortraitDark() = capture("main-portrait-dark") { HostsScreen(hosts(), NoHostsActions) }

    @Test
    @Config(qualifiers = PORTRAIT_LIGHT)
    fun mainPortraitLight() = capture("main-portrait-light") {
        HostsScreen(hosts().copy(nearby = listOf(nearby), metered = true), NoHostsActions)
    }

    @Test
    @Config(qualifiers = LANDSCAPE_DARK)
    fun emptyDark() = capture("empty-dark") { HostsScreen(HostsUiState(emptyList(), null), NoHostsActions) }

    @Test
    @Config(qualifiers = LANDSCAPE_LIGHT)
    fun emptyWithNearbyLight() = capture("empty-nearby-light") {
        HostsScreen(HostsUiState(emptyList(), null, nearby = listOf(nearby)), NoHostsActions)
    }

    @Test
    @Config(qualifiers = LANDSCAPE_DARK)
    fun settingsDark() = capture("settings-dark") { SettingsScreen(settings, NoSettingsActions) }

    @Test
    @Config(qualifiers = LANDSCAPE_LIGHT)
    fun settingsLight() = capture("settings-light") {
        SettingsScreen(
            settings.copy(touchMode = TouchMode.TRACKPAD, sharperColours = false, keyCapture = false, screenLock = false),
            NoSettingsActions,
        )
    }

    @Test
    @Config(qualifiers = PORTRAIT_DARK)
    fun settingsPortraitDark() = capture("settings-portrait-dark") {
        SettingsScreen(settings.copy(touchMode = TouchMode.TRACKPAD), NoSettingsActions)
    }

    @Test
    @Config(qualifiers = LANDSCAPE_DARK)
    fun statsPill() = capture("stats-pill") {
        Desktop { StatsPill(120, LinkQuality.EXCELLENT, onClick = {}, Modifier.align(Alignment.TopStart)) }
    }

    @Test
    @Config(qualifiers = LANDSCAPE_DARK)
    fun statsPillPoor() = capture("stats-pill-poor") {
        Desktop { StatsPill(41, LinkQuality.POOR, onClick = {}, Modifier.align(Alignment.TopStart)) }
    }

    @Test
    @Config(qualifiers = LANDSCAPE_DARK)
    fun statsPanel() = capture("stats-panel") {
        Desktop { StatsPanel(stats, onClose = {}, Modifier.align(Alignment.TopStart)) }
    }

    /**
     * Renders [content] with Material's baseline colours: the app takes them
     * from the wallpaper ([com.desklink.client.ui.DeskLinkTheme]), and
     * Robolectric's stand-in for the wallpaper palette gives light containers
     * dark colours that no device shows.
     */
    private fun capture(name: String, content: @Composable () -> Unit) {
        val dir = System.getProperty("desklink.screenshots") ?: error("run through make screenshots")
        captureRoboImage(File(dir, "$name.png").path) {
            MaterialTheme(colorScheme = if (isSystemInDarkTheme()) darkColorScheme() else lightColorScheme(), content = content)
        }
    }

    /** A stand-in for the streamed desktop under the overlay. */
    @Composable
    private fun Desktop(content: @Composable BoxScope.() -> Unit) {
        Box(
            Modifier.fillMaxSize().background(Brush.linearGradient(listOf(Color(0xFF1F2B3D), Color(0xFF2B2333), Color(0xFF1B2A2A)))),
            content = content,
        )
    }

    private val office = HostProfile(
        "a", "cm", "192.168.178.20", peerId = "peer", fingerprint = "3f2a9c1b7d4e8f60a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718",
        wrappedKey = "key", mac = "aa:bb:cc:dd:ee:ff",
    )
    private val home = HostProfile(
        "b", "Home PC", "192.168.1.30", peerId = "peer2", fingerprint = "9c8b7a6f5e4d3c2b1a09f8e7d6c5b4a39281706f5e4d3c2b1a09f8e7d6c5b4a3",
        wrappedKey = "key", mac = "11:22:33:44:55:66",
    )
    private val nearby = NearbyHost("studio", "192.168.178.42", HostProfile.DEFAULT_PORT, "ab".repeat(32))

    private fun hosts() = HostsUiState(
        hosts = listOf(
            HostItem(office, HostHealth.Online("cm", "0.50.2", 12)),
            HostItem(home, HostHealth.Offline("no answer on 192.168.1.30, 10.8.0.6")),
        ),
        selectedId = office.id,
    )

    private val settings = SettingsUiState(
        sharperColours = true, exactText = true, touchMode = TouchMode.DIRECT, trackpadSpeed = 1.5f, keyCapture = true,
        appLock = false, screenLock = true, lockAfter = true, codec = "av1", av1Available = true, version = "0.50.2 (50002)",
    )

    private val stats = run {
        val now = LinkStats(
            rttMs = 9, fps = 120, mbps = 18.4, lossPct = 0.0, receivedBytes = 1_288_490_189, delayMs = 14,
            decodeMicros = 3_100, decodeP95Micros = 5_200, framesLost = 0, keyframeRequests = 1,
        )
        val history = (0 until StatsHistory.CAPACITY).fold(StatsHistory()) { h, i ->
            h + now.copy(
                fps = 110 + (10 * sin(i / 4.0)).toInt(), mbps = 14 + 6 * sin(i / 7.0), rttMs = 8 + (3 * sin(i / 3.0)).toInt(),
                lossPct = if (i % 17 == 0) 0.4 else 0.0, delayMs = 13 + (2 * sin(i / 5.0)).toInt(),
            )
        }
        StatsDetails(
            stats = now,
            info = SessionInfo(
                sessionId = 1, udpPort = 9444, codec = "av1", width = 2560, height = 1600, fps = 120, qp = 12,
                display = "own 2560x1600 screen at 200%", auto = true, level = 0,
                levels = listOf(LevelInfo(2560, 1600, 120), LevelInfo(2560, 1600, 60), LevelInfo(1920, 1200, 60)),
                screen = ScreenMode.OWN, scale = 2.0,
            ),
            shown = VideoRect(0, 0, 2560, 1600),
            maxFps = 0,
            panel = PanelState(120f, 120f, powerSave = false),
            decoder = DecoderReport(
                "c2.qti.av1.decoder", setOf(DecoderOption.LOW_LATENCY, DecoderOption.REALTIME), 2560, 1600, 120,
                DecoderRating.RATED, "", emptyList(),
            ),
            rendering = VideoRendering.Guided(RenderSecond(120, 0, 1_300, 2_100), gpuTimed = true),
            exactTiles = 412,
            history = history,
            soc = "SM8550",
            mediaClass = 34,
        )
    }

    private object NoHostsActions : HostsActions {
        override fun select(host: HostProfile) = Unit
        override fun connect(host: HostProfile) = Unit
        override fun wake(host: HostProfile) = Unit
        override fun pair(host: HostProfile) = Unit
        override fun pairNearby(host: NearbyHost) = Unit
        override fun scanQr() = Unit
        override fun addHost() = Unit
        override fun check(host: HostProfile) = Unit
        override fun test(host: HostProfile) = Unit
        override fun shareLogs() = Unit
        override fun forget(host: HostProfile) = Unit
        override fun setPreset(preset: StreamPreset) = Unit
        override fun setScreenMode(mode: String) = Unit
        override fun setPlayAudio(play: Boolean) = Unit
        override fun openSettings() = Unit
    }

    private object NoSettingsActions : SettingsActions {
        override fun back() = Unit
        override fun setSharperColours(on: Boolean) = Unit
        override fun setExactText(on: Boolean) = Unit
        override fun setTouchMode(mode: TouchMode) = Unit
        override fun setTrackpadSpeed(speed: Float) = Unit
        override fun openKeyCaptureSettings() = Unit
        override fun setAppLock(on: Boolean) = Unit
        override fun setLockAfter(on: Boolean) = Unit
        override fun setCodec(codec: String) = Unit
    }

    companion object {
        // The Galaxy Tab S9: 2560×1600 pixels at 340 dpi.
        const val LANDSCAPE_DARK = "w1205dp-h753dp-land-night-340dpi"
        const val LANDSCAPE_LIGHT = "w1205dp-h753dp-land-notnight-340dpi"
        const val PORTRAIT_DARK = "w753dp-h1205dp-port-night-340dpi"
        const val PORTRAIT_LIGHT = "w753dp-h1205dp-port-notnight-340dpi"
    }
}
