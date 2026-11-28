package com.desklink.client.ui

import com.desklink.client.input.TouchMode
import com.desklink.client.model.HostSettings
import com.desklink.client.model.KeepAwake
import com.desklink.client.model.MonitorInfo
import com.desklink.client.model.OwnScale
import com.desklink.client.model.ScreenMode
import com.desklink.client.model.StreamPreset
import com.desklink.client.net.LinkQuality
import com.desklink.client.net.LinkStats
import com.desklink.client.net.StreamSession

/** The HUD's status line: connecting, reconnecting, or the link's grade, round trip and frame rate. */
sealed interface HudLink {
    /** The session has not streamed yet. */
    data object Connecting : HudLink

    /** The connection dropped; [attempt] of [of] runs. */
    data class Reconnecting(val attempt: Int, val of: Int) : HudLink

    /** The session streams. */
    data class Live(val quality: LinkQuality, val rttMs: Int, val fps: Int) : HudLink
}

/**
 * The HUD's Screen section: own screen or mirror ([mode], a [ScreenMode]
 * value); the [monitors] to choose from, only while mirroring more than one,
 * with the one [mirroring]; the own screen's [scale] (null when mirroring);
 * and the Primary screen and Desk screens off switches, null when the
 * session does not offer them.
 */
data class HudScreen(
    val mode: String,
    val monitors: List<MonitorInfo> = emptyList(),
    val mirroring: String = "",
    val scale: Double? = null,
    val primary: Boolean? = null,
    val deskOff: Boolean? = null,
) {
    /** Whether − can make the scale smaller. */
    val canShrink: Boolean get() = scale != null && scale > OwnScale.MIN + 0.01

    /** Whether + can make the scale larger. */
    val canGrow: Boolean get() = scale != null && scale < OwnScale.MAX - 0.01

    /** Whether every app is sharp at the scale (100 or 200 %), which the HUD marks with ✓. */
    val sharp: Boolean get() = scale != null && OwnScale.isSharp(scale)
}

/**
 * The HUD's Session section: the Sound switch; the Microphone switch, null
 * when the host offers no microphone; how long the computer stays awake
 * after the session (a [KeepAwake] value); and whether the desk locks then.
 */
data class HudSession(val mic: Boolean?, val keepAwake: Int = KeepAwake.OFF, val lockAfter: Boolean = false, val sound: Boolean = true)

/**
 * The HUD's Picture section: the quality [preset], the [codec] the stream
 * runs in (AV1 can be chosen only when [av1Available]), Sharper colours and
 * Exact text.
 */
data class HudPicture(
    val preset: StreamPreset,
    val codec: String,
    val av1Available: Boolean,
    val sharperColours: Boolean,
    val exactText: Boolean,
)

/**
 * What the HUD sheet shows: the host, the status line, which actions are on
 * (the soft keyboard shown, the touchscreen as a trackpad, the stats
 * overlay), and the Screen, Picture and Session sections, null while they
 * have nothing to show. [trackpadSpeed] shows in trackpad mode, [scrollSpeed]
 * in both touch modes.
 */
data class HudUiState(
    val hostName: String,
    val link: HudLink,
    val keyboardShown: Boolean = false,
    val trackpad: Boolean = false,
    val statsShown: Boolean = false,
    val screen: HudScreen? = null,
    val session: HudSession? = null,
    val picture: HudPicture? = null,
    val trackpadSpeed: Float = HostSettings.DEFAULT_TRACKPAD_SPEED,
    val scrollSpeed: Float = HostSettings.DEFAULT_SCROLL_SPEED,
)

/**
 * The HUD for a session in [state] with the link's [stats], for the host
 * with [settings]. The sections show only once it streams, since they come
 * from the host's session description: Screen when the host reports what it
 * shows, and in it the monitors only while mirroring more than one and the
 * scale only on the own screen. The Microphone switch is on while the user
 * wants it and the host plays it. The codec is the one the stream runs in.
 */
fun hudUiState(
    hostName: String,
    state: StreamSession.State,
    stats: LinkStats,
    keyboardShown: Boolean,
    statsShown: Boolean,
    settings: HostSettings,
    av1Available: Boolean,
): HudUiState {
    val info = (state as? StreamSession.State.Streaming)?.info
    val link = when (state) {
        is StreamSession.State.Streaming -> HudLink.Live(stats.quality, stats.rttMs, stats.fps)
        is StreamSession.State.Reconnecting -> HudLink.Reconnecting(state.attempt, state.of)
        else -> HudLink.Connecting
    }
    val screen = info?.takeIf { it.screen.isNotEmpty() }?.let {
        val mirror = it.screen == ScreenMode.MIRROR
        HudScreen(
            mode = it.screen,
            monitors = if (mirror && it.monitors.size > 1) it.monitors else emptyList(),
            mirroring = it.mirroring,
            scale = it.scale.takeIf { s -> it.screen == ScreenMode.OWN && s > 0 },
            primary = it.primary,
            deskOff = it.deskOff,
        )
    }
    val session = info?.let {
        HudSession(mic = it.mic?.let { hostOn -> hostOn && settings.mic }, keepAwake = it.keepAwake, lockAfter = it.lockAfter, sound = settings.sound)
    }
    val picture = info?.let { HudPicture(settings.preset, it.codec, av1Available, settings.sharperColours, settings.exactText) }
    return HudUiState(
        hostName, link, keyboardShown, settings.touch == TouchMode.TRACKPAD, statsShown, screen, session, picture, settings.trackpadSpeed,
        settings.scrollSpeed,
    )
}
