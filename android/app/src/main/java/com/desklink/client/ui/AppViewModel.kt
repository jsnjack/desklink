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
import com.desklink.client.input.Zoom
import com.desklink.client.model.AppEntry
import com.desklink.client.model.HostProfile
import com.desklink.client.model.InputEvent
import com.desklink.client.model.HostStore
import com.desklink.client.model.NearbyHost
import com.desklink.client.model.PairingCode
import com.desklink.client.model.planPairing
import com.desklink.client.model.usableAddress
import com.desklink.client.input.keysGoToDesktop
import com.desklink.client.model.ScreenMode
import com.desklink.client.model.StreamPreset
import com.desklink.client.net.AddressChoice
import com.desklink.client.net.ControlClient
import com.desklink.client.net.ProbeOutcome
import com.desklink.client.net.UdpResult
import com.desklink.client.net.udpProbe
import com.desklink.client.model.ControlProtocol
import com.desklink.client.net.ProbeRow
import com.desklink.client.net.StreamSession
import com.desklink.client.net.isConnectionRefused
import com.desklink.client.net.probeRows
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.withTimeoutOrNull
import com.desklink.client.video.HardwareDecoder
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import com.desklink.client.model.mergeSameMachine
import com.desklink.client.model.Health
import kotlinx.coroutines.joinAll
import kotlinx.coroutines.CompletableDeferred
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
import java.util.concurrent.ConcurrentHashMap
import kotlin.coroutines.resume
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.sync.Mutex
import java.net.Inet4Address
import android.net.nsd.NsdServiceInfo
import android.net.nsd.NsdManager
import android.net.wifi.WifiManager
import com.desklink.client.crypto.SessionCrypto
import kotlinx.coroutines.withTimeout
import com.desklink.client.model.OwnScale

/** Reachability of a saved host, from GET /health. */
sealed interface HostHealth {
    /** A check is running. */
    data object Checking : HostHealth

    /** Wake-on-LAN packets were sent and the app waits for the host to answer. */
    data object Waking : HostHealth

    /**
     * The host answered; [update] says which side to update when the host and
     * the app speak different protocol versions.
     */
    data class Online(val hostname: String, val version: String, val update: ControlProtocol.Update? = null) : HostHealth

    /** The host did not answer; [reason] is shown to the user. */
    data class Offline(val reason: String) : HostHealth
}

/** What asking a host's addresses for /health found. */
private sealed interface Probe {
    /** An address answered: the profile saved with what the host reported. */
    data class Reached(val profile: HostProfile, val health: Health) : Probe

    /** None answered, but the machine at [address] refused the connection. */
    data class Refused(val address: String) : Probe

    /** No address answered at all. */
    data object NoAnswer : Probe
}

/** The HUD pill's place: the side edge it sticks to and its height, 0..1. */
data class HudPill(val left: Boolean, val y: Float)

/**
 * "Test connection" for the host [hostName]: one row per address asked for
 * /health on [port], or null [rows] while the answers are still coming in.
 */
data class ConnectionTest(val hostName: String, val port: Int, val rows: List<ProbeRow>?)

/** App state that outlives configuration changes: profiles, settings and the session. */
class AppViewModel(app: Application) : AndroidViewModel(app) {
    private val store = HostStore(app)

    private val _profiles = MutableStateFlow(
        store.profiles().let { loaded ->
            // Entries of one machine paired on several networks become one host.
            mergeSameMachine(loaded).also { if (it != loaded) store.save(it) }
        },
    )
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

    private val _screenMode = MutableStateFlow(store.screenMode)
    /** What sessions show: a [ScreenMode] value. */
    val screenMode: StateFlow<String> = _screenMode

    private var currentProfile: HostProfile? = null

    // Keeps the Wi-Fi radio out of power saving while a session streams, so
    // it does not doze between packets and add jitter. Android applies it
    // only while the app is in the foreground with the screen on.
    private val wifiLock: WifiManager.WifiLock? = runCatching {
        app.getSystemService(WifiManager::class.java)
            .createWifiLock(WifiManager.WIFI_MODE_FULL_LOW_LATENCY, "DeskLink stream")
            .apply { setReferenceCounted(false) }
    }.getOrNull()

    private val _nearby = MutableStateFlow<List<NearbyHost>>(emptyList())
    /** Unpaired hosts announced on the local network. */
    val nearby: StateFlow<List<NearbyHost>> = _nearby
    private val nsd = app.getSystemService(NsdManager::class.java)
    private var discovery: NsdManager.DiscoveryListener? = null
    private val resolving = Mutex()

    /** Announced addresses being checked, as "profile id|address"; touched on the main thread only. */
    private val probingAnnounced = mutableSetOf<String>()

    private val _hudPill = MutableStateFlow(HudPill(store.hudPillLeft, store.hudPillY))
    /** Where the HUD pill sits. */
    val hudPill: StateFlow<HudPill> = _hudPill

    private val _playAudio = MutableStateFlow(store.playAudio)
    /** Whether the tablet plays the desktop's sound. */
    val playAudio: StateFlow<Boolean> = _playAudio

    private val _showStats = MutableStateFlow(store.showStats)
    /** Whether the stream shows the link statistics overlay. */
    val showStats: StateFlow<Boolean> = _showStats

    private val _sensitivity = MutableStateFlow(store.sensitivity)
    /** The touchscreen trackpad speed. */
    val sensitivity: StateFlow<Float> = _sensitivity

    private val _lockAfter = MutableStateFlow(store.lockAfter)

    /** Whether the desk locks when a session ends, saved; also sent to a running session. */
    val lockAfter: StateFlow<Boolean> = _lockAfter

    /** Saves whether the desk locks when a session ends, and tells a running session at once. */
    fun setLockAfter(lock: Boolean) {
        store.lockAfter = lock
        _lockAfter.value = lock
        _session.value?.setLockAfter(lock)
    }

    private val _appLock = MutableStateFlow(store.appLock)
    /** Whether Connect first asks for a fingerprint or the device's PIN. */
    val appLock: StateFlow<Boolean> = _appLock

    private val _connectionTest = MutableStateFlow<ConnectionTest?>(null)
    /** The connection test the dialog shows, or null when it is closed. */
    val connectionTest: StateFlow<ConnectionTest?> = _connectionTest
    private var testJob: Job? = null

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
        onHudToggle = { setHudOpen(!_hudOpen.value) },
        keysOpen = { keysToDesktop() },
    ).also {
        it.touchSensitivity = store.sensitivity
        it.touchMode = store.touchMode
    }

    /** The video's local pinch zoom, 1x unless the session mirrors a monitor. */
    val zoom: StateFlow<Zoom> = mapper.zoom.state

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

    /**
     * Looks for hosts announcing themselves with mDNS while the host list is
     * shown. A paired host announced at a new address gets that address once
     * it answers there with the pinned certificate; an unknown one is listed
     * for pairing by QR code.
     */
    fun startDiscovery() {
        if (discovery != null) return
        // A host on two networks (Wi-Fi and a VPN) is found once per network,
        // so it is gone only when it has been lost as often as it was found.
        val announced = mutableMapOf<String, Int>()
        val listener = object : NsdManager.DiscoveryListener {
            override fun onServiceFound(info: NsdServiceInfo) {
                synchronized(announced) { announced.merge(info.serviceName, 1, Int::plus) }
                viewModelScope.launch { resolve(info)?.let(::onDiscovered) }
            }

            override fun onServiceLost(info: NsdServiceInfo) {
                val gone = synchronized(announced) {
                    val left = (announced[info.serviceName] ?: 0) - 1
                    if (left > 0) announced[info.serviceName] = left else announced.remove(info.serviceName)
                    left <= 0
                }
                if (gone) _nearby.update { list -> list.filterNot { it.name == info.serviceName } }
            }

            override fun onDiscoveryStarted(serviceType: String) = Unit
            override fun onDiscoveryStopped(serviceType: String) = Unit
            override fun onStartDiscoveryFailed(serviceType: String, errorCode: Int) {
                Log.d(TAG, "mDNS discovery failed to start: $errorCode")
                discovery = null
            }

            override fun onStopDiscoveryFailed(serviceType: String, errorCode: Int) = Unit
        }
        discovery = listener
        runCatching { nsd.discoverServices(SERVICE_TYPE, NsdManager.PROTOCOL_DNS_SD, listener) }
            .onFailure { discovery = null }
    }

    /** Stops looking for hosts. */
    fun stopDiscovery() {
        discovery?.let { runCatching { nsd.stopServiceDiscovery(it) } }
        discovery = null
    }

    // Android resolves one service at a time before API 34, so resolves queue.
    @Suppress("DEPRECATION")
    private suspend fun resolve(info: NsdServiceInfo): NearbyHost? = resolving.withLock {
        suspendCancellableCoroutine { cont ->
            nsd.resolveService(info, object : NsdManager.ResolveListener {
                override fun onResolveFailed(info: NsdServiceInfo, errorCode: Int) {
                    if (cont.isActive) cont.resume(null)
                }

                override fun onServiceResolved(r: NsdServiceInfo) {
                    val fp = r.attributes["fp"]?.toString(Charsets.US_ASCII)
                    val host = r.host as? Inet4Address
                    val found = if (fp != null && host != null) NearbyHost(r.serviceName, host.hostAddress ?: "", r.port, fp) else null
                    if (cont.isActive) cont.resume(found)
                }
            })
        }
    }

    /**
     * Handles a host announced on the network. Anyone can announce any
     * fingerprint, so an unknown one is only listed for pairing by QR code,
     * and a known paired host's new address is kept only once it answers
     * /health there with the pinned certificate.
     */
    private fun onDiscovered(n: NearbyHost) {
        if (n.ip.isBlank()) return
        val known = _profiles.value.firstOrNull { it.fingerprint.equals(n.fingerprint, ignoreCase = true) }
        if (known == null) {
            _nearby.update { list -> list.filterNot { it.fingerprint == n.fingerprint } + n }
            return
        }
        _nearby.update { list -> list.filterNot { it.fingerprint == n.fingerprint } }
        if (!known.paired || !usableAddress(n.ip) || n.ip in known.candidates()) return
        val probe = known.id + "|" + n.ip
        if (!probingAnnounced.add(probe)) return
        viewModelScope.launch {
            try {
                // The stored port: that is what connecting to this address will use.
                runCatching { ControlClient(getApplication(), n.ip, known.port, known.fingerprint).health() }
                    .onFailure { Log.d(TAG, "announced address ${n.ip} of ${known.name} did not answer with its certificate: ${it.message}") }
                    .getOrNull() ?: return@launch
                val current = _profiles.value.firstOrNull { it.id == known.id } ?: return@launch
                if (n.ip !in current.candidates()) replace(current.withAddresses(current.ip, listOf(n.ip)))
            } finally {
                probingAnnounced.remove(probe)
            }
        }
    }

    /** Saves where the user dropped the HUD pill. */
    fun setHudPill(p: HudPill) {
        store.hudPillLeft = p.left
        store.hudPillY = p.y
        _hudPill.value = p
    }

    /** Saves what sessions show (a [ScreenMode] value); applies from the next connection. */
    fun setScreenMode(mode: String) {
        store.screenMode = mode
        _screenMode.value = mode
    }

    /**
     * Switches the running session between its own screen and a mirrored
     * monitor, saving the choice, by connecting again. Like the session's own
     * reconnects it does not ask for the app lock: the Connect that started
     * the session did.
     */
    fun switchScreen(current: String) {
        val p = currentProfile ?: return
        setScreenMode(if (current == ScreenMode.OWN) ScreenMode.MIRROR else ScreenMode.OWN)
        _hudOpen.value = false
        connect(p)
    }

    /**
     * Sets how long the computer stays awake after the session ends (a
     * KeepAwake value), for this session and later ones.
     */
    fun setKeepAwake(minutes: Int) {
        store.keepAwake = minutes
        _session.value?.setKeepAwake(minutes)
    }

    /**
     * Makes the own screen's scale one step smaller ([direction] -1) or larger
     * (+1), at once and for later sessions.
     */
    fun changeScale(direction: Int) {
        val next = OwnScale.step(store.ownScale, direction)
        store.ownScale = next
        _session.value?.setScale(next)
    }

    /**
     * Makes the own screen GNOME's primary monitor, with the top bar and the
     * overview, or gives the primary back to the desk. It lasts for this
     * session only: the desk gets the primary back when the session ends.
     */
    fun setPrimary(primary: Boolean) {
        _session.value?.setPrimary(primary)
    }

    /**
     * Turns the desk's monitors off, so every window moves to the tablet's
     * screen, or on again, which puts the windows back. It lasts for this
     * session only: the desk's monitors come on again when it ends.
     */
    fun setDeskOff(off: Boolean) {
        _session.value?.setDeskOff(off)
    }

    /** Saves whether the tablet plays the desktop's sound; applies from the next connection. */
    fun setPlayAudio(play: Boolean) {
        store.playAudio = play
        _playAudio.value = play
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

    /**
     * Saves whether Connect first asks for a fingerprint or the device's PIN.
     * The caller asks before turning it off ([withAppLock]).
     */
    fun setAppLock(on: Boolean) {
        store.appLock = on
        _appLock.value = on
    }

    /** Saves a new host and checks it. */
    fun addHost(name: String, ip: String, port: Int) {
        val p = HostProfile(UUID.randomUUID().toString(), name.ifBlank { ip }, ip.trim(), port)
        saveProfiles(_profiles.value + p)
        checkHealth(p)
    }

    /** Forgets a host and its pairing. */
    fun deleteHost(p: HostProfile) = saveProfiles(_profiles.value.filterNot { it.id == p.id })

    /**
     * Forgets a host: a paired one is first asked to forget this tablet, so
     * its pairing cannot be used again; the host is removed here either way.
     * Unless the host confirmed it, the message says only this tablet's
     * profile went and how to remove the pairing on the host.
     */
    fun forgetHost(p: HostProfile) {
        viewModelScope.launch {
            val error = if (p.paired) forgetOnHost(p) else null
            deleteHost(p)
            showMessage(
                when {
                    !p.paired -> str(R.string.msg_forgot, p.name)
                    error == null -> str(R.string.msg_forgot_on_host, p.name)
                    else -> str(R.string.msg_forgot_here_only, p.name, error, deviceName())
                }
            )
        }
    }

    /** Asks the host to forget this tablet; returns why it failed, or null. */
    private suspend fun forgetOnHost(p: HostProfile): String? = runCatching {
        withTimeout(FORGET_TIMEOUT_MS) {
            val key = store.peerKey(p) ?: error(str(R.string.msg_not_paired))
            val (reached, h) = reach(p) ?: error(str(R.string.msg_forget_unreachable))
            val token = SessionCrypto.authToken(p.peerId ?: error(str(R.string.msg_not_paired)), key, h.timeMs / 1000)
            ControlClient(getApplication(), reached.ip, reached.port, p.fingerprint).forget(token)
        }
    }.exceptionOrNull()?.let { e ->
        Log.w(TAG, "forget this tablet on the host", e)
        e.message ?: e.toString()
    }

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
            val health = when (val result = probe(p)) {
                is Probe.Reached -> HostHealth.Online(
                    result.health.hostname, result.health.version, ControlProtocol.advice(result.health.protocol),
                )
                is Probe.Refused -> HostHealth.Offline(str(R.string.err_not_running, result.address))
                Probe.NoAnswer -> HostHealth.Offline(str(R.string.err_no_address, p.candidates().joinToString()))
            }
            _health.update { it + (p.id to health) }
        }
    }

    /** The profile [probe] reached and its /health answer; null when none answered. */
    private suspend fun reach(p: HostProfile, avoid: Set<String> = emptySet()): Pair<HostProfile, Health>? =
        (probe(p, avoid) as? Probe.Reached)?.let { it.profile to it.health }

    /**
     * Asks every known address of [p] for /health at once, trusting only the
     * pinned certificate, and returns the profile reached at the first that
     * answers, saved with the addresses and MAC the host reports. When none
     * answers it tells a machine that refused the connection (on, without
     * desklinkd) from one that did not answer at all (asleep, or not on this
     * network). On a LAN the local address answers before a VPN one. The
     * probes run in the view model's scope, so a slow address never delays
     * the result. An unpaired host has no certificate from a QR code to pin,
     * so whoever answers for it could report anything: only its reached
     * address is saved, not the addresses or MAC it reports.
     */
    private suspend fun probe(p: HostProfile, avoid: Set<String> = emptySet()): Probe {
        val trusted = p.paired
        val candidates = p.candidates()
        val refused = ConcurrentHashMap.newKeySet<String>()
        val found = CompletableDeferred<Probe>()
        val choice = AddressChoice<Probe.Reached>(avoid)
        val probes = candidates.map { addr ->
            viewModelScope.launch {
                runCatching { ControlClient(getApplication(), addr, p.port, p.fingerprint).health() }
                    .onSuccess { h ->
                        choice.answered(addr, Probe.Reached(p.withAddresses(addr, if (trusted) h.addresses else emptyList()), h))
                            ?.let(found::complete)
                    }
                    .onFailure { e -> if (isConnectionRefused(e)) refused += addr }
            }
        }
        viewModelScope.launch {
            probes.joinAll()
            found.complete(choice.last() ?: candidates.firstOrNull { it in refused }?.let(Probe::Refused) ?: Probe.NoAnswer)
        }
        val result = found.await()
        if (result !is Probe.Reached) return result
        val (reached, h) = result
        val current = _profiles.value.firstOrNull { it.id == p.id } ?: return result
        val updated = current.withAddresses(reached.ip, reached.candidates()).let {
            if (trusted && h.mac.isNotBlank()) it.copy(mac = h.mac) else it
        }
        if (updated != current) replace(updated)
        return Probe.Reached(updated, h)
    }

    /**
     * Asks every address of [p] for /health with the pinned certificate, as
     * [probe] does, but waits for all of them and shows each result in the
     * connection test dialog. It saves nothing: it only reports.
     */
    fun testConnection(p: HostProfile) {
        testJob?.cancel()
        val candidates = p.candidates()
        _connectionTest.value = ConnectionTest(p.name, p.port, null)
        testJob = viewModelScope.launch {
            val results = candidates.map { addr -> async { addr to testAddress(p, addr) } }.awaitAll()
            _connectionTest.value = ConnectionTest(p.name, p.port, probeRows(results))
        }
    }

    /** Closes the connection test dialog and stops waiting for its answers. */
    fun closeConnectionTest() {
        testJob?.cancel()
        testJob = null
        _connectionTest.value = null
    }

    /**
     * One address's answer to /health, timed. The call blocks an IO thread
     * until OkHttp's own timeouts (up to 10 s for a stalled read), which
     * cancelling cannot cut short, so the test stops waiting after
     * [TEST_TIMEOUT_MS] instead and lets the call finish unseen.
     */
    private suspend fun testAddress(p: HostProfile, addr: String): ProbeOutcome {
        val started = SystemClock.elapsedRealtime()
        val call = viewModelScope.async {
            runCatching { ControlClient(getApplication(), addr, p.port, p.fingerprint).health() }
        }
        val result = withTimeoutOrNull(TEST_TIMEOUT_MS) { call.await() } ?: return ProbeOutcome.TimedOut
        val h = result.getOrElse { return ProbeOutcome.of(it) }
        val ms = SystemClock.elapsedRealtime() - started
        // The stream needs UDP to the same address, which a firewall can drop
        // while HTTPS gets through.
        val key = store.peerKey(p)
        val id = p.peerId
        val udp = when {
            h.udpPort == 0 || key == null || id == null -> UdpResult.Unsupported
            else -> udpProbe(addr, h.udpPort, id, key)?.let(UdpResult::Answered) ?: UdpResult.NoAnswer
        }
        return ProbeOutcome.Answered(ms, h.hostname, h.version, udp)
    }

    /**
     * Pairs with the QR code `desklinkd pair` shows, the only way to pair:
     * its fingerprint is pinned, so only the desktop that showed the code can
     * take part, and a code without one is refused. [chosen] is the saved
     * host whose Pair button opened the scanner, [announced] the host found
     * on the network whose Pair button did; the code's certificate wins over
     * theirs, and the message says so when they differ.
     */
    fun pairFromQr(raw: String, chosen: HostProfile? = null, announced: NearbyHost? = null) {
        val qr = when (val code = PairingCode.parse(raw)) {
            is PairingCode.Valid -> code.qr
            PairingCode.NoFingerprint -> return showMessage(str(R.string.msg_qr_no_fingerprint))
            PairingCode.Invalid -> return showMessage(str(R.string.msg_bad_qr))
        }
        val plan = planPairing(_profiles.value, qr, chosen?.id, announced) { UUID.randomUUID().toString() }
        if (plan.isNew) saveProfiles(_profiles.value + plan.profile) else replace(plan.profile)
        val note = when {
            plan.announcedDiffers && announced != null -> str(R.string.msg_cert_announced_differs, announced.name)
            plan.savedDiffers -> str(R.string.msg_cert_saved_differs, plan.profile.name)
            else -> null
        }
        pair(plan.profile, qr.pin, plan.fingerprint, note)
    }

    /**
     * Runs the PIN exchange with [p], trusting only the certificate
     * [fingerprint] from the QR code. [note] goes in front of the result
     * message.
     */
    private fun pair(p: HostProfile, pin: String, fingerprint: String, note: String?) {
        _pairing.value = true
        val say = { text: String -> showMessage(listOfNotNull(note, text).joinToString(" ")) }
        viewModelScope.launch {
            try {
                val client = ControlClient(getApplication(), p.ip, p.port, fingerprint)
                val health = runCatching { client.health() }.getOrNull()
                val result = client.pair(pin.trim(), deviceName())
                val current = (_profiles.value.firstOrNull { it.id == p.id } ?: p)
                    .withAddresses(p.ip, health?.addresses.orEmpty())
                replace(store.withPairing(current, result, health?.mac))
                // Pairing a known machine on a new network folds the new entry
                // into the old one, which keeps its name and quality levels.
                saveProfiles(mergeSameMachine(_profiles.value, fresh = current.id))
                _nearby.update { list -> list.filterNot { it.fingerprint.equals(result.fingerprint, ignoreCase = true) } }
                say(str(R.string.msg_paired, result.fingerprint.take(16)))
                _profiles.value.firstOrNull { it.fingerprint.equals(result.fingerprint, ignoreCase = true) }
                    ?.let { checkHealth(it) }
            } catch (e: Exception) {
                Log.w(TAG, "pairing failed", e)
                say(str(R.string.msg_pair_failed, e.message ?: e.toString()))
            } finally {
                _pairing.value = false
            }
        }
    }

    /**
     * The network the tablet is on, so each one keeps its own quality level:
     * its kind and its gateway, which tells home Wi-Fi from office Wi-Fi
     * without the location permission an SSID needs.
     */
    private fun networkType(): String {
        val cm = getApplication<Application>().getSystemService(ConnectivityManager::class.java)
        val net = cm.activeNetwork ?: return "none"
        val caps = cm.getNetworkCapabilities(net) ?: return "none"
        val gateway = cm.getLinkProperties(net)?.routes
            ?.firstOrNull { it.isDefaultRoute && it.gateway != null }?.gateway?.hostAddress
        val kind = when {
            caps.hasTransport(NetworkCapabilities.TRANSPORT_VPN) -> "vpn"
            caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) -> "wifi"
            caps.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET) -> "lan"
            caps.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) -> "cellular"
            else -> "other"
        }
        return if (gateway != null) "$kind:$gateway" else kind
    }

    /**
     * The level to start at on [network]. Levels saved before networks were
     * told apart by their gateway are under the bare kind ("wifi"), and serve
     * until this network has its own.
     */
    private fun startLevel(id: String, network: String) =
        store.level(id, network) ?: store.level(id, network.substringBefore(':'))

    private fun deviceName(): String =
        Settings.Global.getString(getApplication<Application>().contentResolver, Settings.Global.DEVICE_NAME) ?: Build.MODEL

    /**
     * Starts a session with a paired host. A host that does not answer is
     * woken first when its MAC is known, and the session starts once it does.
     * A host that refuses the connection is already on, so waking it would
     * only keep the user waiting: it says at once that desklinkd is not
     * running there.
     */
    fun connect(p: HostProfile) {
        val key = store.peerKey(p) ?: return showMessage(str(R.string.msg_not_paired))
        disconnect()
        connectJob = viewModelScope.launch {
            val reached = when (val result = probe(p)) {
                is Probe.Reached -> result.profile
                is Probe.Refused -> return@launch showNotRunning(p, result.address)
                Probe.NoAnswer -> if (p.mac != null) wakeAndWait(p) ?: return@launch else p
            }
            startSession(reached, key)
        }
    }

    /** Says that [p] is on but desklinkd does not run there: [address] refused the connection. */
    private fun showNotRunning(p: HostProfile, address: String) {
        _health.update { it + (p.id to HostHealth.Offline(str(R.string.err_not_running, address))) }
        showMessage(str(R.string.msg_not_running_named, p.name, address))
    }

    /**
     * Sends magic packets towards every address of [p] until one answers
     * /health or [WAKE_TIMEOUT_MS] passes; returns the host reached, or null.
     * It keeps waiting while the machine refuses connections, because
     * desklinkd starts a little after the network during an automatic login,
     * but when the last attempt was refused it says the machine is on.
     */
    private suspend fun wakeAndWait(p: HostProfile): HostProfile? {
        val mac = p.mac ?: return null
        _health.update { it + (p.id to HostHealth.Waking) }
        showMessage(str(R.string.msg_waking, p.name))
        val deadline = SystemClock.elapsedRealtime() + WAKE_TIMEOUT_MS
        var lastSent = 0L
        var refused: String? = null
        while (SystemClock.elapsedRealtime() < deadline) {
            if (SystemClock.elapsedRealtime() - lastSent >= WAKE_RESEND_MS) {
                val sent = withContext(Dispatchers.IO) { runCatching { sendWake(mac, p.candidates()) } }
                sent.exceptionOrNull()?.let { e ->
                    checkHealth(p)
                    showMessage(str(R.string.msg_wake_failed, e.message ?: e.toString()))
                    return null
                }
                lastSent = SystemClock.elapsedRealtime()
            }
            delay(WAKE_POLL_MS)
            when (val result = probe(p)) {
                is Probe.Reached -> {
                    checkHealth(result.profile)
                    return result.profile
                }
                is Probe.Refused -> refused = result.address
                Probe.NoAnswer -> refused = null
            }
        }
        if (refused != null) {
            showNotRunning(p, refused)
        } else {
            _health.update { it + (p.id to HostHealth.Offline(str(R.string.msg_not_woken))) }
            showMessage(str(R.string.msg_not_woken_named, p.name))
        }
        return null
    }

    private fun startSession(p: HostProfile, key: ByteArray) {
        val network = networkType()
        val s = StreamSession(getApplication(), p, key, _preset.value, _codec.value, startLevel(p.id, network), viewModelScope,
            audio = _playAudio.value, screen = _screenMode.value, scale = store.ownScale, keepAwake = store.keepAwake, lockAfter = store.lockAfter,
            // A reconnect tries every address saved by then and saves the one that answers.
            reach = { host, avoid -> reach(_profiles.value.firstOrNull { it.id == host.id } ?: host, avoid) })
        currentProfile = p
        runCatching { wifiLock?.acquire() }.onFailure { Log.d(TAG, "Wi-Fi lock: ${it.message}") }
        _session.value = s
        _hudOpen.value = false
        // A new session starts with the whole video, whatever the last one zoomed.
        mapper.zoom.enabled = false
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
                    // The own screen is drawn 1:1 for the tablet; only a mirrored monitor zooms.
                    mapper.zoom.enabled = info.screen == ScreenMode.MIRROR
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
        if (wifiLock?.isHeld == true) wifiLock.release()
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

    /**
     * Whether keys go to the desktop: while the stream is on screen with the
     * HUD closed. While connecting, reconnecting or with the HUD open, every
     * key stays with Android.
     */
    fun keysToDesktop(): Boolean = keysGoToDesktop(_session.value?.state?.value, _hudOpen.value)

    /** Opens or closes the HUD panel; while it is open, keys stay with Android. */
    fun setHudOpen(open: Boolean) {
        if (open && !_hudOpen.value) releaseInput()
        _hudOpen.value = open
    }

    /** Shows or hides the soft keyboard and modifier bar. */
    fun setKeyboardShown(shown: Boolean) {
        if (shown && !_keyboardShown.value) releaseInput()
        _keyboardShown.value = shown
    }

    /**
     * Releases every key, button and pen contact held on the host, for when
     * keys stop reaching the session (focus lost, the activity paused, the
     * HUD or soft keyboard took over), so a key released meanwhile does not
     * stay down on the desktop.
     */
    fun releaseInput() {
        _session.value?.releaseInput()
    }

    /** Latches or releases a sticky modifier. */
    fun toggleModifier(code: Int) {
        mapper.latch.toggle(code)
        _latched.value = latchState()
    }

    /** Presses and releases a key from the modifier bar. */
    fun tapKey(code: Int) = mapper.tapKey(code)

    /** Shows the whole video again after a pinch zoomed it. */
    fun resetZoom() = mapper.zoom.reset()

    /** Launches an app on the host and closes the HUD. */
    /**
     * Presses [codes] together on the desktop and releases them in reverse,
     * like a shortcut (Win+A opens GNOME's app grid, Win the overview), then
     * closes the HUD.
     */
    fun pressKeys(vararg codes: Int) {
        _session.value?.sendInput(codes.map { InputEvent.key(it, true) } + codes.reversed().map { InputEvent.key(it, false) })
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
            val result = withContext(Dispatchers.IO) { runCatching { sendWake(mac, p.candidates()) } }
            showMessage(result.fold({ str(R.string.msg_wake_sent) }, { str(R.string.msg_wake_failed, it.message ?: "") }))
        }
    }

    /**
     * Sends magic packets for [mac] to the LAN the tablet is on and towards
     * each of the host's [addresses]. A target without a route (a VPN that is
     * down) does not stop the others; it fails only when no packet left.
     */
    private fun sendWake(mac: String, addresses: List<String>) {
        val macBytes = mac.split(':', '-').map { it.toInt(16).toByte() }.toByteArray()
        require(macBytes.size == 6) { "bad MAC address $mac" }
        val packet = ByteArray(6) { 0xFF.toByte() } + List(16) { macBytes }.reduce { a, b -> a + b }
        // The limited broadcast reaches the LAN the tablet is on; the /24
        // broadcast is for routers that forward it from a VPN.
        val targets = (listOf("255.255.255.255") + addresses.flatMap { listOf(it.substringBeforeLast('.') + ".255", it) }).distinct()
        var sent = 0
        var failure: Exception? = null
        DatagramSocket().use { sock ->
            sock.broadcast = true
            for (target in targets) {
                try {
                    sock.send(DatagramPacket(packet, packet.size, InetAddress.getByName(target), WOL_PORT))
                    sent++
                } catch (e: Exception) {
                    failure = e
                }
            }
        }
        if (sent == 0) throw failure ?: IllegalStateException("no address to wake")
    }

    override fun onCleared() {
        disconnect()
    }

    companion object {
        private const val TAG = "DeskLink"
        private const val WOL_PORT = 9
        private const val SERVICE_TYPE = "_desklink._tcp"
        private const val WAKE_TIMEOUT_MS = 90_000L

        /** Asking a host to forget this tablet gives up after this; it is removed here anyway. */
        private const val FORGET_TIMEOUT_MS = 8_000L
        private const val WAKE_RESEND_MS = 10_000L
        private const val WAKE_POLL_MS = 2_000L
        private const val MAX_CLIPBOARD_BYTES = 256 * 1024

        /** How long the connection test waits for one address; connecting alone gives up after 3 s. */
        private const val TEST_TIMEOUT_MS = 5_000L
    }
}
