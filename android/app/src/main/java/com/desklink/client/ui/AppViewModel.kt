package com.desklink.client.ui

import android.app.Application
import android.content.ClipData
import android.content.ClipboardManager
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.net.Uri
import androidx.core.content.FileProvider
import com.desklink.client.model.ClipboardImages
import com.desklink.client.model.readAtMost
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.os.Handler
import android.os.Looper
import android.os.Build
import android.os.SystemClock
import android.provider.Settings
import android.util.Log
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.viewModelScope
import com.desklink.client.R
import com.desklink.client.input.InputMapper
import com.desklink.client.input.LocalPointer
import com.desklink.client.input.TouchMode
import com.desklink.client.input.Zoom
import com.desklink.client.model.AppEntry
import com.desklink.client.model.HostProfile
import com.desklink.client.model.HostSettings
import com.desklink.client.model.InputEvent
import com.desklink.client.model.HostStore
import com.desklink.client.model.NearbyHost
import com.desklink.client.model.PairingCode
import com.desklink.client.model.planPairing
import com.desklink.client.model.usableAddress
import com.desklink.client.input.keysGoToDesktop
import com.desklink.client.model.ScreenMode
import com.desklink.client.model.StreamPreset
import com.desklink.client.model.levelToRemember
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
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.stateIn
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
import com.desklink.client.model.StreamSize
import com.desklink.client.model.streamSizeFor
import android.Manifest
import android.content.pm.PackageManager
import androidx.core.content.ContextCompat
import com.desklink.client.audio.MicPolicy

/** Reachability of a saved host, from GET /health. */
sealed interface HostHealth {
    /** A check is running. */
    data object Checking : HostHealth

    /** Wake-on-LAN packets were sent and the app waits for the host to answer. */
    data object Waking : HostHealth

    /**
     * The host answered within [ms], the TLS handshake included; [update]
     * says which side to update when the host and the app speak different
     * protocol versions.
     */
    data class Online(
        val hostname: String,
        val version: String,
        val ms: Long,
        val update: ControlProtocol.Update? = null,
    ) : HostHealth

    /**
     * The host did not answer; [reason] is shown in its details. [why] and
     * the [tried] addresses, or the [address] that refused, give the main
     * page's banner its sentence.
     */
    data class Offline(val reason: String, val why: Why = Why.NO_ANSWER, val tried: Int = 0, val address: String? = null) : HostHealth

    /** Why a host is offline. */
    enum class Why {
        /** No address answered. */
        NO_ANSWER,

        /** The machine refused the connection: it is on, without desklinkd. */
        REFUSED,

        /** Wake-on-LAN was sent and the host still did not answer. */
        NOT_WOKEN,
    }
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
            mergeSameMachine(loaded).also { merged ->
                if (merged != loaded) {
                    store.save(merged)
                    (loaded.map { it.id } - merged.map { it.id }.toSet()).forEach(store::forgetSettings)
                }
            }
        },
    )
    /** The saved hosts. */
    val profiles: StateFlow<List<HostProfile>> = _profiles

    private val _hostSettings = MutableStateFlow(_profiles.value.associate { it.id to store.settings(it.id) })
    /** Each saved host's settings, by profile ID. */
    val hostSettings: StateFlow<Map<String, HostSettings>> = _hostSettings

    private val _health = MutableStateFlow<Map<String, HostHealth>>(emptyMap())
    /** The last health check of each host, by profile ID. */
    val health: StateFlow<Map<String, HostHealth>> = _health

    /** Whether this device decodes AV1 in hardware. */
    val av1Available: Boolean = HardwareDecoder.bestCodec() == HardwareDecoder.CODEC_AV1

    /** The codec [s] asks for: its choice, unless that is AV1 on a tablet without a hardware AV1 decoder, or the best one. */
    fun codecOf(s: HostSettings): String = s.codec?.takeIf { it != HardwareDecoder.CODEC_AV1 || av1Available } ?: HardwareDecoder.bestCodec()

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

    private val _showStats = MutableStateFlow(store.showStats)
    /** Whether the stream shows the link statistics overlay. */
    val showStats: StateFlow<Boolean> = _showStats

    // Whether the activity is started; the microphone is captured only then.
    private var foreground = true

    /** Whether the app may record; the HUD asks for the permission when the switch is first turned on. */
    fun micPermitted(): Boolean =
        ContextCompat.checkSelfPermission(getApplication(), Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED

    /** Says why the microphone stays off: the permission was refused. */
    fun micRefused() = showMessage(str(R.string.msg_mic_refused))

    /**
     * The activity was started or stopped. Android gives a background app
     * only silence from the microphone and the stream stops there anyway, so
     * capture pauses until the app is back.
     */
    fun setForeground(fg: Boolean) {
        foreground = fg
        _session.value?.setForeground(fg)
    }

    private val _appLock = MutableStateFlow(store.appLock)
    /** Whether Connect first asks for a fingerprint or the device's PIN. */
    val appLock: StateFlow<Boolean> = _appLock

    private val _selectedHost = MutableStateFlow(store.selectedHost)
    /** The profile ID of the host last chosen on the main page; [selectedHost] falls back to the first. */
    val selectedHostId: StateFlow<String?> = _selectedHost

    /** Remembers the host the main page shows the details of. */
    fun selectHost(id: String) {
        store.selectedHost = id
        _selectedHost.value = id
    }

    private val _connectionTest = MutableStateFlow<ConnectionTest?>(null)
    /** The connection test the dialog shows, or null when it is closed. */
    val connectionTest: StateFlow<ConnectionTest?> = _connectionTest
    private var testJob: Job? = null

    private val _session = MutableStateFlow<StreamSession?>(null)
    /** The running session, or null on the connect screen. */
    val session: StateFlow<StreamSession?> = _session

    private val panelWatch = PanelWatch(app)
    private val _panel = MutableStateFlow<PanelState?>(null)
    /** The panel's refresh rate and power saving, sampled while a session runs, for the stats overlay. */
    val panel: StateFlow<PanelState?> = _panel

    /**
     * The panel's highest refresh rate while a session runs: it changes when
     * Samsung's motion smoothness setting adds or hides the faster modes, and
     * then the activity and the video surface ask for the new highest rate.
     */
    val panelHighestHz: StateFlow<Float?> = _panel.map { it?.maxHz }.stateIn(viewModelScope, SharingStarted.Eagerly, null)

    // When the screen was last touched, on the uptime clock; Samsung's
    // adaptive refresh comes up to its highest rate at a touch.
    @Volatile
    private var lastTouchMs = Long.MIN_VALUE / 2

    private val _metered = MutableStateFlow(false)
    /** Whether the network the tablet's data would cross is metered; the connect screen then says what a stream costs. */
    val metered: StateFlow<Boolean> = _metered
    private val connectivity: ConnectivityManager? = app.getSystemService(ConnectivityManager::class.java)
    private val meteredWatch = object : ConnectivityManager.NetworkCallback() {
        override fun onCapabilitiesChanged(network: Network, caps: NetworkCapabilities) {
            _metered.value = !caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_METERED)
        }

        override fun onLost(network: Network) {
            _metered.value = false
        }
    }

    init {
        // The best network that is not a VPN, which the data really crosses:
        // a VPN app that never says otherwise counts as metered even on
        // Wi-Fi, and an office VPN is the usual way in.
        val physical = NetworkRequest.Builder()
            .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            .addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
            .build()
        connectivity?.let { cm ->
            runCatching { cm.registerBestMatchingNetworkCallback(physical, meteredWatch, Handler(Looper.getMainLooper())) }
                .onFailure { Log.d(TAG, "cannot watch whether the network is metered: ${it.message}") }
        }
    }

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

    /**
     * The desk's pointer as the tablet draws it when the session leaves it out
     * of the video: the input sent moves it at once.
     */
    val pointer = LocalPointer(SystemClock::uptimeMillis)

    /** Turns every Android input source into host input for the running session. */
    val mapper = InputMapper(
        send = { events ->
            pointer.sent(events)
            _session.value?.sendInput(events)
            _latched.value = latchState()
        },
        onHudToggle = { setHudOpen(!_hudOpen.value) },
        keysOpen = { keysToDesktop() },
        sendText = { _session.value?.typeText(it) },
        typesText = { _session.value?.typesText == true },
        mouseInfo = { _session.value?.mouse },
        scrollPixels = { _session.value?.scrollPx == true },
    )

    /** The video's local pinch zoom, 1x unless the session mirrors a monitor. */
    val zoom: StateFlow<Zoom> = mapper.zoom.state

    private var sessionJobs = emptyList<Job>()
    private var connectJob: Job? = null
    private var lastClipboard: String? = null

    /** The clipboard image last sent to the host ([ClipImage.key]). */
    private var lastImageKey: String? = null
    // The stream view's size as a stream size, from the stream screen's
    // layout, and the window's, which stands in until the stream screen has
    // been laid out. Both are in physical pixels.
    private var viewSize: StreamSize? = null
    private var windowSize: StreamSize? = null
    private val resizer = ScreenResizer(RESIZE_QUIET_MS) { SystemClock.elapsedRealtime() }
    private var resizeJob: Job? = null

    private fun latchState(): Set<Int> = mapper.latch.active

    private fun str(id: Int, vararg args: Any): String = getApplication<Application>().getString(id, *args)

    /** Shows [text] once; null clears it. */
    fun showMessage(text: String?) {
        _message.value = text
    }

    /** The settings of the saved host [id]. */
    fun settingsOf(id: String): HostSettings = _hostSettings.value[id] ?: store.settings(id)

    /** Saves the settings of the host [id] as [change] makes them and returns them. */
    private fun changeSettings(id: String, change: (HostSettings) -> HostSettings): HostSettings {
        val next = change(settingsOf(id))
        store.saveSettings(id, next)
        _hostSettings.update { it + (id to next) }
        return next
    }

    /** The running session, when it shows the host [id]. */
    private fun sessionWith(id: String): StreamSession? = _session.value?.takeIf { currentProfile?.id == id }

    /**
     * Connects to the session's host again, for a change the running
     * connection cannot take; the new connection asks for the host's saved
     * settings. Like the session's own reconnects it does not ask for the
     * app lock: the Connect that started the session did.
     */
    private fun connectAgain() {
        val p = currentProfile ?: return
        _hudOpen.value = false
        connect(p)
    }

    /** Saves the host's stream preset; a session with it changes its stream at once. */
    fun setPreset(id: String, preset: StreamPreset) {
        changeSettings(id) { it.copy(preset = preset) }
        sessionWith(id)?.let { if (!it.changeStream(preset)) connectAgain() }
    }

    /** Saves the host's codec; a session with it changes encoder and decoder at once. */
    fun setCodec(id: String, codec: String) {
        val s = changeSettings(id) { it.copy(codec = codec) }
        sessionWith(id)?.let { if (!it.changeStream(s.preset, codec = codec)) connectAgain() }
    }

    /** Saves how touches on the video act for the host. */
    fun setTouchMode(id: String, mode: TouchMode) {
        changeSettings(id) { it.copy(touch = mode) }
        if (sessionWith(id) != null) mapper.touchMode = mode
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

    /**
     * Saves what the host's sessions show (a [ScreenMode] value). A session
     * with it switches between its own screen and a mirrored monitor in
     * place, or by connecting again to a host that cannot; the HUD closes,
     * since the screen under it changes.
     */
    fun setScreenMode(id: String, mode: String) {
        val s = changeSettings(id) { it.copy(screen = mode) }
        val session = sessionWith(id) ?: return
        _hudOpen.value = false
        val monitor = if (mode == ScreenMode.MIRROR) store.mirrorMonitor(id) else null
        if (!session.changeStream(s.preset, screen = mode, monitor = monitor)) connectAgain()
    }

    /**
     * Mirrors the host's monitor on [connector] and remembers it for the
     * host's later mirroring sessions: a mirroring session switches at once,
     * and the own screen switches to mirroring it.
     */
    fun chooseMonitor(connector: String) {
        val p = currentProfile ?: return
        store.setMirrorMonitor(p.id, connector)
        val s = _session.value ?: return
        if ((s.state.value as? StreamSession.State.Streaming)?.info?.screen == ScreenMode.MIRROR) {
            s.mirror(connector)
            return
        }
        setScreenMode(p.id, ScreenMode.MIRROR)
    }

    /**
     * The stream view now measures [width] x [height] physical pixels. Later
     * hellos ask for a stream of that size, and while the session shows its
     * own screen, the screen follows once the size has held for a moment, as
     * after a rotation ([ScreenResizer]).
     */
    fun setStreamViewSize(width: Int, height: Int) {
        val size = streamSizeFor(width, height) ?: return
        viewSize = size
        _session.value?.setViewSize(size)
        resizer.viewChanged(size)
        resizeJob?.cancel()
        resizeJob = viewModelScope.launch {
            delay(RESIZE_QUIET_MS)
            followView()
        }
    }

    /** The window measures [width] x [height] physical pixels; it stands in for the stream view's size until that is known. */
    fun setWindowSize(width: Int, height: Int) {
        windowSize = streamSizeFor(width, height)
    }

    /** Asks for the own screen at the stream view's size once that has held, while the session streams its own screen. */
    private fun followView() {
        val s = _session.value ?: return
        val info = (s.state.value as? StreamSession.State.Streaming)?.info ?: return
        if (info.screen != ScreenMode.OWN) return
        val top = info.levels.firstOrNull() ?: return
        resizer.due(StreamSize(top.width, top.height))?.let(s::resize)
    }

    /**
     * Saves how long the computer stays awake after the host's sessions (a
     * KeepAwake value); a session with it takes it at once.
     */
    fun setKeepAwake(id: String, minutes: Int) {
        changeSettings(id) { it.copy(keepAwake = minutes) }
        sessionWith(id)?.setKeepAwake(minutes)
    }

    /**
     * Makes the host's own screen's scale one step smaller ([direction] -1)
     * or larger (+1), at once in a session with it and for later sessions.
     */
    fun changeScale(id: String, direction: Int) {
        val next = changeSettings(id) { it.copy(scale = OwnScale.step(it.scale, direction)) }.scale
        sessionWith(id)?.setScale(next)
    }

    /**
     * Saves whether the host's own screen takes GNOME's primary monitor, with
     * the top bar and the overview, or leaves it on the desk; a session with
     * it moves the primary at once.
     */
    fun setPrimary(id: String, primary: Boolean) {
        changeSettings(id) { it.copy(primary = primary) }
        sessionWith(id)?.setPrimary(primary)
    }

    /**
     * Saves whether the host's own screen turns the desk's monitors off, so
     * every window moves to the tablet's screen; a session with it turns them
     * off, or on again, at once. They come on again when a session ends.
     */
    fun setDeskOff(id: String, off: Boolean) {
        changeSettings(id) { it.copy(deskOff = off) }
        sessionWith(id)?.setDeskOff(off)
    }

    /**
     * Saves whether the tablet plays the host's sound. A session with it
     * switches at once, or connects again on a host that cannot add the
     * sound to a running session.
     */
    fun setPlayAudio(id: String, play: Boolean) {
        changeSettings(id) { it.copy(sound = play) }
        sessionWith(id)?.let { if (!it.setPlayAudio(play)) connectAgain() }
    }

    /** Saves whether the host's sessions sharpen coloured edges on the GPU; a session with it switches at once. */
    fun setSharperColours(id: String, on: Boolean) {
        changeSettings(id) { it.copy(sharperColours = on) }
        sessionWith(id)?.setSharperColours(on)
    }

    /**
     * Saves whether the host's sessions draw exact pixels of still parts of
     * the screen. A session with it turns them on or off at once, or connects
     * again when its connection did not ask for them.
     */
    fun setExactText(id: String, on: Boolean) {
        changeSettings(id) { it.copy(exactText = on) }
        sessionWith(id)?.let { if (!it.setExactText(on)) connectAgain() }
    }

    /** Saves the host's touchscreen trackpad speed. */
    fun setSensitivity(id: String, speed: Float) {
        changeSettings(id) { it.copy(trackpadSpeed = speed) }
        if (sessionWith(id) != null) mapper.touchSensitivity = speed
    }

    /** Saves the host's scroll speed; a session with it scrolls at the new speed at once. */
    fun setScrollSpeed(id: String, speed: Float) {
        changeSettings(id) { it.copy(scrollSpeed = speed) }
        if (sessionWith(id) != null) mapper.scrollSpeed = speed
    }

    /** Saves whether the desk locks when the host's sessions end; a session with it takes it at once. */
    fun setLockAfter(id: String, lock: Boolean) {
        changeSettings(id) { it.copy(lockAfter = lock) }
        sessionWith(id)?.setLockAfter(lock)
    }

    /**
     * Saves whether the host's sessions stream the microphone; a session with
     * it starts or stops at once. The caller has the permission before
     * turning it on ([MicPolicy.toggle]).
     */
    fun setMic(id: String, on: Boolean) {
        changeSettings(id) { it.copy(mic = on) }
        sessionWith(id)?.setMic(on)
    }

    /** Shows or hides the link statistics overlay. */
    fun setShowStats(show: Boolean) {
        store.showStats = show
        _showStats.value = show
    }

    /**
     * Saves whether Connect first asks for a fingerprint or the device's PIN.
     * The caller asks before turning it off ([withAppLock]).
     */
    fun setAppLock(on: Boolean) {
        store.appLock = on
        _appLock.value = on
    }

    /** Saves a new host, with the settings of the host used last, selects it and checks it. */
    fun addHost(name: String, ip: String, port: Int) {
        val p = HostProfile(UUID.randomUUID().toString(), name.ifBlank { ip }, ip.trim(), port)
        store.startSettings(p.id, from = store.selectedHost)
        saveProfiles(_profiles.value + p)
        selectHost(p.id)
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

    /** Saves the host list; a host no longer in it loses its settings. */
    private fun saveProfiles(list: List<HostProfile>) {
        (_profiles.value.map { it.id } - list.map { it.id }.toSet()).forEach(store::forgetSettings)
        store.save(list)
        _profiles.value = list
        val known = _hostSettings.value
        _hostSettings.value = list.associate { it.id to (known[it.id] ?: store.settings(it.id)) }
    }

    private fun replace(p: HostProfile) = saveProfiles(_profiles.value.map { if (it.id == p.id) p else it })

    /** Checks every saved host. */
    fun checkAll() = _profiles.value.forEach(::checkHealth)

    /** Calls /health on a host and stores its MAC address for Wake-on-LAN. */
    fun checkHealth(p: HostProfile) {
        _health.update { it + (p.id to HostHealth.Checking) }
        viewModelScope.launch {
            val started = SystemClock.elapsedRealtime()
            val health = when (val result = probe(p)) {
                is Probe.Reached -> HostHealth.Online(
                    result.health.hostname, result.health.version, SystemClock.elapsedRealtime() - started,
                    ControlProtocol.advice(result.health.protocol),
                )
                is Probe.Refused -> HostHealth.Offline(
                    str(R.string.err_not_running, result.address), HostHealth.Why.REFUSED, address = result.address,
                )
                Probe.NoAnswer -> HostHealth.Offline(
                    str(R.string.err_no_address, p.candidates().joinToString()), HostHealth.Why.NO_ANSWER, p.candidates().size,
                )
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
        if (plan.isNew) {
            store.startSettings(plan.profile.id, from = store.selectedHost)
            saveProfiles(_profiles.value + plan.profile)
        } else {
            replace(plan.profile)
        }
        selectHost(plan.profile.id)
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
                // The merge may have kept an older entry of this machine: select that one.
                _profiles.value.firstOrNull { it.fingerprint.equals(result.fingerprint, ignoreCase = true) }
                    ?.let {
                        selectHost(it.id)
                        checkHealth(it)
                    }
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
     * until this network has its own. A level saved lower by an older version
     * is sent as it is: the host starts at most one level below the top.
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
        _health.update { it + (p.id to HostHealth.Offline(str(R.string.err_not_running, address), HostHealth.Why.REFUSED, address = address)) }
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
            _health.update { it + (p.id to HostHealth.Offline(str(R.string.msg_not_woken), HostHealth.Why.NOT_WOKEN, p.candidates().size)) }
            showMessage(str(R.string.msg_not_woken_named, p.name))
        }
        return null
    }

    private fun startSession(p: HostProfile, key: ByteArray) {
        val network = networkType()
        val settings = settingsOf(p.id)
        val s = StreamSession(getApplication(), p, key, settings.preset, codecOf(settings), startLevel(p.id, network), viewModelScope,
            playAudio = settings.sound, screen = settings.screen, scale = settings.scale, keepAwake = settings.keepAwake,
            lockAfter = settings.lockAfter, viewSize = viewSize ?: windowSize, monitor = store.mirrorMonitor(p.id),
            sharperColours = settings.sharperColours, exactText = settings.exactText,
            mic = MicPolicy.ask(settings.mic, micPermitted()), micPermitted = ::micPermitted,
            primary = settings.primary, deskOff = settings.deskOff,
            // A reconnect tries every address saved by then and saves the one that answers.
            reach = { host, avoid -> reach(_profiles.value.firstOrNull { it.id == host.id } ?: host, avoid) },
            onPointerShape = pointer::shape, onPointerAt = pointer::hostAt)
        // The hello's size, which the host measures relative motion in.
        (viewSize ?: windowSize ?: StreamSize(settings.preset.width, settings.preset.height)).let { pointer.start(it.width, it.height) }
        mapper.touchMode = settings.touch
        mapper.touchSensitivity = settings.trackpadSpeed
        mapper.scrollSpeed = settings.scrollSpeed
        currentProfile = p
        s.setForeground(foreground)
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
            viewModelScope.launch { s.hostClipboardImage.collect(::putImageOnClipboard) },
            viewModelScope.launch { s.notices.collect { showMessage(it) } },
            viewModelScope.launch { watchPanel(s) { settingsOf(p.id).preset.fps } },
            viewModelScope.launch {
                s.state.collect { st ->
                    val info = (st as? StreamSession.State.Streaming)?.info ?: return@collect
                    // The own screen is drawn 1:1 for the tablet; only a mirrored monitor zooms.
                    mapper.zoom.enabled = info.screen == ScreenMode.MIRROR
                    // Relative motion is measured on the client's screen, the top level's size.
                    info.levels.firstOrNull()?.let { pointer.screen(it.width, it.height) }
                    // Never more than one level below the top, so a bad moment does not blur later sessions.
                    levelToRemember(info)?.let { store.setLevel(p.id, network, it) }
                    // The tablet may have rotated while the session connected.
                    followView()
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
        _panel.value = null
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

    /**
     * Sends the Android clipboard to the host. With [onlyIfChanged], skips
     * text the host already has, an image already sent and an image that
     * came from the host. An image goes as PNG, and only to a host that takes
     * clipboard images; to an older host the clip goes as text, as before.
     */
    fun pushClipboard(onlyIfChanged: Boolean) {
        val s = _session.value ?: return
        val clip = clipboard().primaryClip?.takeIf { it.itemCount > 0 } ?: return
        val image = clipImage(clip)
        if (image != null && s.hostImageLimit > 0) return pushClipboardImage(s, image, onlyIfChanged)
        val text = clip.getItemAt(0)?.coerceToText(getApplication())?.toString() ?: return
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

    /** An image on the Android clipboard: its content URI, MIME type and what tells this copy from the next. */
    private class ClipImage(val uri: Uri, val mime: String, val key: String)

    private fun clipImage(clip: ClipData): ClipImage? {
        val uri = clip.getItemAt(0).uri ?: return null
        val desc = clip.description
        val mime = (0 until desc.mimeTypeCount).map(desc::getMimeType).firstOrNull { it.startsWith("image/") } ?: return null
        return ClipImage(uri, mime, "$uri|${desc.timestamp}")
    }

    private val fileAuthority get() = getApplication<Application>().packageName + FILE_AUTHORITY_SUFFIX

    private fun pushClipboardImage(s: StreamSession, image: ClipImage, onlyIfChanged: Boolean) {
        if (onlyIfChanged && (image.key == lastImageKey || image.uri.authority == fileAuthority)) return
        lastImageKey = image.key
        val limit = s.hostImageLimit
        viewModelScope.launch {
            val png = withContext(Dispatchers.IO) { readClipboardPng(image, limit) }
            val problem = when (png) {
                is ClipPng.Ok -> {
                    if (_session.value !== s) return@launch
                    if (withContext(Dispatchers.IO) { s.pushClipboardImage(png.bytes) }) return@launch
                    // Nothing went out, so the next focus may try again.
                    lastImageKey = null
                    str(R.string.msg_clipboard_image_busy)
                }
                ClipPng.TooLarge -> str(R.string.msg_clipboard_image_too_large, limit / (1024 * 1024))
                is ClipPng.Failed -> str(R.string.msg_clipboard_image_failed, png.message)
            }
            if (!onlyIfChanged) showMessage(problem)
        }
    }

    /** A clipboard image read as PNG, or why not. */
    private sealed interface ClipPng {
        class Ok(val bytes: ByteArray) : ClipPng

        data object TooLarge : ClipPng

        data class Failed(val message: String) : ClipPng
    }

    /**
     * Reads the clipboard image through the content resolver, which the
     * clipboard granted access to: a PNG as it is, any other image type
     * decoded and encoded as PNG. Larger than [limit], or with more pixels
     * than decoding can hold, it is too large.
     */
    private fun readClipboardPng(image: ClipImage, limit: Int): ClipPng = try {
        val resolver = getApplication<Application>().contentResolver
        val raw = if (image.mime == PNG_MIME) resolver.openInputStream(image.uri)?.use { readAtMost(it, limit + 1) } else null
        when {
            raw != null && raw.size > limit -> ClipPng.TooLarge
            raw != null && ClipboardImages.isPng(raw) -> ClipPng.Ok(raw)
            else -> {
                val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
                resolver.openInputStream(image.uri)?.use { BitmapFactory.decodeStream(it, null, bounds) }
                if (bounds.outWidth.toLong() * bounds.outHeight > MAX_CLIPBOARD_PIXELS) {
                    ClipPng.TooLarge
                } else {
                    val bitmap = resolver.openInputStream(image.uri)?.use { BitmapFactory.decodeStream(it) }
                    if (bitmap == null) {
                        ClipPng.Failed(str(R.string.err_not_an_image))
                    } else {
                        val out = java.io.ByteArrayOutputStream()
                        bitmap.compress(Bitmap.CompressFormat.PNG, PNG_QUALITY, out)
                        bitmap.recycle()
                        if (out.size() > limit) ClipPng.TooLarge else ClipPng.Ok(out.toByteArray())
                    }
                }
            }
        }
    } catch (e: Exception) {
        Log.w(TAG, "read the clipboard image", e)
        ClipPng.Failed(e.message ?: e.toString())
    }

    /**
     * Puts a PNG from the host on the Android clipboard. Apps paste images
     * from a content URI, so it is saved in the cache and offered through
     * the FileProvider, which lets the pasting app read it; the previous one
     * is deleted.
     */
    private suspend fun putImageOnClipboard(png: ByteArray) {
        val app = getApplication<Application>()
        val uri = withContext(Dispatchers.IO) {
            runCatching {
                val dir = java.io.File(app.cacheDir, CLIPBOARD_CACHE_DIR).apply { mkdirs() }
                dir.listFiles()?.forEach { it.delete() }
                val file = java.io.File(dir, "clipboard-${System.currentTimeMillis()}.png").apply { writeBytes(png) }
                FileProvider.getUriForFile(app, fileAuthority, file)
            }.onFailure { Log.w(TAG, "save the clipboard image from the host", it) }.getOrNull()
        } ?: return
        lastClipboard = null
        clipboard().setPrimaryClip(ClipData.newUri(app.contentResolver, "DeskLink", uri))
    }

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

    /**
     * Samples the panel every half second while [s] runs, for the stats,
     * and once the stream is on screen judges the rate it shows
     * ([PanelRateJudge]); while that is below [streamFps], the session's top
     * rate as its preset now has it, the session asks the host for at most
     * that rate. On the connect screen Samsung's adaptive refresh rests at
     * 60 Hz, so only a streaming session is judged.
     */
    private suspend fun watchPanel(s: StreamSession, streamFps: () -> Int) {
        val judge = PanelRateJudge()
        var logged: PanelState? = null
        while (true) {
            val p = panelWatch.sample()
            _panel.value = p
            if (p != null && p != logged) {
                Log.i(TAG, "panel: ${p.hz} Hz, highest ${p.maxHz} Hz, power saving ${if (p.powerSave) "on" else "off"}")
                logged = p
            }
            if (p != null && s.state.value is StreamSession.State.Streaming) {
                val shown = judge.sample(SystemClock.uptimeMillis(), p, lastTouchMs)
                val cap = PanelRateJudge.maxFps(shown, streamFps())
                if (cap != s.maxFps.value) {
                    Log.i(TAG, if (cap > 0) "panel shows $shown Hz: asking the host for at most $cap fps" else "panel shows $shown Hz: lifting the frame-rate cap")
                    s.setMaxFps(cap)
                }
            }
            delay(PANEL_SAMPLE_MS)
        }
    }

    /** The screen was touched at [uptimeMs] (SystemClock.uptimeMillis, the clock of MotionEvent times). */
    fun noteTouch(uptimeMs: Long) {
        lastTouchMs = uptimeMs
    }

    override fun onCleared() {
        disconnect()
        connectivity?.let { cm -> runCatching { cm.unregisterNetworkCallback(meteredWatch) } }
    }

    companion object {
        private const val TAG = "DeskLink"
        private const val WOL_PORT = 9

        /** How often the panel is sampled during a session. */
        private const val PANEL_SAMPLE_MS = 500L
        private const val SERVICE_TYPE = "_desklink._tcp"
        private const val WAKE_TIMEOUT_MS = 90_000L

        /** Asking a host to forget this tablet gives up after this; it is removed here anyway. */
        private const val FORGET_TIMEOUT_MS = 8_000L
        private const val WAKE_RESEND_MS = 10_000L
        private const val WAKE_POLL_MS = 2_000L

        /** How long the stream view keeps a new size before the own screen follows it. */
        private const val RESIZE_QUIET_MS = 800L
        private const val MAX_CLIPBOARD_BYTES = 256 * 1024

        /** The FileProvider's authority after the package name; see AndroidManifest.xml. */
        private const val FILE_AUTHORITY_SUFFIX = ".files"

        /** The cache directory res/xml/file_paths.xml shares, holding the last image from the host. */
        private const val CLIPBOARD_CACHE_DIR = "clipboard"
        private const val PNG_MIME = "image/png"

        /** PNG is lossless; the quality argument is ignored. */
        private const val PNG_QUALITY = 100

        /** More pixels than this would take too much memory to decode; such an image is too large anyway. */
        private const val MAX_CLIPBOARD_PIXELS = 16_000_000L

        /** How long the connection test waits for one address; connecting alone gives up after 3 s. */
        private const val TEST_TIMEOUT_MS = 5_000L
    }
}
