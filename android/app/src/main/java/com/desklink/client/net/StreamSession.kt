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
import com.desklink.client.model.Hello
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
import kotlinx.coroutines.flow.flatMapLatest
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.launch
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.JsonObject
import java.util.concurrent.atomic.AtomicReference

/**
 * One connection to a host: the control WebSocket, the UDP transport and the
 * decoder. The decoder exists while both the session description and the
 * SurfaceView's surface are available.
 */
class StreamSession(
    private val context: Context,
    private val profile: HostProfile,
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
) {
    /** Where the session is: connecting, streaming, failed with a message, or closed. */
    sealed interface State {
        /** Waiting for /health, the WebSocket or the session message. */
        data object Connecting : State

        /** Video is flowing; [info] is the latest session description. */
        data class Streaming(val info: SessionInfo) : State

        /** The connection dropped and the session is connecting again. */
        data class Reconnecting(val attempt: Int, val of: Int) : State

        /** The session ended with an error to show. */
        data class Failed(val message: String) : State

        /** The session ended normally. */
        data object Closed : State
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

    private var control: ControlSession? = null
    private val decoder = AtomicReference<HardwareDecoder?>(null)
    private var surface: Surface? = null
    private var info: SessionInfo? = null

    // Each connection attempt has a generation; callbacks from an older
    // WebSocket (one that already failed) are ignored.
    @Volatile
    private var generation = 0

    @Volatile
    private var everStreamed = false
    private var receivedBefore = 0L
    private var audioPlayer: AudioPlayer? = null

    @Volatile
    private var closing = false

    @Volatile
    private var attempt: CompletableDeferred<Boolean>? = null
    private var reconnectJob: Job? = null

    /** Checks /health, then opens the authenticated WebSocket. */
    fun connect() {
        scope.launch { open() }
    }

    /** Opens one connection; a failure before the WebSocket ends the session unless it is reconnecting. */
    private suspend fun open() {
        val failed = { message: String -> attempt?.complete(false) ?: fail(message) }
        val fingerprint = profile.fingerprint ?: return fail(context.getString(R.string.msg_not_paired))
        val peerId = profile.peerId ?: return fail(context.getString(R.string.msg_not_paired))
        val client = ControlClient(context, profile.ip, profile.port, fingerprint)
        val health = runCatching { client.health() }.getOrElse {
            failed(context.getString(R.string.err_unreachable, profile.ip, profile.port, it.message ?: ""))
            return
        }
        val token = SessionCrypto.authToken(peerId, peerKey, health.timeMs / 1000)
        if (closing) return
        control = client.openSession(token, listenerFor(++generation))
    }

    /**
     * Connects again after the link dropped, as when the tablet changes
     * networks or the host restarts its daemon, keeping the stream screen up.
     */
    private fun reconnect(reason: String) {
        stopMedia()
        if (reconnectJob?.isActive == true) return
        reconnectJob = scope.launch {
            for ((i, wait) in RECONNECT_DELAYS_MS.withIndex()) {
                _state.value = State.Reconnecting(i + 1, RECONNECT_DELAYS_MS.size)
                delay(wait)
                if (closing) return@launch
                val a = CompletableDeferred<Boolean>()
                attempt = a
                open()
                val ok = a.await()
                attempt = null
                if (ok) return@launch
            }
            fail(context.getString(R.string.err_connection_lost, reason))
        }
    }

    private fun listenerFor(gen: Int) = object : ControlClient.Listener {
        private val current get() = gen == generation

        override fun onOpen() {
            if (current) listener.onOpen()
        }

        override fun onMessage(envelope: Envelope) {
            if (current) listener.onMessage(envelope)
        }

        override fun onClosed(reason: String) {
            if (!current) return
            attempt?.complete(false) ?: listener.onClosed(reason)
        }

        override fun onFailure(message: String, retryable: Boolean) {
            if (!current) return
            when {
                attempt != null -> attempt?.complete(false)
                retryable && everStreamed && !closing -> reconnect(message)
                else -> listener.onFailure(message, retryable)
            }
        }
    }

    private val listener = object : ControlClient.Listener {
        override fun onOpen() {
            val hello = Hello(
                codec, preset.width, preset.height, preset.fps, preset.qp,
                auto = preset.auto, start = startLevel.takeIf { preset.auto }, audio = audio, screen = screen,
            )
            send(MsgType.HELLO, ProtocolJson.encodeToJsonElement(Hello.serializer(), hello))
        }

        override fun onMessage(envelope: Envelope) {
            val payload = envelope.payload ?: JsonObject(emptyMap())
            when (envelope.type) {
                MsgType.SESSION -> onSessionInfo(ProtocolJson.decodeFromJsonElement(SessionInfo.serializer(), payload))
                MsgType.APPS -> _apps.value = ProtocolJson.decodeFromJsonElement(ListSerializer(AppEntry.serializer()), payload)
                MsgType.CLIPBOARD -> _hostClipboard.tryEmit(ProtocolJson.decodeFromJsonElement(ClipboardText.serializer(), payload).text)
                MsgType.ERROR -> {
                    val msg = ProtocolJson.decodeFromJsonElement(ErrorInfo.serializer(), payload).message
                    if (_state.value is State.Streaming) _notices.tryEmit(msg) else fail(msg)
                }
            }
        }

        override fun onClosed(reason: String) {
            if (_state.value !is State.Failed) _state.value = State.Closed
            stopMedia()
        }

        override fun onFailure(message: String, retryable: Boolean) = fail(message)
    }

    @Synchronized
    private fun onSessionInfo(si: SessionInfo) {
        val previous = info
        info = si
        if (transport.value == null) {
            val nonce = B64.decode(si.hostNonce ?: return fail(context.getString(R.string.err_pair_protocol)))
            val keys = SessionCrypto.deriveSessionKeys(peerKey, si.sessionId, nonce)
            val player = if (si.audio) AudioPlayer().also { it.start() } else null
            audioPlayer = player
            val t = UdpTransport(
                profile.ip, si.udpPort, si.sessionId, keys, { frame -> decoder.get()?.onFrame(frame) }, scope, receivedBefore,
                audioSink = { p -> player?.onPacket(p) },
            )
            transport.value = t
            t.start()
        }
        _state.value = State.Streaming(si)
        everStreamed = true
        // A reconnect starts where auto quality has settled.
        si.levels.getOrNull(si.level)?.takeIf { si.auto }?.let { startLevel = it }
        attempt?.complete(true)
        if (previous == null || decoder.get() == null || previous.width != si.width || previous.height != si.height || previous.codec != si.codec) {
            restartDecoder()
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
        decoder.getAndSet(null)?.release()
        val s = surface ?: return
        val si = info ?: return
        lateinit var d: HardwareDecoder
        d = HardwareDecoder(
            s, si.codec, si.width, si.height,
            onNeedKeyframe = { transport.value?.requestKeyframe() },
            onFatal = { scope.launch { replaceFailedDecoder(d) } },
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

    /** Asks the host to start an app. */
    fun launch(app: AppEntry) =
        send(MsgType.LAUNCH, ProtocolJson.encodeToJsonElement(Launch.serializer(), Launch(app.id, app.exec)))

    /** Sends text to the host clipboard. */
    fun pushClipboard(text: String) =
        send(MsgType.CLIPBOARD, ProtocolJson.encodeToJsonElement(ClipboardText.serializer(), ClipboardText(text)))

    /** Asks the host for its clipboard. */
    fun requestClipboard() = send(MsgType.CLIPBOARD_REQUEST, JsonObject(emptyMap()))

    private fun send(type: String, payload: kotlinx.serialization.json.JsonElement) {
        control?.send(type, payload)
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
        control?.close()
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
