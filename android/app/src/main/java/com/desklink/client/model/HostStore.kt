package com.desklink.client.model

import android.content.Context
import androidx.annotation.StringRes
import androidx.core.content.edit
import com.desklink.client.R
import com.desklink.client.crypto.KeystoreVault
import com.desklink.client.net.PairResult
import kotlinx.serialization.Serializable
import kotlinx.serialization.builtins.ListSerializer
import java.util.UUID

/** A saved host. The peer key is stored wrapped by the Android Keystore. */
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
) {
    val paired: Boolean get() = peerId != null && fingerprint != null && wrappedKey != null

    companion object {
        const val DEFAULT_IP = "192.168.178.11"
        const val DEFAULT_PORT = 9443
    }
}

/** Stream presets: size, quantizer (lower is sharper) and frame rate. */
enum class StreamPreset(val width: Int, val height: Int, val qp: Int, val fps: Int, @param:StringRes val label: Int) {
    CRISP(2560, 1600, 18, 120, R.string.preset_crisp),
    BALANCED(1920, 1200, 20, 60, R.string.preset_balanced),
    OFFICE_SAVER(1920, 1200, 24, 60, R.string.preset_office),
}

/** Host profiles and stream settings in private SharedPreferences. */
class HostStore(context: Context) {
    private val prefs = context.getSharedPreferences("desklink", Context.MODE_PRIVATE)
    private val listSerializer = ListSerializer(HostProfile.serializer())

    fun profiles(): List<HostProfile> {
        val raw = prefs.getString(KEY_PROFILES, null)
            ?: return listOf(HostProfile(UUID.randomUUID().toString(), "Desktop", HostProfile.DEFAULT_IP))
        return runCatching { ProtocolJson.decodeFromString(listSerializer, raw) }.getOrDefault(emptyList())
    }

    fun save(list: List<HostProfile>) {
        prefs.edit { putString(KEY_PROFILES, ProtocolJson.encodeToString(listSerializer, list)) }
    }

    fun peerKey(p: HostProfile): ByteArray? =
        p.wrappedKey?.let { runCatching { KeystoreVault.unwrap(B64.decode(it)) }.getOrNull() }

    fun withPairing(p: HostProfile, result: PairResult, mac: String?): HostProfile = p.copy(
        peerId = result.peerId,
        fingerprint = result.fingerprint,
        wrappedKey = B64.encode(KeystoreVault.wrap(result.peerKey)),
        mac = mac?.takeIf { it.isNotBlank() } ?: p.mac,
    )

    var preset: StreamPreset
        get() = runCatching { StreamPreset.valueOf(prefs.getString(KEY_PRESET, null) ?: "") }.getOrDefault(StreamPreset.CRISP)
        set(v) = prefs.edit { putString(KEY_PRESET, v.name) }

    var codec: String?
        get() = prefs.getString(KEY_CODEC, null)
        set(v) = prefs.edit { putString(KEY_CODEC, v) }

    var sensitivity: Float
        get() = prefs.getFloat(KEY_SENSITIVITY, DEFAULT_SENSITIVITY)
        set(v) = prefs.edit { putFloat(KEY_SENSITIVITY, v) }

    companion object {
        private const val KEY_PROFILES = "profiles"
        private const val KEY_PRESET = "preset"
        private const val KEY_CODEC = "codec"
        private const val KEY_SENSITIVITY = "sensitivity"
        const val DEFAULT_SENSITIVITY = 1.5f
    }
}
