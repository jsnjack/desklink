package com.desklink.client.net

import android.content.Context
import android.util.Log
import com.desklink.client.R
import android.view.Surface
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
import com.desklink.client.model.SessionInfo
import com.desklink.client.model.StreamPreset
import com.desklink.client.video.HardwareDecoder
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
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
    private val startLevel: LevelInfo?,
    parent: CoroutineScope,
) {
    /** Where the session is: connecting, streaming, failed with a message, or closed. */
    sealed interface State {
        /** Waiting for /health, the WebSocket or the session message. */
        data object Connecting : State

        /** Video is flowing; [info] is the latest session description. */
        data class Streaming(val info: SessionInfo) : State

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

    /** Checks /health, then opens the authenticated WebSocket. */
    fun connect() {
        scope.launch {
            val fingerprint = profile.fingerprint ?: return@launch fail(context.getString(R.string.msg_not_paired))
            val peerId = profile.peerId ?: return@launch fail(context.getString(R.string.msg_not_paired))
            val client = ControlClient(context, profile.ip, profile.port, fingerprint)
            val health = runCatching { client.health() }.getOrElse {
                return@launch fail(context.getString(R.string.err_unreachable, profile.ip, profile.port, it.message ?: ""))
            }
            val token = SessionCrypto.authToken(peerId, peerKey, health.timeMs / 1000)
            control = client.openSession(token, listener)
        }
    }

    private val listener = object : ControlClient.Listener {
        override fun onOpen() {
            val hello = Hello(
                codec, preset.width, preset.height, preset.fps, preset.qp,
                auto = preset.auto, start = startLevel.takeIf { preset.auto },
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

        override fun onFailure(message: String) = fail(message)
    }

    @Synchronized
    private fun onSessionInfo(si: SessionInfo) {
        val previous = info
        info = si
        if (transport.value == null) {
            val nonce = B64.decode(si.hostNonce ?: return fail(context.getString(R.string.err_pair_protocol)))
            val keys = SessionCrypto.deriveSessionKeys(peerKey, si.sessionId, nonce)
            val t = UdpTransport(profile.ip, si.udpPort, si.sessionId, keys, { frame -> decoder.get()?.onFrame(frame) }, scope)
            transport.value = t
            t.start()
        }
        _state.value = State.Streaming(si)
        if (previous == null || previous.width != si.width || previous.height != si.height || previous.codec != si.codec) {
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
        _state.value = State.Failed(message)
        stopMedia()
    }

    @Synchronized
    private fun stopMedia() {
        decoder.getAndSet(null)?.release()
        transport.getAndSet(null)?.close()
    }

    /** Disconnects; the host restores the desktop. */
    fun close() {
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
    }
}
