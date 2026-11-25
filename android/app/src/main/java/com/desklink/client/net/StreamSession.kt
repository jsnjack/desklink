package com.desklink.client.net

import android.content.Context
import android.util.Log
import com.desklink.client.R
import android.view.Surface
import com.desklink.client.audio.AudioPlayer
import com.desklink.client.crypto.SessionCrypto
import com.desklink.client.model.AppEntry
import com.desklink.client.model.B64
import com.desklink.client.model.ClipboardText
import com.desklink.client.model.Envelope
import com.desklink.client.model.ErrorInfo
import com.desklink.client.model.ErrorReason
import com.desklink.client.model.Hello
import com.desklink.client.model.Health
import com.desklink.client.model.HostProfile
import com.desklink.client.model.InputEvent
import com.desklink.client.model.Launch
import com.desklink.client.model.LevelInfo
import com.desklink.client.model.MsgType
import com.desklink.client.model.ProtocolJson
import com.desklink.client.model.ScreenMode
import com.desklink.client.model.SessionInfo
import com.desklink.client.model.StreamPreset
import com.desklink.client.video.HardwareDecoder
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.emptyFlow
import kotlinx.coroutines.flow.flatMapLatest
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.flow.runningFold
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.launch
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.JsonObject
import java.io.IOException
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.atomic.AtomicReference
import com.desklink.client.model.ScaleChange
import com.desklink.client.model.KeepAwakeChange
import com.desklink.client.model.DeskChange
import com.desklink.client.model.PrimaryChange

/**
 * One connection to a host: the control WebSocket, the UDP transport and the
 * decoder. The stream counts as up once the host's first UDP datagram
 * arrives. The decoder exists while both the session description and the
 * SurfaceView's surface are available.
 */
class StreamSession(
    private val context: Context,
    profile: HostProfile,
    private val peerKey: ByteArray,
    private val preset: StreamPreset,
    private val codec: String,
    /** The auto-quality level to start at, from the last session on this network. */
    private var startLevel: LevelInfo?,
    parent: CoroutineScope,
    /** Whether to ask for the desktop's sound. */
    private val audio: Boolean = false,
    /** What to show: a [ScreenMode] value. */
    private val screen: String = ScreenMode.AUTO,
    /** The own screen's scale to ask for; 0 lets the host choose. */
    scale: Double = 0.0,
    /** How long the computer stays awake after the session: a KeepAwake value. */
    keepAwake: Int = 0,
    /**
     * Asks every known address of a host for /health with the pinned
     * certificate and returns the host reached at the first that answers,
     * saved with the addresses it reports; null when none answers. Reconnects
     * use it, because the tablet may have moved to another network. An
     * address in the set given is used only when no other answers.
     */
    private val reach: suspend (HostProfile, Set<String>) -> Pair<HostProfile, Health>?,
) {
    /** The host's name, for the HUD. */
    val hostName: String = profile.name

    private val hellos = HelloBuilder(codec, preset, audio, screen, scale, keepAwake)

    /** Where the session is: connecting, streaming, failed with a message, or closed. */
    sealed interface State {
        /** Waiting for /health, the WebSocket, the session message or the first UDP datagram. */
        data object Connecting : State

        /**
         * The host's datagrams arrive over UDP, so video can flow; [info] is
         * the latest session description.
         */
        data class Streaming(val info: SessionInfo) : State

        /** The connection dropped and the session is connecting again. */
        data class Reconnecting(val attempt: Int, val of: Int) : State

        /** The session ended with an error to show. */
        data class Failed(val message: String) : State

        /** The session ended normally. */
        data object Closed : State
    }

    /**
     * One connection attempt: the address it reached and, once open, its
     * WebSocket. Callbacks of an attempt that is no longer [link] are ignored.
     */
    private class Link(val ip: String) {
        @Volatile
        var control: ControlSession? = null
    }

    private val scope = CoroutineScope(parent.coroutineContext + SupervisorJob())
    private val _state = MutableStateFlow<State>(State.Connecting)
    /** The session state. */
    val state: StateFlow<State> = _state

    private val _apps = MutableStateFlow<List<AppEntry>>(emptyList())
    /** The host's app drawer, pinned apps first. */
    val apps: StateFlow<List<AppEntry>> = _apps

    private val _hostClipboard = MutableSharedFlow<String>(extraBufferCapacity = 4)
    /** Clipboard text from the host. */
    val hostClipboard: SharedFlow<String> = _hostClipboard

    private val _notices = MutableSharedFlow<String>(extraBufferCapacity = 4)
    /** Host errors that do not end the session, such as a failed launch. */
    val notices: SharedFlow<String> = _notices

    private val transport = MutableStateFlow<UdpTransport?>(null)

    /** Link numbers for the HUD. */
    @OptIn(ExperimentalCoroutinesApi::class)
    val stats: StateFlow<LinkStats> = transport
        .flatMapLatest { it?.stats ?: flowOf(LinkStats()) }
        .stateIn(scope, SharingStarted.Eagerly, LinkStats())

    /** The last minute of [stats], a sample a second, for the HUD's charts; it spans reconnects. */
    @OptIn(ExperimentalCoroutinesApi::class)
    val history: StateFlow<StatsHistory> = transport
        .flatMapLatest { it?.samples ?: emptyFlow() }
        .runningFold(StatsHistory()) { history, sample -> history + sample }
        .stateIn(scope, SharingStarted.Eagerly, StatsHistory())

    /** The host as last reached; a reconnect may find it at another address. */
    @Volatile
    private var host: HostProfile = profile

    /** The current connection attempt; null between reconnect attempts. */
    @Volatile
    private var link: Link? = null

    // WebSockets of attempts given up while the session reconnects. They stay
    // open until a new connection has replaced them on the host, which then
    // keeps the desktop as it is instead of ending the session in between.
    private val retired = mutableListOf<ControlSession>()

    private val decoder = AtomicReference<HardwareDecoder?>(null)
    private var surface: Surface? = null
    private var info: SessionInfo? = null

    @Volatile
    private var everStreamed = false

    // Addresses where this session got the WebSocket but no UDP datagram, and
    // why the last one failed. A LAN address can answer HTTPS first while a
    // firewall drops its UDP that a slower VPN address carries, so reconnects
    // try the host's other addresses before these.
    private val mediaFailed: MutableSet<String> = ConcurrentHashMap.newKeySet()

    @Volatile
    private var mediaFailure: String? = null
    private var receivedBefore = 0L
    private var audioPlayer: AudioPlayer? = null

    @Volatile
    private var closing = false

    @Volatile
    private var attempt: CompletableDeferred<Boolean>? = null
    private var reconnectJob: Job? = null

    /** Checks /health, then opens the authenticated WebSocket. */
    fun connect() {
        scope.launch { open(again = false) }
    }

    /**
     * Opens one connection. The first goes to the address Connect reached;
     * a reconnect asks every known address again ([reach]) and uses the one
     * that answers for both the WebSocket and UDP. A failure before the
     * WebSocket ends the session unless it is reconnecting.
     */
    private suspend fun open(again: Boolean) {
        val fingerprint = host.fingerprint ?: return fail(context.getString(R.string.msg_not_paired))
        val peerId = host.peerId ?: return fail(context.getString(R.string.msg_not_paired))
        val from = host
        val (target, health) = try {
            if (again) {
                reach(from, mediaFailed.toSet()) ?: throw IOException(context.getString(R.string.err_no_address, from.candidates().joinToString()))
            } else {
                from to ControlClient(context, from.ip, from.port, fingerprint).health()
            }
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            if (closing) return
            val message = context.getString(R.string.err_unreachable, from.ip, from.port, e.message ?: "")
            if (attempt?.complete(false) == null) fail(message)
            return
        }
        // Before the stream ever worked, an address whose UDP failed comes back
        // only when no other answers, and it would fail the same way again.
        if (again && !everStreamed && target.ip in mediaFailed) {
            return fail(mediaFailure ?: context.getString(R.string.err_no_media, target.ip, target.port))
        }
        val token = SessionCrypto.authToken(peerId, peerKey, health.timeMs / 1000)
        val l = Link(target.ip)
        synchronized(this) {
            if (closing) return
            retire()
            host = target
            link = l
        }
        ControlClient(context, target.ip, target.port, fingerprint).openSession(token, listenerFor(l))
    }

    /**
     * Connects again after the link dropped, as when the tablet changes
     * networks, the host restarts its daemon or UDP stops arriving, keeping
     * the stream screen up.
     */
    @Synchronized
    private fun reconnect(reason: String) {
        retire()
        stopMedia()
        if (reconnectJob?.isActive == true) return
        reconnectJob = scope.launch {
            for ((i, wait) in RECONNECT_DELAYS_MS.withIndex()) {
                _state.value = State.Reconnecting(i + 1, RECONNECT_DELAYS_MS.size)
                delay(wait)
                if (closing) return@launch
                val a = CompletableDeferred<Boolean>()
                attempt = a
                open(again = true)
                val ok = a.await()
                attempt = null
                if (ok) return@launch
                retire()
                stopMedia()
            }
            fail(context.getString(R.string.err_connection_lost, reason))
        }
    }

    // Stops listening to the current attempt; its open WebSocket closes once
    // another has taken over or the session ends.
    @Synchronized
    private fun retire() {
        val l = link ?: return
        link = null
        l.control?.let(retired::add)
    }

    @Synchronized
    private fun closeRetired() {
        retired.forEach { it.close() }
        retired.clear()
    }

    private fun listenerFor(l: Link) = object : ControlClient.Listener {
        private val current get() = link === l && !closing

        override fun onOpen(session: ControlSession) {
            // Hello goes out on the socket that opened, and only while its
            // attempt is current; a late one from an attempt given up closes.
            val mine = synchronized(this@StreamSession) {
                (link === l && !closing).also { ok ->
                    if (ok) {
                        l.control = session
                        session.send(MsgType.HELLO, hello())
                    }
                }
            }
            if (!mine) session.close()
        }

        override fun onMessage(envelope: Envelope) {
            if (current) onEnvelope(l, envelope)
        }

        override fun onClosed(reason: String) {
            if (!current) return
            if (attempt?.complete(false) != null) return
            if (_state.value !is State.Failed) _state.value = State.Closed
            stopMedia()
        }

        override fun onFailure(message: String, retryable: Boolean) {
            if (!current) return
            when {
                attempt != null -> attempt?.complete(false)
                retryable && everStreamed && !closing -> reconnect(message)
                else -> fail(message)
            }
        }
    }

    private fun hello() = ProtocolJson.encodeToJsonElement(Hello.serializer(), hellos.build(startLevel))

    private fun onEnvelope(l: Link, envelope: Envelope) {
        val payload = envelope.payload ?: JsonObject(emptyMap())
        when (envelope.type) {
            MsgType.SESSION -> onSessionInfo(l, ProtocolJson.decodeFromJsonElement(SessionInfo.serializer(), payload))
            MsgType.APPS -> _apps.value = ProtocolJson.decodeFromJsonElement(ListSerializer(AppEntry.serializer()), payload)
            MsgType.CLIPBOARD -> _hostClipboard.tryEmit(ProtocolJson.decodeFromJsonElement(ClipboardText.serializer(), payload).text)
            MsgType.ERROR -> {
                val error = ProtocolJson.decodeFromJsonElement(ErrorInfo.serializer(), payload)
                when {
                    error.reason == ErrorReason.REPLACED -> endTakenOver(error.message)
                    _state.value is State.Streaming -> _notices.tryEmit(error.message)
                    else -> fail(error.message)
                }
            }
        }
    }

    /**
     * Takes a session description. The first of a connection starts the UDP
     * transport; the stream shows once the host's first datagram arrived
     * ([mediaReady]), so a blocked UDP path never looks like a frozen stream.
     */
    @Synchronized
    private fun onSessionInfo(l: Link, si: SessionInfo) {
        if (link !== l) return
        val previous = info
        info = si
        val t = transport.value ?: run {
            val nonce = B64.decode(si.hostNonce ?: return fail(context.getString(R.string.err_pair_protocol)))
            startTransport(l, si, nonce)
        }
        if (t.ready) _state.value = State.Streaming(si)
        // A reconnect starts where auto quality has settled.
        si.levels.getOrNull(si.level)?.takeIf { si.auto }?.let { startLevel = it }
        if (previous == null || decoder.get() == null || previous.width != si.width || previous.height != si.height || previous.codec != si.codec) {
            restartDecoder()
        }
    }

    private fun startTransport(l: Link, si: SessionInfo, nonce: ByteArray): UdpTransport {
        val keys = SessionCrypto.deriveSessionKeys(peerKey, si.sessionId, nonce)
        val player = if (si.audio) AudioPlayer().also { it.start() } else null
        audioPlayer = player
        lateinit var t: UdpTransport
        t = UdpTransport(
            l.ip, si.udpPort, si.sessionId, keys, { frame -> decoder.get()?.onFrame(frame) }, scope, receivedBefore,
            audioSink = { p -> player?.onPacket(p) },
            onReady = { mediaReady(t) },
            onLost = { ever -> mediaLost(t, l, si.udpPort, ever) },
        )
        transport.value = t
        t.start()
        return t
    }

    /** The host's first datagram arrived: UDP works both ways, so the stream is up. */
    @Synchronized
    private fun mediaReady(t: UdpTransport) {
        if (transport.value !== t) return
        val si = info ?: return
        _state.value = State.Streaming(si)
        everStreamed = true
        link?.ip?.let(mediaFailed::remove)
        attempt?.complete(true)
        // The host has taken this connection's hello, so it already replaced
        // the sessions of earlier attempts; their sockets can go.
        closeRetired()
    }

    /**
     * No datagram from the host in time. A stream that worked reconnects; a
     * reconnect attempt counts as failed and the next one runs; a first
     * connection fails with the reason, since UDP is most likely blocked.
     */
    private fun mediaLost(t: UdpTransport, l: Link, port: Int, everArrived: Boolean) {
        synchronized(this) {
            if (transport.value !== t || closing) return
        }
        // Lift held keys in case only the host's direction is lost.
        t.releaseHeld()
        val reason = if (everArrived) {
            context.getString(R.string.err_media_lost)
        } else {
            context.getString(R.string.err_no_media, l.ip, port)
        }
        Log.w(TAG, "media path: $reason")
        if (!everArrived) {
            mediaFailed += l.ip
            mediaFailure = reason
        }
        when {
            attempt != null -> attempt?.complete(false)
            everStreamed -> reconnect(reason)
            // UDP never worked at this address; another of the host's may carry it.
            host.candidates().any { it !in mediaFailed } -> reconnect(reason)
            else -> fail(reason)
        }
    }

    /** Gives the session the SurfaceView's surface, or null when it is destroyed. */
    @Synchronized
    fun attachSurface(s: Surface?) {
        surface = s
        restartDecoder()
    }

    @Synchronized
    private fun restartDecoder() {
        // Releasing waits for the old codec, so the new one never competes
        // with it for the hardware decoder.
        decoder.getAndSet(null)?.release()
        val s = surface ?: return
        val si = info ?: return
        lateinit var d: HardwareDecoder
        d = HardwareDecoder(
            s, si.codec, si.width, si.height,
            onNeedKeyframe = { transport.value?.requestKeyframe() },
            onFatal = { scope.launch { replaceFailedDecoder(d) } },
            onShown = { transport.value?.frameShown(it) },
        )
        try {
            d.start()
        } catch (e: Exception) {
            Log.e(TAG, "decoder start failed", e)
            d.release()
            fail(context.getString(R.string.err_decoder, si.codec, e.message ?: ""))
            return
        }
        decoder.set(d)
        transport.value?.requestKeyframe()
    }

    @Synchronized
    private fun replaceFailedDecoder(failed: HardwareDecoder) {
        if (decoder.get() === failed) restartDecoder()
    }

    /** Sends input over UDP. */
    fun sendInput(events: List<InputEvent>) {
        transport.value?.sendInput(*events.toTypedArray())
    }

    /**
     * Releases every key, button and pen contact this tablet holds on the
     * host, after the input already queued, for when keys stop reaching the
     * session: the window lost focus or the HUD took the keyboard.
     */
    fun releaseInput() {
        transport.value?.releaseHeld()
    }

    /** Asks the host to start an app. */
    fun launch(app: AppEntry) =
        send(MsgType.LAUNCH, ProtocolJson.encodeToJsonElement(Launch.serializer(), Launch(app.id, app.exec)))

    /** Sends text to the host clipboard. */
    fun pushClipboard(text: String) =
        send(MsgType.CLIPBOARD, ProtocolJson.encodeToJsonElement(ClipboardText.serializer(), ClipboardText(text)))

    /**
     * Asks the host to change the own screen's scale; the next session info
     * shows it applied, and a reconnect asks for it again.
     */
    fun setScale(scale: Double) {
        hellos.scale = scale
        send(MsgType.SCALE, ProtocolJson.encodeToJsonElement(ScaleChange.serializer(), ScaleChange(scale)))
    }

    /**
     * Sets how long the computer stays awake after this session; the next
     * session info shows it, and a reconnect asks for it again.
     */
    fun setKeepAwake(minutes: Int) {
        hellos.keepAwake = minutes
        send(MsgType.KEEP_AWAKE, ProtocolJson.encodeToJsonElement(KeepAwakeChange.serializer(), KeepAwakeChange(minutes)))
    }

    /**
     * Asks the host to make the own screen GNOME's primary monitor, or to give
     * the primary back to the desk; the next session info shows it applied.
     */
    fun setPrimary(primary: Boolean) =
        send(MsgType.PRIMARY, ProtocolJson.encodeToJsonElement(PrimaryChange.serializer(), PrimaryChange(primary)))

    /**
     * Asks the host to turn the desk's monitors off, so every window moves to
     * the own screen, or on again; the next session info shows it applied.
     */
    fun setDeskOff(off: Boolean) =
        send(MsgType.DESK, ProtocolJson.encodeToJsonElement(DeskChange.serializer(), DeskChange(off)))

    /** Asks the host for its clipboard. */
    fun requestClipboard() = send(MsgType.CLIPBOARD_REQUEST, JsonObject(emptyMap()))

    private fun send(type: String, payload: kotlinx.serialization.json.JsonElement) {
        link?.control?.send(type, payload)
    }

    /**
     * Another device took the session over: the session ends with [message]
     * and does not reconnect when the host then closes the connection, which
     * would take the desk back from that device.
     */
    private fun endTakenOver(message: String) {
        closing = true
        reconnectJob?.cancel()
        fail(message)
    }

    private fun fail(message: String) {
        Log.w(TAG, "session failed: $message")
        // A pending reconnect attempt ends the retry loop with this failure.
        attempt?.cancel()
        _state.value = State.Failed(message)
        stopMedia()
    }

    @Synchronized
    private fun stopMedia() {
        decoder.getAndSet(null)?.release()
        transport.getAndSet(null)?.let {
            receivedBefore = it.stats.value.receivedBytes
            it.close()
        }
        audioPlayer?.release()
        audioPlayer = null
    }

    /** Disconnects; the host restores the desktop. */
    fun close() {
        closing = true
        reconnectJob?.cancel()
        synchronized(this) {
            link?.control?.close()
            closeRetired()
        }
        stopMedia()
        if (_state.value !is State.Failed) _state.value = State.Closed
        scope.cancel()
    }

    private fun <T> MutableStateFlow<T?>.getAndSet(v: T?): T? {
        val old = value
        value = v
        return old
    }

    companion object {
        private const val TAG = "DeskLink"

        /** Waits before each reconnect attempt: about a minute in all. */
        private val RECONNECT_DELAYS_MS = listOf(500L, 1000L, 2000L, 3000L, 5000L, 5000L, 8000L, 10000L, 10000L, 15000L)
    }
}

/**
 * Builds the hello of each connection attempt. The HUD changes the own
 * screen's scale and keep awake during a session; a reconnect's hello must
 * ask for those, not for the values the session started with, or the host
 * goes back to the old ones.
 */
internal class HelloBuilder(
    private val codec: String,
    private val preset: StreamPreset,
    private val audio: Boolean,
    private val screen: String,
    scale: Double,
    keepAwake: Int,
) {
    @Volatile var scale: Double = scale

    @Volatile var keepAwake: Int = keepAwake

    /** The hello to send, starting auto quality at [start] when known. */
    fun build(start: LevelInfo?) = Hello(
        codec, preset.width, preset.height, preset.fps, preset.qp,
        auto = preset.auto, start = start.takeIf { preset.auto }, audio = audio, screen = screen, scale = scale, keepAwake = keepAwake,
    )
}

/**
 * Picks the address a reconnect uses from /health answers as they arrive: the
 * first that answers, unless UDP failed there earlier in the session ([avoid]);
 * such an address is used only when no other answers ([last]).
 */
internal class AddressChoice<T>(private val avoid: Set<String>) {
    private var fallback: T? = null

    /** Takes [addr]'s answer [r]; returns it when it settles the choice at once. */
    @Synchronized
    fun answered(addr: String, r: T): T? {
        if (addr !in avoid) return r
        if (fallback == null) fallback = r
        return null
    }

    /** The answer to use once every address has answered or failed: an avoided one, or null. */
    @Synchronized
    fun last(): T? = fallback
}
