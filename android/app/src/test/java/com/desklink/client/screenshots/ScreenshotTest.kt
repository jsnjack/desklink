package com.desklink.client.screenshots

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxScope
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalInspectionMode
import com.desklink.client.input.TouchMode
import com.desklink.client.model.HostProfile
import com.desklink.client.model.HostSettings
import com.desklink.client.model.KeepAwake
import com.desklink.client.model.LevelInfo
import com.desklink.client.model.MonitorInfo
import com.desklink.client.model.NearbyHost
import com.desklink.client.model.ScreenMode
import com.desklink.client.model.SessionInfo
import com.desklink.client.model.StreamPreset
import com.desklink.client.net.ExactTileStats
import com.desklink.client.net.LinkQuality
import com.desklink.client.net.LinkStats
import com.desklink.client.net.StatsHistory
import com.desklink.client.net.StreamSession
import com.desklink.client.ui.AboutScreen
import com.desklink.client.ui.ConnectProgress
import com.desklink.client.ui.ConnectStage
import com.desklink.client.ui.ConnectingActions
import com.desklink.client.ui.ConnectingPage
import com.desklink.client.ui.DeskLinkTheme
import com.desklink.client.ui.HostHealth
import com.desklink.client.ui.HostItem
import com.desklink.client.ui.HostSettingsActions
import com.desklink.client.ui.HostSettingsScreen
import com.desklink.client.ui.HostSettingsUiState
import com.desklink.client.ui.HostsActions
import com.desklink.client.ui.HostsScreen
import com.desklink.client.ui.HostsUiState
import com.desklink.client.ui.HudActions
import com.desklink.client.ui.HudPill
import com.desklink.client.ui.HudSheet
import com.desklink.client.ui.HudTheme
import com.desklink.client.ui.HudUiState
import com.desklink.client.ui.PairScreen
import com.desklink.client.ui.PanelState
import com.desklink.client.ui.ReconnectingOverlay
import com.desklink.client.ui.SessionPill
import com.desklink.client.ui.SettingsActions
import com.desklink.client.ui.SettingsScreen
import com.desklink.client.ui.SettingsUiState
import com.desklink.client.ui.StageState
import com.desklink.client.ui.StatsDetails
import com.desklink.client.ui.StatsPanel
import com.desklink.client.ui.StatsPill
import com.desklink.client.ui.ToolRail
import com.desklink.client.ui.VideoRect
import com.desklink.client.ui.hudUiState
import com.desklink.client.video.DecoderOption
import com.desklink.client.video.DecoderRating
import com.desklink.client.video.DecoderReport
import com.desklink.client.video.RenderSecond
import com.desklink.client.video.VideoRendering
import com.github.takahirom.roborazzi.captureRoboImage
import java.io.File
import kotlin.math.sin
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode

/**
 * Renders the main page, the settings page, the stats overlay and the HUD
 * sheet with made-up state, as the Galaxy Tab S9 shows them, into the
 * directory `make screenshots` names. They are for looking at: nothing
 * compares them.
 */
@RunWith(RobolectricTestRunner::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class ScreenshotTest {
    @Test
    @Config(qualifiers = LANDSCAPE_DARK)
    fun mainLandscapeDark() = capture("main-landscape-dark") { HostsScreen(hosts(), NoHostsActions) }

    /** On a metered network with a host nearby. */
    @Test
    @Config(qualifiers = LANDSCAPE_LIGHT)
    fun mainLandscapeLight() = capture("main-landscape-light") {
        HostsScreen(hosts().copy(nearby = listOf(nearby), metered = true), NoHostsActions)
    }

    /** Other choices for the next session on the office desk. */
    @Test
    @Config(qualifiers = LANDSCAPE_DARK)
    fun mainLandscapeOtherChoicesDark() = capture("main-landscape-choices-dark") {
        val choices = HostSettings(preset = StreamPreset.BALANCED, screen = ScreenMode.OWN, sound = false)
        HostsScreen(hosts().let { it.copy(hosts = listOf(it.hosts[0].copy(settings = choices)) + it.hosts.drop(1)) }, NoHostsActions)
    }

    @Test
    @Config(qualifiers = LANDSCAPE_DARK)
    fun mainLandscapeUnpairedDark() = capture("main-landscape-unpaired-dark") {
        val fresh = HostProfile("c", "Desktop", HostProfile.DEFAULT_IP)
        HostsScreen(
            hosts().let { it.copy(hosts = it.hosts + HostItem(fresh, HostHealth.Checking), nearby = listOf(nearby)) },
            NoHostsActions,
        )
    }

    @Test
    @Config(qualifiers = LANDSCAPE_LIGHT)
    fun mainLandscapeRefusedLight() = capture("main-landscape-refused-light") {
        val refused = HostHealth.Offline("192.168.1.30 is on, but desklinkd is not running there", HostHealth.Why.REFUSED, address = "192.168.1.30")
        HostsScreen(
            hosts().let { it.copy(hosts = it.hosts.map { h -> if (h.profile.id == "b") h.copy(health = refused) else h }, selected = "b") },
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
    fun settingsDark() = capture("settings-dark") { SettingsScreen(settings, VERSION, NoSettingsActions, {}, {}, {}) }

    /** With the shortcuts service off and no screen lock. */
    @Test
    @Config(qualifiers = PORTRAIT_LIGHT)
    fun settingsPortraitLight() = capture("settings-portrait-light") {
        SettingsScreen(settings.copy(keyCapture = false, screenLock = false), VERSION, NoSettingsActions, {}, {}, {})
    }

    @Test
    @Config(qualifiers = LANDSCAPE_LIGHT)
    fun aboutLight() = capture("about-light") { AboutScreen(VERSION, {}, {}) }

    @Test
    @Config(qualifiers = LANDSCAPE_DARK)
    fun hostSettingsDark() = capture("host-settings-dark") {
        HostSettingsScreen(HostSettingsUiState("cm", officeSettings, "av1", av1Available = true, listOf("192.168.178.20", "10.8.0.2"), "aa:bb:cc:dd:ee:ff"), NoHostSettingsActions, {})
    }

    /** In trackpad mode, with Exact text waiting for Sharper colours, on a tablet without a hardware AV1 decoder. */
    @Test
    @Config(qualifiers = PORTRAIT_LIGHT)
    fun hostSettingsPortraitLight() = capture("host-settings-portrait-light") {
        val limited = officeSettings.copy(touch = TouchMode.TRACKPAD, sharperColours = false, screen = ScreenMode.MIRROR, scale = 1.25)
        HostSettingsScreen(HostSettingsUiState("Home PC", limited, "hevc", av1Available = false), NoHostSettingsActions, {})
    }

    @Test
    @Config(qualifiers = LANDSCAPE_DARK)
    fun emptyDark() = capture("empty-dark") { PairScreen(emptyList(), pairing = false, first = true, NoHostsActions, onBack = null) }

    @Test
    @Config(qualifiers = LANDSCAPE_LIGHT)
    fun emptyWithNearbyLight() = capture("empty-nearby-light") {
        PairScreen(listOf(nearby), pairing = false, first = false, NoHostsActions, onBack = {})
    }

    @Test
    @Config(qualifiers = LANDSCAPE_DARK)
    fun statsPill() = capture("stats-pill") {
        Desktop { StatsPill(120, 9, LinkQuality.EXCELLENT, onClick = {}, Modifier.align(Alignment.TopStart)) }
    }

    @Test
    @Config(qualifiers = LANDSCAPE_DARK)
    fun statsPillPoor() = capture("stats-pill-poor") {
        Desktop { StatsPill(41, 212, LinkQuality.POOR, onClick = {}, Modifier.align(Alignment.TopStart)) }
    }

    @Test
    @Config(qualifiers = LANDSCAPE_DARK)
    fun statsPanel() = capture("stats-panel") {
        Desktop { StatsPanel(stats, onClose = {}, Modifier.align(Alignment.TopStart)) }
    }

    /** The HUD of an own-screen session beside an unlocked desk, which offers Primary, Desk screens off and the microphone. */
    @Test
    @Config(qualifiers = LANDSCAPE_DARK)
    fun hudOwnScreen() = capture("hud-own") {
        Desktop {
            StatsPill(120, 9, LinkQuality.EXCELLENT, onClick = {}, Modifier.align(Alignment.TopStart))
            HudSheet(hud(StreamSession.State.Streaming(ownScreenSession)), NoHudActions, Modifier.align(Alignment.TopEnd))
        }
    }

    /** The HUD of a session mirroring one of two monitors, from a host without a microphone, in trackpad mode. */
    @Test
    @Config(qualifiers = LANDSCAPE_DARK)
    fun hudMirror() = capture("hud-mirror") {
        Desktop {
            HudSheet(
                hud(StreamSession.State.Streaming(mirrorSession), trackpad = true, statsShown = false, quality = LinkQuality.FAIR),
                NoHudActions, Modifier.align(Alignment.TopEnd),
            )
        }
    }

    @Test
    @Config(qualifiers = LANDSCAPE_DARK)
    fun hudConnecting() = capture("hud-connecting") {
        Desktop { HudSheet(hud(StreamSession.State.Connecting), NoHudActions, Modifier.align(Alignment.TopEnd)) }
    }

    @Test
    @Config(qualifiers = LANDSCAPE_DARK)
    fun hudReconnecting() = capture("hud-reconnecting") {
        Desktop { HudSheet(hud(StreamSession.State.Reconnecting(3, 10)), NoHudActions, Modifier.align(Alignment.TopEnd)) }
    }

    @Test
    @Config(qualifiers = LANDSCAPE_LIGHT)
    fun connectingLight() = capture("connecting-light") {
        val now = 24_000L
        ConnectingPage(
            ConnectProgress.starting("a", "cm", now)
                .with(ConnectStage.WAKE, StageState.Done(21_000))
                .with(ConnectStage.REACH, StageState.Done(12, "192.168.178.20"))
                .with(ConnectStage.PICTURE, StageState.Running(now - 2_000)),
            NoConnectingActions,
            now,
        )
    }

    @Test
    @Config(qualifiers = LANDSCAPE_DARK)
    fun connectingFailedDark() = capture("connecting-failed-dark") {
        ConnectingPage(
            ConnectProgress.starting("a", "cm", 0).with(
                ConnectStage.REACH,
                StageState.Failed("desklinkd is not running on cm", "cm answered at 192.168.178.20 but refused the connection."),
            ),
            NoConnectingActions,
            now = 0,
        )
    }

    @Test
    @Config(qualifiers = LANDSCAPE_LIGHT)
    fun reconnecting() = capture("reconnecting") {
        Desktop {
            ReconnectingOverlay(StreamSession.State.Reconnecting(2, 10))
            SessionPill(HudPill(left = false, y = 0.35f), {}, {})
        }
    }

    @Test
    @Config(qualifiers = LANDSCAPE_LIGHT)
    fun sessionPill() = capture("session-pill") { Desktop { SessionPill(HudPill(left = false, y = 0.35f), {}, {}) } }

    @Test
    @Config(qualifiers = LANDSCAPE_LIGHT)
    fun sessionRail() = capture("session-rail") {
        Desktop { ToolRail(hud(StreamSession.State.Streaming(ownScreenSession)).copy(keyboardShown = true), NoHudActions, HudPill(left = false, y = 0.35f)) }
    }

    @Test
    @Config(qualifiers = PORTRAIT_DARK)
    fun hudOwnScreenPortrait() = capture("hud-own-portrait") {
        Desktop { HudSheet(hud(StreamSession.State.Streaming(ownScreenSession)), NoHudActions, Modifier.align(Alignment.TopEnd)) }
    }

    /** Renders [content] in the app's own theme, light or dark as the test's qualifiers say. */
    private fun capture(name: String, content: @Composable () -> Unit) {
        val dir = System.getProperty("desklink.screenshots") ?: error("run through make screenshots")
        captureRoboImage(File(dir, "$name.png").path) {
            CompositionLocalProvider(LocalInspectionMode provides true) {
                DeskLinkTheme(content = content)
            }
        }
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
            HostItem(home, HostHealth.Offline("no answer on 192.168.1.30, 10.8.0.6"), HostSettings(preset = StreamPreset.CRISP, screen = ScreenMode.MIRROR)),
        ),
    )

    private val settings = SettingsUiState(keyCapture = true, appLock = false, screenLock = true)

    private val officeSettings = HostSettings(exactText = true, lockAfter = true, keepAwake = KeepAwake.FOUR_HOURS, primary = false)

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
            exactTiles = ExactTileStats(412, 0),
            history = history,
            soc = "SM8550",
            mediaClass = 34,
        )
    }

    private val mirrorSession = ownScreenSession.copy(
        display = "mirroring DP-1", screen = ScreenMode.MIRROR, scale = 0.0, primary = null, deskOff = null, mic = null,
        keepAwake = KeepAwake.UNTIL_RETURN, lockAfter = false, mirroring = "DP-1",
    )

    private fun hud(
        state: StreamSession.State,
        trackpad: Boolean = false,
        statsShown: Boolean = true,
        quality: LinkQuality = LinkQuality.EXCELLENT,
    ): HudUiState {
        val link = when (quality) {
            LinkQuality.FAIR -> stats.stats.copy(rttMs = 96, fps = 60)
            else -> stats.stats
        }
        val settings = HostSettings(touch = if (trackpad) TouchMode.TRACKPAD else TouchMode.DIRECT)
        return hudUiState("cm", state, link, keyboardShown = false, statsShown = statsShown, settings = settings, av1Available = true)
    }

    private object NoHostsActions : HostsActions {
        override fun connect(host: HostProfile) = Unit
        override fun wake(host: HostProfile) = Unit
        override fun pair(host: HostProfile) = Unit
        override fun pairNearby(host: NearbyHost) = Unit
        override fun scanQr() = Unit
        override fun addHost() = Unit
        override fun addComputer() = Unit
        override fun select(host: HostProfile) = Unit
        override fun setExactText(host: HostProfile, on: Boolean) = Unit
        override fun setDeskOff(host: HostProfile, off: Boolean) = Unit
        override fun check(host: HostProfile) = Unit
        override fun test(host: HostProfile) = Unit
        override fun forget(host: HostProfile) = Unit
        override fun openSettings() = Unit
        override fun openHostSettings(host: HostProfile) = Unit
        override fun setPreset(host: HostProfile, preset: StreamPreset) = Unit
        override fun setScreenMode(host: HostProfile, mode: String) = Unit
        override fun setPlayAudio(host: HostProfile, play: Boolean) = Unit
    }

    private object NoSettingsActions : SettingsActions {
        override fun openKeyCaptureSettings() = Unit
        override fun setAppLock(on: Boolean) = Unit
    }

    private object NoHostSettingsActions : HostSettingsActions {
        override fun setPreset(preset: StreamPreset) = Unit
        override fun setCodec(codec: String) = Unit
        override fun setSharperColours(on: Boolean) = Unit
        override fun setExactText(on: Boolean) = Unit
        override fun setScreenMode(mode: String) = Unit
        override fun setSound(on: Boolean) = Unit
        override fun changeScale(direction: Int) = Unit
        override fun setPrimary(on: Boolean) = Unit
        override fun setDeskOff(off: Boolean) = Unit
        override fun setMic(on: Boolean) = Unit
        override fun setKeepAwake(minutes: Int) = Unit
        override fun setLockAfter(on: Boolean) = Unit
        override fun setTouchMode(mode: TouchMode) = Unit
        override fun setTrackpadSpeed(speed: Float) = Unit
        override fun testConnection() = Unit
        override fun forget() = Unit
        override fun setScrollSpeed(speed: Float) = Unit
    }

    companion object {
        const val VERSION = "0.53.0 (53000)"

        // The Galaxy Tab S9: 2560×1600 pixels at 340 dpi.
        const val LANDSCAPE_DARK = "w1205dp-h753dp-land-night-340dpi"
        const val LANDSCAPE_LIGHT = "w1205dp-h753dp-land-notnight-340dpi"
        const val PORTRAIT_DARK = "w753dp-h1205dp-port-night-340dpi"
        const val PORTRAIT_LIGHT = "w753dp-h1205dp-port-notnight-340dpi"
    }
}

/** A stand-in for the streamed desktop under the overlay. */
@Composable
internal fun Desktop(content: @Composable BoxScope.() -> Unit) {
    // A light GNOME wallpaper, as the design's mockups show the session's tools over.
    HudTheme {
        Box(
            Modifier.fillMaxSize().background(Brush.linearGradient(listOf(Color(0xFF8FB8EA), Color(0xFF5C8FD6), Color(0xFF3F74C6)))),
            content = content,
        )
    }
}

/** An own-screen session beside an unlocked desk, which offers Primary screen, Desk screens off and the microphone. */
internal val ownScreenSession = SessionInfo(
    sessionId = 1, udpPort = 9444, codec = "av1", width = 2560, height = 1600, fps = 120, qp = 12,
    display = "own 2560x1600 screen at 200%", screen = ScreenMode.OWN, scale = 2.0,
    keepAwake = KeepAwake.FOUR_HOURS, lockAfter = true, primary = true, deskOff = false, mic = true,
    monitors = listOf(MonitorInfo("DP-1", 2560, 1440, 60.0, primary = true), MonitorInfo("HDMI-A-1", 1920, 1080, 60.0)),
)

private object NoConnectingActions : ConnectingActions {
    override fun cancel() = Unit
    override fun retry() = Unit
}

internal object NoHudActions : HudActions {
    override fun close() = Unit
    override fun openSettings() = Unit
    override fun toggleKeyboard() = Unit
    override fun toggleTrackpad() = Unit
    override fun apps() = Unit
    override fun overview() = Unit
    override fun sendClipboard() = Unit
    override fun getClipboard() = Unit
    override fun toggleStats() = Unit
    override fun printScreen() = Unit
    override fun setScreen(mode: String) = Unit
    override fun setPreset(preset: StreamPreset) = Unit
    override fun setCodec(codec: String) = Unit
    override fun setSharperColours(on: Boolean) = Unit
    override fun setExactText(on: Boolean) = Unit
    override fun setSound(on: Boolean) = Unit
    override fun setTrackpadSpeed(speed: Float) = Unit
    override fun setScrollSpeed(speed: Float) = Unit
    override fun chooseMonitor(connector: String) = Unit
    override fun changeScale(direction: Int) = Unit
    override fun setPrimary(on: Boolean) = Unit
    override fun setDeskOff(off: Boolean) = Unit
    override fun setMic(on: Boolean) = Unit
    override fun setKeepAwake(minutes: Int) = Unit
    override fun setLockAfter(on: Boolean) = Unit
    override fun disconnect() = Unit
}
