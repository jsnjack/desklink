package com.desklink.client.net

import android.util.Log
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
    private val profile: HostProfile,
    private val peerKey: ByteArray,
    private val preset: StreamPreset,
    private val codec: String,
    parent: CoroutineScope,
) {
    sealed interface State {
        data object Connecting : State
        data class Streaming(val info: SessionInfo) : State
        data class Failed(val message: String) : State
        data object Closed : State
    }

    private val scope = CoroutineScope(parent.coroutineContext + SupervisorJob())
    private val _state = MutableStateFlow<State>(State.Connecting)
    val state: StateFlow<State> = _state

    private val _apps = MutableStateFlow<List<AppEntry>>(emptyList())
    val apps: StateFlow<List<AppEntry>> = _apps

    private val _hostClipboard = MutableSharedFlow<String>(extraBufferCapacity = 4)
    val hostClipboard: SharedFlow<String> = _hostClipboard

    private val _notices = MutableSharedFlow<String>(extraBufferCapacity = 4)
    val notices: SharedFlow<String> = _notices

    private val transport = MutableStateFlow<UdpTransport?>(null)

    @OptIn(ExperimentalCoroutinesApi::class)
    val stats: StateFlow<LinkStats> = transport
        .flatMapLatest { it?.stats ?: flowOf(LinkStats()) }
        .stateIn(scope, SharingStarted.Eagerly, LinkStats())

    private var control: ControlSession? = null
    private val decoder = AtomicReference<HardwareDecoder?>(null)
    private var surface: Surface? = null
    private var info: SessionInfo? = null

    fun connect() {
        scope.launch {
            val fingerprint = profile.fingerprint ?: return@launch fail("host is not paired")
            val peerId = profile.peerId ?: return@launch fail("host is not paired")
            val client = ControlClient(profile.ip, profile.port, fingerprint)
            val health = runCatching { client.health() }.getOrElse {
                return@launch fail("cannot reach ${profile.ip}:${profile.port}: ${it.message}")
            }
            val token = SessionCrypto.authToken(peerId, peerKey, health.timeMs / 1000)
            control = client.openSession(token, listener)
        }
    }

    private val listener = object : ControlClient.Listener {
        override fun onOpen() {
            send(MsgType.HELLO, ProtocolJson.encodeToJsonElement(Hello.serializer(), Hello(codec, preset.width, preset.height, preset.fps, preset.qp)))
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
            val nonce = B64.decode(si.hostNonce ?: return fail("host sent no session nonce"))
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

    /** Called by the SurfaceView callbacks. */
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
        val d = HardwareDecoder(s, si.codec, si.width, si.height) { transport.value?.requestKeyframe() }
        try {
            d.start()
        } catch (e: Exception) {
            Log.e(TAG, "decoder start failed", e)
            d.release()
            fail("cannot start the ${si.codec} decoder: ${e.message}")
            return
        }
        decoder.set(d)
        transport.value?.requestKeyframe()
    }

    fun sendInput(events: List<InputEvent>) {
        transport.value?.sendInput(*events.toTypedArray())
    }

    fun launch(app: AppEntry) =
        send(MsgType.LAUNCH, ProtocolJson.encodeToJsonElement(Launch.serializer(), Launch(app.id, app.exec)))

    fun pushClipboard(text: String) =
        send(MsgType.CLIPBOARD, ProtocolJson.encodeToJsonElement(ClipboardText.serializer(), ClipboardText(text)))

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
