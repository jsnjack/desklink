package com.desklink.client.ui

import android.app.Application
import android.content.ClipData
import android.content.ClipboardManager
import android.os.Build
import android.provider.Settings
import android.util.Log
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.viewModelScope
import com.desklink.client.R
import com.desklink.client.input.InputMapper
import com.desklink.client.model.AppEntry
import com.desklink.client.model.HostProfile
import com.desklink.client.model.HostStore
import com.desklink.client.model.PairingQr
import com.desklink.client.model.ProtocolJson
import com.desklink.client.model.StreamPreset
import com.desklink.client.net.ControlClient
import com.desklink.client.net.StreamSession
import com.desklink.client.video.HardwareDecoder
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.InetAddress
import java.util.UUID

/** Reachability of a saved host, from GET /health. */
sealed interface HostHealth {
    data object Checking : HostHealth
    data class Online(val hostname: String, val version: String) : HostHealth
    data class Offline(val reason: String) : HostHealth
}

/** App state that outlives configuration changes: profiles, settings and the session. */
class AppViewModel(app: Application) : AndroidViewModel(app) {
    private val store = HostStore(app)

    private val _profiles = MutableStateFlow(store.profiles())
    val profiles: StateFlow<List<HostProfile>> = _profiles

    private val _health = MutableStateFlow<Map<String, HostHealth>>(emptyMap())
    val health: StateFlow<Map<String, HostHealth>> = _health

    val av1Available: Boolean = HardwareDecoder.bestCodec() == HardwareDecoder.CODEC_AV1

    private val _preset = MutableStateFlow(store.preset)
    val preset: StateFlow<StreamPreset> = _preset

    private val _codec = MutableStateFlow(store.codec?.takeIf { it != HardwareDecoder.CODEC_AV1 || av1Available } ?: HardwareDecoder.bestCodec())
    val codec: StateFlow<String> = _codec

    private val _sensitivity = MutableStateFlow(store.sensitivity)
    val sensitivity: StateFlow<Float> = _sensitivity

    private val _session = MutableStateFlow<StreamSession?>(null)
    val session: StateFlow<StreamSession?> = _session

    private val _message = MutableStateFlow<String?>(null)
    val message: StateFlow<String?> = _message

    private val _hudOpen = MutableStateFlow(false)
    val hudOpen: StateFlow<Boolean> = _hudOpen

    private val _keyboardShown = MutableStateFlow(false)
    val keyboardShown: StateFlow<Boolean> = _keyboardShown

    private val _latched = MutableStateFlow<Set<Int>>(emptySet())
    val latched: StateFlow<Set<Int>> = _latched

    private val _pairing = MutableStateFlow(false)
    val pairing: StateFlow<Boolean> = _pairing

    val mapper = InputMapper(
        send = { events ->
            _session.value?.sendInput(events)
            _latched.value = latchState()
        },
        onHudToggle = { _hudOpen.update { !it } },
    ).also { it.touchSensitivity = store.sensitivity }

    private var sessionJobs = emptyList<Job>()
    private var lastClipboard: String? = null

    private fun latchState(): Set<Int> = mapper.latch.active

    private fun str(id: Int, vararg args: Any): String = getApplication<Application>().getString(id, *args)

    fun showMessage(text: String?) {
        _message.value = text
    }

    fun setPreset(p: StreamPreset) {
        store.preset = p
        _preset.value = p
    }

    fun setCodec(c: String) {
        store.codec = c
        _codec.value = c
    }

    fun setSensitivity(v: Float) {
        store.sensitivity = v
        mapper.touchSensitivity = v
        _sensitivity.value = v
    }

    fun addHost(name: String, ip: String, port: Int) {
        val p = HostProfile(UUID.randomUUID().toString(), name.ifBlank { ip }, ip.trim(), port)
        saveProfiles(_profiles.value + p)
        checkHealth(p)
    }

    fun deleteHost(p: HostProfile) = saveProfiles(_profiles.value.filterNot { it.id == p.id })

    private fun saveProfiles(list: List<HostProfile>) {
        store.save(list)
        _profiles.value = list
    }

    private fun replace(p: HostProfile) = saveProfiles(_profiles.value.map { if (it.id == p.id) p else it })

    fun checkAll() = _profiles.value.forEach(::checkHealth)

    fun checkHealth(p: HostProfile) {
        _health.update { it + (p.id to HostHealth.Checking) }
        viewModelScope.launch {
            val result = runCatching { ControlClient(p.ip, p.port, p.fingerprint).health() }
            _health.update {
                it + (p.id to result.fold(
                    { h -> HostHealth.Online(h.hostname, h.version) },
                    { e -> HostHealth.Offline(e.message ?: e.toString()) },
                ))
            }
            val mac = result.getOrNull()?.mac
            if (!mac.isNullOrBlank() && mac != p.mac) {
                _profiles.value.firstOrNull { it.id == p.id }?.let { replace(it.copy(mac = mac)) }
            }
        }
    }

    /** Pairs with a scanned QR code, adding the host if it is new. */
    fun pairFromQr(raw: String) {
        val qr = runCatching { ProtocolJson.decodeFromString(PairingQr.serializer(), raw) }.getOrNull()
            ?: return showMessage(str(R.string.msg_bad_qr))
        val existing = _profiles.value.firstOrNull { it.ip == qr.ip && it.port == qr.port }
        val profile = existing ?: HostProfile(UUID.randomUUID().toString(), qr.ip, qr.ip, qr.port).also {
            saveProfiles(_profiles.value + it)
        }
        pair(profile, qr.pin, qr.fingerprint)
    }

    /**
     * Pairs with a PIN. Without a fingerprint from the QR code the first
     * certificate seen is trusted; its fingerprint is shown for comparison.
     */
    fun pair(p: HostProfile, pin: String, fingerprint: String?) {
        _pairing.value = true
        viewModelScope.launch {
            try {
                val client = ControlClient(p.ip, p.port, fingerprint)
                val mac = runCatching { client.health().mac }.getOrNull()
                val result = client.pair(pin.trim(), deviceName())
                val current = _profiles.value.firstOrNull { it.id == p.id } ?: p
                replace(store.withPairing(current, result, mac))
                showMessage(str(R.string.msg_paired, result.fingerprint.take(16)))
                checkHealth(current)
            } catch (e: Exception) {
                Log.w(TAG, "pairing failed", e)
                showMessage(str(R.string.msg_pair_failed, e.message ?: e.toString()))
            } finally {
                _pairing.value = false
            }
        }
    }

    private fun deviceName(): String =
        Settings.Global.getString(getApplication<Application>().contentResolver, Settings.Global.DEVICE_NAME) ?: Build.MODEL

    fun connect(p: HostProfile) {
        val key = store.peerKey(p) ?: return showMessage(str(R.string.msg_not_paired))
        disconnect()
        val s = StreamSession(p, key, _preset.value, _codec.value, viewModelScope)
        _session.value = s
        _hudOpen.value = false
        sessionJobs = listOf(
            viewModelScope.launch {
                s.hostClipboard.collect { text ->
                    lastClipboard = text
                    clipboard().setPrimaryClip(ClipData.newPlainText("DeskLink", text))
                }
            },
            viewModelScope.launch { s.notices.collect { showMessage(it) } },
        )
        s.connect()
    }

    fun disconnect() {
        sessionJobs.forEach { it.cancel() }
        _session.value?.close()
        _session.value = null
        _keyboardShown.value = false
    }

    /** Leaves a failed or closed session and shows why. */
    fun dismissSession(reason: String?) {
        disconnect()
        reason?.let(::showMessage)
    }

    fun setHudOpen(open: Boolean) {
        _hudOpen.value = open
    }

    fun setKeyboardShown(shown: Boolean) {
        _keyboardShown.value = shown
    }

    fun toggleModifier(code: Int) {
        mapper.latch.toggle(code)
        _latched.value = latchState()
    }

    fun tapKey(code: Int) = mapper.tapKey(code)

    fun launch(app: AppEntry) {
        _session.value?.launch(app)
        _hudOpen.value = false
    }

    private fun clipboard(): ClipboardManager = getApplication<Application>().getSystemService(ClipboardManager::class.java)

    /** Sends the Android clipboard to the host. With [onlyIfChanged], skips text the host already has. */
    fun pushClipboard(onlyIfChanged: Boolean) {
        val s = _session.value ?: return
        val text = clipboard().primaryClip?.takeIf { it.itemCount > 0 }
            ?.getItemAt(0)?.coerceToText(getApplication())?.toString() ?: return
        if (onlyIfChanged && text == lastClipboard) return
        lastClipboard = text
        s.pushClipboard(text)
    }

    fun pullClipboard() = _session.value?.requestClipboard()

    /** Sends a Wake-on-LAN packet to the host's subnet broadcast and to its address. */
    fun wake(p: HostProfile) {
        val mac = p.mac ?: return showMessage(str(R.string.msg_no_mac))
        viewModelScope.launch {
            val result = withContext(Dispatchers.IO) { runCatching { sendMagicPacket(mac, p.ip) } }
            showMessage(result.fold({ str(R.string.msg_wake_sent) }, { str(R.string.msg_wake_failed, it.message ?: "") }))
        }
    }

    private fun sendMagicPacket(mac: String, ip: String) {
        val macBytes = mac.split(':', '-').map { it.toInt(16).toByte() }.toByteArray()
        require(macBytes.size == 6) { "bad MAC address $mac" }
        val packet = ByteArray(6) { 0xFF.toByte() } + List(16) { macBytes }.reduce { a, b -> a + b }
        val broadcast = ip.substringBeforeLast('.') + ".255"
        DatagramSocket().use { sock ->
            sock.broadcast = true
            for (target in listOf(broadcast, ip)) {
                sock.send(DatagramPacket(packet, packet.size, InetAddress.getByName(target), WOL_PORT))
            }
        }
    }

    override fun onCleared() {
        disconnect()
    }

    companion object {
        private const val TAG = "DeskLink"
        private const val WOL_PORT = 9
    }
}
