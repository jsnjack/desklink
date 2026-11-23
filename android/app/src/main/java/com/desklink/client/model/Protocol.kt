package com.desklink.client.model

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonElement
import java.nio.ByteBuffer
import java.util.Base64

/**
 * Wire constants and codecs of the UDP media plane; host/internal/net/packet.go
 * is the reference and docs/protocol.md describes the layout.
 */
object Wire {
    // Header and size limits.
    const val MAGIC = 0x444C
    const val VERSION = 1
    const val HEADER_SIZE = 16
    const val MAX_DATAGRAM = 1280
    const val TAG_SIZE = 16
    const val MAX_PLAINTEXT = MAX_DATAGRAM - HEADER_SIZE - TAG_SIZE
    const val VIDEO_PREFIX = 12
    const val INPUT_EVENT_SIZE = 12
    const val TELEMETRY_SIZE = 5
    const val KEEPALIVE_SIZE = 8

    /** Input records that fit one datagram. */
    const val MAX_EVENTS_PER_PACKET = MAX_PLAINTEXT / INPUT_EVENT_SIZE

    // Packet types, header byte 3.
    const val TYPE_VIDEO = 0x01
    const val TYPE_INPUT = 0x02
    const val TYPE_TELEMETRY = 0x03
    const val TYPE_KEEPALIVE = 0x04

    /** FragIdx value marking a keepalive that echoes the other side's timestamp. */
    const val KEEPALIVE_ECHO = 1
    private const val VIDEO_FLAG_KEYFRAME = 0x01

    /** Decodes a decrypted video payload; null when the fragment indices are invalid. */
    fun parseVideo(h: Header, pt: ByteArray): VideoFragment? {
        if (pt.size < VIDEO_PREFIX) return null
        val index = ((pt[1].toInt() and 0xFF) shl 8) or h.fragIdx
        val count = ((pt[2].toInt() and 0xFF) shl 8) or h.fragCnt
        if (count == 0 || index >= count) return null
        return VideoFragment(
            frameId = h.frameId,
            index = index,
            count = count,
            keyframe = (pt[0].toInt() and VIDEO_FLAG_KEYFRAME) != 0,
            captureMicros = ByteBuffer.wrap(pt, 4, 8).long,
            data = pt,
            offset = VIDEO_PREFIX,
            length = pt.size - VIDEO_PREFIX,
        )
    }

    /** Encodes input records; the caller keeps them within [MAX_EVENTS_PER_PACKET]. */
    fun encodeInput(events: List<InputEvent>): ByteArray {
        val buf = ByteBuffer.allocate(events.size * INPUT_EVENT_SIZE)
        for (e in events) {
            buf.put(e.kind.toByte()).put(e.flags.toByte()).putShort(e.code.toShort()).putInt(e.x).putInt(e.y)
        }
        return buf.array()
    }

    /** Decodes an input payload, the inverse of [encodeInput]. */
    fun decodeInput(payload: ByteArray): List<InputEvent> {
        require(payload.isNotEmpty() && payload.size % INPUT_EVENT_SIZE == 0) { "bad input payload length ${payload.size}" }
        val buf = ByteBuffer.wrap(payload)
        return List(payload.size / INPUT_EVENT_SIZE) {
            InputEvent(
                kind = buf.get().toInt() and 0xFF,
                flags = buf.get().toInt() and 0xFF,
                code = buf.short.toInt() and 0xFFFF,
                x = buf.int,
                y = buf.int,
            )
        }
    }

    /** Encodes the 250 ms telemetry report. */
    fun encodeTelemetry(lastContiguousFrameId: Int, lossPctX100: Int, needIdr: Boolean): ByteArray =
        ByteBuffer.allocate(TELEMETRY_SIZE)
            .putShort(lastContiguousFrameId.toShort())
            .putShort(lossPctX100.coerceIn(0, 10_000).toShort())
            .put(if (needIdr) 1 else 0)
            .array()

    /** Encodes a keepalive carrying a Unix timestamp in microseconds. */
    fun encodeKeepalive(timestampMicros: Long): ByteArray =
        ByteBuffer.allocate(KEEPALIVE_SIZE).putLong(timestampMicros).array()

    /** The timestamp in a keepalive, or null if it is too short. */
    fun parseKeepalive(pt: ByteArray): Long? =
        if (pt.size < KEEPALIVE_SIZE) null else ByteBuffer.wrap(pt).long
}

/**
 * The 16-byte big-endian cleartext header, also the AEAD additional data.
 * [sessionId] and [seq] hold the unsigned 32-bit values' bit patterns.
 */
data class Header(
    val type: Int,
    val sessionId: Int,
    val seq: Int,
    val frameId: Int = 0,
    val fragIdx: Int = 0,
    val fragCnt: Int = 0,
) {
    /** Writes the header into [out] at [offset]. */
    fun write(out: ByteArray, offset: Int = 0) {
        ByteBuffer.wrap(out, offset, Wire.HEADER_SIZE)
            .putShort(Wire.MAGIC.toShort())
            .put(Wire.VERSION.toByte())
            .put(type.toByte())
            .putInt(sessionId)
            .putInt(seq)
            .putShort(frameId.toShort())
            .put(fragIdx.toByte())
            .put(fragCnt.toByte())
    }

    companion object {
        /** Parses a header, or returns null for a short packet or wrong magic. */
        fun parse(buf: ByteArray, length: Int): Header? {
            if (length < Wire.HEADER_SIZE) return null
            val b = ByteBuffer.wrap(buf, 0, Wire.HEADER_SIZE)
            if ((b.short.toInt() and 0xFFFF) != Wire.MAGIC || b.get().toInt() != Wire.VERSION) return null
            return Header(
                type = b.get().toInt() and 0xFF,
                sessionId = b.int,
                seq = b.int,
                frameId = b.short.toInt() and 0xFFFF,
                fragIdx = b.get().toInt() and 0xFF,
                fragCnt = b.get().toInt() and 0xFF,
            )
        }
    }
}

/** One decrypted slice of an encoded frame; [data] is shared, read [offset] and [length]. */
class VideoFragment(
    val frameId: Int,
    val index: Int,
    val count: Int,
    val keyframe: Boolean,
    val captureMicros: Long,
    val data: ByteArray,
    val offset: Int,
    val length: Int,
)

/** Input event kinds and flags; host/internal/input/uinput.go handles them. */
object InputKind {
    // Record kinds.
    const val KEY = 1
    const val REL_MOVE = 2
    const val ABS_MOVE = 3
    const val MOUSE_BUTTON = 4
    const val SCROLL = 5

    /**
     * A snapshot of what the client holds down, sent every 250 ms so the host
     * can lift keys whose up packet was lost. Flags name the item.
     */
    const val HELD = 7
    const val HELD_NONE = 0
    const val HELD_KEY = 1
    const val HELD_BUTTON = 2
    const val HELD_PEN = 3

    /** Bits of a [HELD_PEN] record's code. */
    const val PEN_HELD_TOUCH = 1
    const val PEN_HELD_IN_RANGE = 2

    // Pen flags of ABS_MOVE records.
    const val PEN_HOVER = 0
    const val PEN_TOUCH = 1
    const val PEN_OUT = 2

    // Mouse button codes.
    const val BUTTON_LEFT = 1
    const val BUTTON_RIGHT = 2
    const val BUTTON_MIDDLE = 3
    const val BUTTON_SIDE = 4
    const val BUTTON_EXTRA = 5

    /** Scroll values are in 1/120 of a wheel notch. */
    const val WHEEL_UNIT = 120
    const val ABS_MAX = 65535
}

/** One 12-byte input record; see [InputKind] for the meaning of each field. */
data class InputEvent(val kind: Int, val flags: Int = 0, val code: Int = 0, val x: Int = 0, val y: Int = 0) {
    /** Constructors for each record kind. */
    companion object {
        fun key(code: Int, down: Boolean) = InputEvent(InputKind.KEY, if (down) 1 else 0, code)
        fun move(dx: Int, dy: Int) = InputEvent(InputKind.REL_MOVE, x = dx, y = dy)
        fun button(button: Int, down: Boolean) = InputEvent(InputKind.MOUSE_BUTTON, if (down) 1 else 0, button)
        fun scroll(hwheel: Int, wheel: Int) = InputEvent(InputKind.SCROLL, x = hwheel, y = wheel)
        fun pen(flags: Int, x: Int, y: Int) = InputEvent(InputKind.ABS_MOVE, flags, 0, x, y)
    }
}

/** Control message types; host/internal/net/control.go is the reference. */
object MsgType {
    const val HELLO = "hello"
    const val SESSION = "session"
    const val APPS = "apps"
    const val LAUNCH = "launch"
    const val CLIPBOARD = "clipboard"
    const val CLIPBOARD_REQUEST = "clipboard_request"
    const val QUALITY = "quality"
    const val REQ_IDR = "req_idr"
    const val ERROR = "error"
}

/** JSON settings shared by every control message. */
val ProtocolJson = Json {
    ignoreUnknownKeys = true
    encodeDefaults = true
    explicitNulls = false
}

/** The `{"type", "payload"}` wrapper of every WebSocket message. */
@Serializable
data class Envelope(val type: String, val payload: JsonElement? = null)

/**
 * The first message: the stream the client wants. With [auto] the size and
 * rate are the top of the host's quality ladder and [start] is the level
 * last used on this network.
 */
@Serializable
data class Hello(
    val codec: String,
    val width: Int,
    val height: Int,
    val fps: Int,
    val qp: Int,
    val auto: Boolean = false,
    val start: LevelInfo? = null,
)

/** One rung of the auto-quality ladder. */
@Serializable
data class LevelInfo(val width: Int, val height: Int, val fps: Int)

/** The host's description of the stream and the UDP session. */
@Serializable
data class SessionInfo(
    @SerialName("session_id") val sessionId: Long,
    @SerialName("udp_port") val udpPort: Int,
    @SerialName("host_nonce") val hostNonce: String? = null,
    val codec: String,
    val width: Int,
    val height: Int,
    val fps: Int,
    val qp: Int,
    val display: String = "",
    val auto: Boolean = false,
    val level: Int = 0,
    val levels: List<LevelInfo> = emptyList(),
)

/** An app the host can launch. */
@Serializable
data class AppEntry(val id: String, val name: String, val exec: String)

/** Asks the host to start an app from its list. */
@Serializable
data class Launch(val id: String, val exec: String)

/** Clipboard text in either direction. */
@Serializable
data class ClipboardText(val text: String)

/** Changes the stream size or quantizer during a session. */
@Serializable
data class Quality(val width: Int, val height: Int, val qp: Int)

/** A host error; before the session message it ends the session. */
@Serializable
data class ErrorInfo(val message: String)

/** The unauthenticated /health reply; [timeMs] stamps bearer tokens in host time. */
@Serializable
data class Health(
    val name: String = "",
    val version: String = "",
    val hostname: String = "",
    @SerialName("time_ms") val timeMs: Long = 0,
    val mac: String = "",
)

/** The body of both POST /pair phases; binary fields are base64. */
@Serializable
data class PairRequest(
    val phase: String,
    @SerialName("client_pub") val clientPub: String? = null,
    @SerialName("pairing_id") val pairingId: String? = null,
    val proof: String? = null,
    val name: String? = null,
)

/** The reply to POST /pair. */
@Serializable
data class PairResponse(
    @SerialName("pairing_id") val pairingId: String? = null,
    @SerialName("host_pub") val hostPub: String? = null,
    @SerialName("peer_id") val peerId: String? = null,
    val proof: String? = null,
    val error: String? = null,
)

/** The JSON inside the pairing QR code printed by `desklinkd pair`. */
@Serializable
data class PairingQr(val ip: String, val port: Int, val pin: String, val fingerprint: String)

/** Go encodes []byte as standard padded base64. */
object B64 {
    /** Standard padded base64 of [b]. */
    fun encode(b: ByteArray): String = Base64.getEncoder().encodeToString(b)

    /** Decodes standard padded base64. */
    fun decode(s: String): ByteArray = Base64.getDecoder().decode(s)
}
