package com.desklink.client.ui

import android.app.Application
import android.content.ClipData
import android.content.ClipboardManager
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import android.os.Build
import android.os.SystemClock
import android.provider.Settings
import android.util.Log
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.viewModelScope
import com.desklink.client.R
import com.desklink.client.input.InputMapper
import com.desklink.client.input.TouchMode
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
import kotlinx.coroutines.delay
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
    /** A check is running. */
    data object Checking : HostHealth

    /** Wake-on-LAN packets were sent and the app waits for the host to answer. */
    data object Waking : HostHealth

    /** The host answered. */
    data class Online(val hostname: String, val version: String) : HostHealth

    /** The host did not answer; [reason] is shown to the user. */
    data class Offline(val reason: String) : HostHealth
}

/** App state that outlives configuration changes: profiles, settings and the session. */
class AppViewModel(app: Application) : AndroidViewModel(app) {
    private val store = HostStore(app)

    private val _profiles = MutableStateFlow(store.profiles())
    /** The saved hosts. */
    val profiles: StateFlow<List<HostProfile>> = _profiles

    private val _health = MutableStateFlow<Map<String, HostHealth>>(emptyMap())
    /** The last health check of each host, by profile ID. */
    val health: StateFlow<Map<String, HostHealth>> = _health

    /** Whether this device decodes AV1 in hardware. */
    val av1Available: Boolean = HardwareDecoder.bestCodec() == HardwareDecoder.CODEC_AV1

    private val _preset = MutableStateFlow(store.preset)
    /** The selected stream preset. */
    val preset: StateFlow<StreamPreset> = _preset

    private val _codec = MutableStateFlow(store.codec?.takeIf { it != HardwareDecoder.CODEC_AV1 || av1Available } ?: HardwareDecoder.bestCodec())
    /** The selected codec, "av1" or "hevc". */
    val codec: StateFlow<String> = _codec

    private val _touchMode = MutableStateFlow(store.touchMode)
    /** How touches on the video act. */
    val touchMode: StateFlow<TouchMode> = _touchMode

    private val _showStats = MutableStateFlow(store.showStats)
    /** Whether the stream shows the link statistics overlay. */
    val showStats: StateFlow<Boolean> = _showStats

    private val _sensitivity = MutableStateFlow(store.sensitivity)
    /** The touchscreen trackpad speed. */
    val sensitivity: StateFlow<Float> = _sensitivity

    private val _session = MutableStateFlow<StreamSession?>(null)
    /** The running session, or null on the connect screen. */
    val session: StateFlow<StreamSession?> = _session

    private val _message = MutableStateFlow<String?>(null)
    /** A message to show once, then cleared with [showMessage]. */
    val message: StateFlow<String?> = _message

    private val _hudOpen = MutableStateFlow(false)
    /** Whether the HUD panel is open. */
    val hudOpen: StateFlow<Boolean> = _hudOpen

    private val _keyboardShown = MutableStateFlow(false)
    /** Whether the soft keyboard and modifier bar are shown. */
    val keyboardShown: StateFlow<Boolean> = _keyboardShown

    private val _latched = MutableStateFlow<Set<Int>>(emptySet())
    /** The latched modifier keys. */
    val latched: StateFlow<Set<Int>> = _latched

    private val _pairing = MutableStateFlow(false)
    /** Whether a pairing is in progress. */
    val pairing: StateFlow<Boolean> = _pairing

    /** Turns every Android input source into host input for the running session. */
    val mapper = InputMapper(
        send = { events ->
            _session.value?.sendInput(events)
            _latched.value = latchState()
        },
        onHudToggle = { _hudOpen.update { !it } },
    ).also {
        it.touchSensitivity = store.sensitivity
        it.touchMode = store.touchMode
    }

    private var sessionJobs = emptyList<Job>()
    private var connectJob: Job? = null
    private var lastClipboard: String? = null

    private fun latchState(): Set<Int> = mapper.latch.active

    private fun str(id: Int, vararg args: Any): String = getApplication<Application>().getString(id, *args)

    /** Shows [text] once; null clears it. */
    fun showMessage(text: String?) {
        _message.value = text
    }

    /** Saves the stream preset. */
    fun setPreset(p: StreamPreset) {
        store.preset = p
        _preset.value = p
    }

    /** Saves the codec. */
    fun setCodec(c: String) {
        store.codec = c
        _codec.value = c
    }

    /** Saves how touches on the video act. */
    fun setTouchMode(m: TouchMode) {
        store.touchMode = m
        mapper.touchMode = m
        _touchMode.value = m
    }

    /** Shows or hides the link statistics overlay. */
    fun setShowStats(show: Boolean) {
        store.showStats = show
        _showStats.value = show
    }

    /** Saves the touchscreen trackpad speed. */
    fun setSensitivity(v: Float) {
        store.sensitivity = v
        mapper.touchSensitivity = v
        _sensitivity.value = v
    }

    /** Saves a new host and checks it. */
    fun addHost(name: String, ip: String, port: Int) {
        val p = HostProfile(UUID.randomUUID().toString(), name.ifBlank { ip }, ip.trim(), port)
        saveProfiles(_profiles.value + p)
        checkHealth(p)
    }

    /** Forgets a host and its pairing. */
    fun deleteHost(p: HostProfile) = saveProfiles(_profiles.value.filterNot { it.id == p.id })

    private fun saveProfiles(list: List<HostProfile>) {
        store.save(list)
        _profiles.value = list
    }

    private fun replace(p: HostProfile) = saveProfiles(_profiles.value.map { if (it.id == p.id) p else it })

    /** Checks every saved host. */
    fun checkAll() = _profiles.value.forEach(::checkHealth)

    /** Calls /health on a host and stores its MAC address for Wake-on-LAN. */
    fun checkHealth(p: HostProfile) {
        _health.update { it + (p.id to HostHealth.Checking) }
        viewModelScope.launch {
            val result = runCatching { ControlClient(getApplication(), p.ip, p.port, p.fingerprint).health() }
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
                val client = ControlClient(getApplication(), p.ip, p.port, fingerprint)
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

    /** The kind of network the tablet is on, so each one keeps its own quality level. */
    private fun networkType(): String {
        val cm = getApplication<Application>().getSystemService(ConnectivityManager::class.java)
        val caps = cm.getNetworkCapabilities(cm.activeNetwork) ?: return "none"
        return when {
            caps.hasTransport(NetworkCapabilities.TRANSPORT_VPN) -> "vpn"
            caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) -> "wifi"
            caps.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET) -> "lan"
            caps.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) -> "cellular"
            else -> "other"
        }
    }

    private fun deviceName(): String =
        Settings.Global.getString(getApplication<Application>().contentResolver, Settings.Global.DEVICE_NAME) ?: Build.MODEL

    /**
     * Starts a session with a paired host. A host that does not answer is
     * woken first when its MAC is known, and the session starts once it does.
     */
    fun connect(p: HostProfile) {
        val key = store.peerKey(p) ?: return showMessage(str(R.string.msg_not_paired))
        disconnect()
        connectJob = viewModelScope.launch {
            val client = ControlClient(getApplication(), p.ip, p.port, p.fingerprint)
            val up = runCatching { client.health() }.isSuccess
            if (up || p.mac == null || wakeAndWait(p, client)) startSession(p, key)
        }
    }

    /** Sends magic packets until the host answers /health or [WAKE_TIMEOUT_MS] passes. */
    private suspend fun wakeAndWait(p: HostProfile, client: ControlClient): Boolean {
        val mac = p.mac ?: return false
        _health.update { it + (p.id to HostHealth.Waking) }
        showMessage(str(R.string.msg_waking, p.name))
        val deadline = SystemClock.elapsedRealtime() + WAKE_TIMEOUT_MS
        var lastSent = 0L
        while (SystemClock.elapsedRealtime() < deadline) {
            if (SystemClock.elapsedRealtime() - lastSent >= WAKE_RESEND_MS) {
                val sent = withContext(Dispatchers.IO) { runCatching { sendMagicPacket(mac, p.ip) } }
                sent.exceptionOrNull()?.let { e ->
                    checkHealth(p)
                    showMessage(str(R.string.msg_wake_failed, e.message ?: e.toString()))
                    return false
                }
                lastSent = SystemClock.elapsedRealtime()
            }
            delay(WAKE_POLL_MS)
            if (runCatching { client.health() }.isSuccess) {
                checkHealth(p)
                return true
            }
        }
        _health.update { it + (p.id to HostHealth.Offline(str(R.string.msg_not_woken))) }
        showMessage(str(R.string.msg_not_woken_named, p.name))
        return false
    }

    private fun startSession(p: HostProfile, key: ByteArray) {
        val network = networkType()
        val s = StreamSession(getApplication(), p, key, _preset.value, _codec.value, store.level(p.id, network), viewModelScope)
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
            viewModelScope.launch {
                s.state.collect { st ->
                    val info = (st as? StreamSession.State.Streaming)?.info ?: return@collect
                    info.levels.getOrNull(info.level)?.takeIf { info.auto }?.let { store.setLevel(p.id, network, it) }
                }
            },
        )
        s.connect()
    }

    /** Ends the session, or stops waiting for a host to wake. */
    fun disconnect() {
        connectJob?.cancel()
        connectJob = null
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

    /** Opens or closes the HUD panel. */
    fun setHudOpen(open: Boolean) {
        _hudOpen.value = open
    }

    /** Shows or hides the soft keyboard and modifier bar. */
    fun setKeyboardShown(shown: Boolean) {
        _keyboardShown.value = shown
    }

    /** Latches or releases a sticky modifier. */
    fun toggleModifier(code: Int) {
        mapper.latch.toggle(code)
        _latched.value = latchState()
    }

    /** Presses and releases a key from the modifier bar. */
    fun tapKey(code: Int) = mapper.tapKey(code)

    /** Launches an app on the host and closes the HUD. */
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
        if (text.toByteArray().size > MAX_CLIPBOARD_BYTES) {
            // The host syncs at most 256 KB; larger text would end the session.
            if (!onlyIfChanged) showMessage(str(R.string.msg_clipboard_too_large))
            return
        }
        s.pushClipboard(text)
    }

    /** Asks the host for its clipboard. */
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
        // The limited broadcast reaches the LAN the tablet is on; the /24
        // broadcast is for routers that forward it from a VPN.
        val targets = listOf("255.255.255.255", ip.substringBeforeLast('.') + ".255", ip)
        DatagramSocket().use { sock ->
            sock.broadcast = true
            for (target in targets) {
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
        private const val WAKE_TIMEOUT_MS = 90_000L
        private const val WAKE_RESEND_MS = 10_000L
        private const val WAKE_POLL_MS = 2_000L
        private const val MAX_CLIPBOARD_BYTES = 256 * 1024
    }
}
