package com.desklink.client.model

import com.desklink.client.input.TouchMode
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json

/**
 * One computer's settings: what its sessions ask for, and how the tablet
 * draws them and takes touch for them. Every saved host keeps its own
 * ([HostStore.settings]); App lock, the stats overlay and the HUD pill's
 * place belong to the tablet and stay the same for every host.
 */
@Serializable
data class HostSettings(
    val preset: StreamPreset = StreamPreset.AUTO,
    /** "av1" or "hevc"; null takes the best codec the tablet decodes in hardware. */
    val codec: String? = null,
    /** What sessions show: a [ScreenMode] value. */
    val screen: String = ScreenMode.AUTO,
    /**
     * Whether the tablet plays the desktop's sound. Every session receives
     * the sound, so turning it on during a session is heard at once.
     */
    val sound: Boolean = true,
    /** Whether the GPU renderer rebuilds coloured edges ("Sharp Colour"). */
    @SerialName("sharper_colours") val sharperColours: Boolean = true,
    /**
     * Whether sessions ask for exact tiles of the parts of the screen that
     * stopped changing ("Exact Text"); they need [sharperColours].
     * Off until the tiles are confirmed not to flicker on the device.
     */
    @SerialName("exact_text") val exactText: Boolean = false,
    /** How touches on the video act. */
    val touch: TouchMode = TouchMode.DIRECT,
    /** The touchscreen trackpad's speed. */
    @SerialName("trackpad_speed") val trackpadSpeed: Float = DEFAULT_TRACKPAD_SPEED,
    /** What finger scrolls are multiplied by, on the touchscreen and a captured touchpad. */
    @SerialName("scroll_speed") val scrollSpeed: Float = DEFAULT_SCROLL_SPEED,
    /** The scale asked for the own screen. */
    val scale: Double = OwnScale.DEFAULT,
    /** How long the computer stays awake after a session: a [KeepAwake] value. */
    @SerialName("keep_awake") val keepAwake: Int = KeepAwake.OFF,
    /** Whether the desk locks when a session ends, also when it was unlocked as the tablet came. */
    @SerialName("lock_after") val lockAfter: Boolean = false,
    /** Whether sessions stream the tablet's microphone to the desk. */
    val mic: Boolean = false,
    /** Whether the own screen takes GNOME's top bar and overview; false leaves them on the desk. */
    val primary: Boolean = true,
    /** Whether an own-screen session turns the desk's monitors off as it starts. */
    @SerialName("desk_off") val deskOff: Boolean = false,
) {
    companion object {
        /** The trackpad speed before the user changes it. */
        const val DEFAULT_TRACKPAD_SPEED = 1.5f

        /**
         * The scroll speed before the user changes it. At 1 the desk scrolls
         * by the fingers' distance as a touchpad would, which apps may
         * enlarge, and scrolling felt too fast on the tablet.
         */
        const val DEFAULT_SCROLL_SPEED = 0.5f

        /**
         * How settings are stored. A value this version does not know, such
         * as a preset added later and then rolled back, falls back to that
         * setting's default instead of losing the others.
         */
        val Storage = Json {
            ignoreUnknownKeys = true
            coerceInputValues = true
            encodeDefaults = true
        }
    }
}
