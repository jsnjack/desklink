package com.desklink.client.model

import android.content.Context
import android.util.Log
import androidx.annotation.StringRes
import androidx.core.content.edit
import com.desklink.client.R
import com.desklink.client.crypto.KeystoreVault
import com.desklink.client.input.TouchMode
import com.desklink.client.net.PairResult
import kotlinx.serialization.Serializable
import kotlinx.serialization.builtins.ListSerializer
import java.util.UUID

/**
 * A saved host. [wrappedKey] is the peer key encrypted by the Android
 * Keystore; [fingerprint] pins the host certificate; [mac] is for Wake-on-LAN.
 */
@Serializable
data class HostProfile(
    val id: String,
    val name: String,
    val ip: String,
    val port: Int = DEFAULT_PORT,
    val peerId: String? = null,
    val fingerprint: String? = null,
    val wrappedKey: String? = null,
    val mac: String? = null,
    /** The host's other addresses, on other networks or VPNs; [ip] answered last. */
    val addresses: List<String> = emptyList(),
) {
    /** Whether the profile holds pairing credentials. */
    val paired: Boolean get() = peerId != null && fingerprint != null && wrappedKey != null

    /**
     * The addresses to try, at most [MAX_ADDRESSES]: the last one that
     * answered first, then the others, leaving out any that cannot be a
     * host's address.
     */
    fun candidates(): List<String> = (listOf(ip) + addresses).filter(::usableAddress).distinct().take(MAX_ADDRESSES)

    /**
     * This host reached at [reached], keeping its other addresses newest
     * first: the address that answered before, then [reported] (what the host
     * or the pairing code lists now), then the older ones. Addresses that
     * cannot be a host's are dropped, and so are the oldest beyond
     * [MAX_ADDRESSES] in all, so reconnects never probe a long list.
     */
    fun withAddresses(reached: String, reported: List<String>): HostProfile = copy(
        ip = reached,
        addresses = (listOf(ip) + reported + addresses).map(String::trim)
            .filter { usableAddress(it) && it != reached }.distinct().take(MAX_ADDRESSES - 1),
    )

    companion object {
        /** The FRITZ!Box address the app suggests for a new host. */
        const val DEFAULT_IP = "192.168.178.11"

        /** The port the app suggests for a new host. */
        const val DEFAULT_PORT = 9443

        /** The most addresses a host keeps, the main one included. */
        const val MAX_ADDRESSES = 8
    }
}

/**
 * Stream presets: size, quantizer (lower is sharper) and frame rate. [auto]
 * lets the host move down and up a ladder from that top size and rate.
 */
enum class StreamPreset(
    val width: Int,
    val height: Int,
    val qp: Int,
    val fps: Int,
    @param:StringRes val label: Int,
    val auto: Boolean = false,
) {
    AUTO(2560, 1600, 18, 120, R.string.preset_auto, auto = true),
    CRISP(2560, 1600, 18, 120, R.string.preset_crisp),
    BALANCED(1920, 1200, 20, 60, R.string.preset_balanced),
    OFFICE_SAVER(1920, 1200, 24, 60, R.string.preset_office),
}

/**
 * Folds profiles of the same machine (the same certificate fingerprint) into
 * one, so a machine paired on several networks is one host. The oldest entry
 * survives with its id, name and so its remembered quality levels, and gains
 * the others' addresses. When [fresh] names the entry just paired, the
 * survivor takes that entry's pairing, port, MAC and address.
 */
fun mergeSameMachine(list: List<HostProfile>, fresh: String? = null): List<HostProfile> {
    val groups = list.filter { it.fingerprint != null }.groupBy { it.fingerprint!!.lowercase() }
    val drop = mutableSetOf<String>()
    val merged = mutableMapOf<String, HostProfile>()
    for (group in groups.values.filter { it.size > 1 }) {
        val kept = group.first()
        val paired = group.firstOrNull { it.id == fresh }
        var result = if (paired == null || paired.id == kept.id) {
            kept
        } else {
            kept.copy(
                port = paired.port,
                peerId = paired.peerId,
                fingerprint = paired.fingerprint,
                wrappedKey = paired.wrappedKey,
                mac = paired.mac ?: kept.mac,
            ).withAddresses(paired.ip, emptyList())
        }
        for (other in group) {
            if (other.id == kept.id) continue
            result = result.withAddresses(result.ip, other.candidates())
            drop += other.id
        }
        merged[kept.id] = result
    }
    return list.filterNot { it.id in drop }.map { merged[it.id] ?: it }
}

/** Host profiles and stream settings in private SharedPreferences. */
class HostStore(context: Context) {
    private val prefs = context.getSharedPreferences("desklink", Context.MODE_PRIVATE)
    private val listSerializer = ListSerializer(HostProfile.serializer())

    /**
     * The saved hosts, or one unpaired default host on first start. Data that
     * cannot be read is kept under a backup key rather than overwritten by the
     * next save, so a format problem does not silently lose pairings.
     */
    fun profiles(): List<HostProfile> {
        val raw = prefs.getString(KEY_PROFILES, null)
            ?: return listOf(HostProfile(UUID.randomUUID().toString(), "Desktop", HostProfile.DEFAULT_IP))
        return try {
            ProtocolJson.decodeFromString(listSerializer, raw)
        } catch (e: kotlinx.serialization.SerializationException) {
            Log.e(TAG, "saved hosts are unreadable; kept as $KEY_UNREADABLE", e)
            prefs.edit { putString(KEY_UNREADABLE, raw) }
            emptyList()
        } catch (e: IllegalArgumentException) {
            Log.e(TAG, "saved hosts are unreadable; kept as $KEY_UNREADABLE", e)
            prefs.edit { putString(KEY_UNREADABLE, raw) }
            emptyList()
        }
    }

    /** Saves the host list. */
    fun save(list: List<HostProfile>) {
        prefs.edit { putString(KEY_PROFILES, ProtocolJson.encodeToString(listSerializer, list)) }
    }

    /** Unwraps a host's peer key, or null if it is missing or the Keystore key is gone. */
    fun peerKey(p: HostProfile): ByteArray? =
        p.wrappedKey?.let { runCatching { KeystoreVault.unwrap(B64.decode(it)) }.getOrNull() }

    /** The profile with a new pairing's credentials, the key wrapped by the Keystore. */
    fun withPairing(p: HostProfile, result: PairResult, mac: String?): HostProfile = p.copy(
        peerId = result.peerId,
        fingerprint = result.fingerprint,
        wrappedKey = B64.encode(KeystoreVault.wrap(result.peerKey)),
        mac = mac?.takeIf { it.isNotBlank() } ?: p.mac,
    )

    /** The selected stream preset. */
    var preset: StreamPreset
        get() = runCatching { StreamPreset.valueOf(prefs.getString(KEY_PRESET, null) ?: "") }.getOrDefault(StreamPreset.AUTO)
        set(v) = prefs.edit { putString(KEY_PRESET, v.name) }

    /**
     * The auto-quality level a host last used on a network, by size and rate,
     * which stays meaningful if the host's ladder changes; null if none.
     */
    fun level(profileId: String, network: String): LevelInfo? =
        prefs.getString(levelKey(profileId, network), null)?.let {
            runCatching { ProtocolJson.decodeFromString(LevelInfo.serializer(), it) }.getOrNull()
        }

    /** Remembers the auto-quality level a host used on a network. */
    fun setLevel(profileId: String, network: String, level: LevelInfo) {
        prefs.edit { putString(levelKey(profileId, network), ProtocolJson.encodeToString(LevelInfo.serializer(), level)) }
    }

    private fun levelKey(profileId: String, network: String) = "level2:$profileId:$network"

    /** The selected codec, or null before the first choice. */
    var codec: String?
        get() = prefs.getString(KEY_CODEC, null)
        set(v) = prefs.edit { putString(KEY_CODEC, v) }

    /** The touchscreen trackpad speed. */
    var sensitivity: Float
        get() = prefs.getFloat(KEY_SENSITIVITY, DEFAULT_SENSITIVITY)
        set(v) = prefs.edit { putFloat(KEY_SENSITIVITY, v) }

    /** How touches on the video act: tap to click (default) or trackpad. */
    var touchMode: TouchMode
        get() = runCatching { TouchMode.valueOf(prefs.getString(KEY_TOUCH_MODE, null) ?: "") }.getOrDefault(TouchMode.DIRECT)
        set(v) = prefs.edit { putString(KEY_TOUCH_MODE, v.name) }

    /** The scale asked for the own screen, changed with the HUD's - and +. */
    var ownScale: Double
        get() = prefs.getFloat(KEY_OWN_SCALE, OwnScale.DEFAULT.toFloat()).toDouble()
        set(v) = prefs.edit { putFloat(KEY_OWN_SCALE, v.toFloat()) }

    /** How long the computer stays awake after a session: a [KeepAwake] value. */
    var keepAwake: Int
        get() = prefs.getInt(KEY_KEEP_AWAKE, KeepAwake.OFF)
        set(v) = prefs.edit { putInt(KEY_KEEP_AWAKE, v) }

    /** What sessions show: a [ScreenMode] value. */
    var screenMode: String
        get() = prefs.getString(KEY_SCREEN, null) ?: ScreenMode.AUTO
        set(v) = prefs.edit { putString(KEY_SCREEN, v) }

    /** Whether the tablet plays the desktop's sound. */
    var playAudio: Boolean
        get() = prefs.getBoolean(KEY_PLAY_AUDIO, true)
        set(v) = prefs.edit { putBoolean(KEY_PLAY_AUDIO, v) }

    /** Which side edge the HUD pill sticks to. */
    var hudPillLeft: Boolean
        get() = prefs.getBoolean(KEY_PILL_LEFT, false)
        set(v) = prefs.edit { putBoolean(KEY_PILL_LEFT, v) }

    /** The HUD pill's height along its edge, 0 at the top to 1 at the bottom. */
    var hudPillY: Float
        get() = prefs.getFloat(KEY_PILL_Y, DEFAULT_PILL_Y)
        set(v) = prefs.edit { putFloat(KEY_PILL_Y, v) }

    /** Whether the stream shows the link statistics overlay. */
    var showStats: Boolean
        get() = prefs.getBoolean(KEY_SHOW_STATS, true)
        set(v) = prefs.edit { putBoolean(KEY_SHOW_STATS, v) }

    /** Whether the GPU renderer rebuilds coloured edges ("Sharper colours"); on by default. */
    var sharperColours: Boolean
        get() = prefs.getBoolean(KEY_SHARPER_COLOURS, true)
        set(v) = prefs.edit { putBoolean(KEY_SHARPER_COLOURS, v) }

    /** Whether Connect first asks for a fingerprint or the device's PIN ([AppLock]); off by default. */
    var appLock: Boolean
        get() = prefs.getBoolean(KEY_APP_LOCK, false)
        set(v) = prefs.edit { putBoolean(KEY_APP_LOCK, v) }

    /** Whether the desk locks when a session ends, also when it was unlocked as the tablet came. */
    var lockAfter: Boolean
        get() = prefs.getBoolean(KEY_LOCK_AFTER, false)
        set(v) = prefs.edit { putBoolean(KEY_LOCK_AFTER, v) }

    companion object {
        private const val KEY_PROFILES = "profiles"
        private const val KEY_PRESET = "preset"
        private const val KEY_CODEC = "codec"
        private const val KEY_SENSITIVITY = "sensitivity"
        private const val KEY_TOUCH_MODE = "touch_mode"
        private const val KEY_SHOW_STATS = "show_stats"
        private const val KEY_PLAY_AUDIO = "play_audio"
        private const val KEY_SHARPER_COLOURS = "sharper_colours"
        private const val KEY_APP_LOCK = "app_lock"
        private const val KEY_LOCK_AFTER = "lock_after"
        private const val KEY_SCREEN = "screen"
        private const val KEY_OWN_SCALE = "own_scale"
        private const val KEY_KEEP_AWAKE = "keep_awake"
        private const val KEY_PILL_LEFT = "hud_pill_left"
        private const val KEY_PILL_Y = "hud_pill_y"
        private const val DEFAULT_PILL_Y = 0.06f
        private const val KEY_UNREADABLE = "profiles_unreadable"
        private const val TAG = "DeskLink"

        /** The trackpad speed before the user changes it. */
        const val DEFAULT_SENSITIVITY = 1.5f
    }
}
