package com.desklink.client.model

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonElement
import java.nio.ByteBuffer
import java.util.Base64
import kotlin.math.abs

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
    const val TYPE_AUDIO = 0x05
    const val TYPE_FEC = 0x06

    /** One Opus frame of this tablet's microphone, laid out like [TYPE_AUDIO]. */
    const val TYPE_MIC = 0x07

    /** Which exact tiles a frame changed, before and after the frame's fragments ([TileWire.parsePacket]). */
    const val TYPE_TILES = 0x08

    /** A UDP probe outside any session ([UdpProbe]). */
    const val TYPE_PROBE = 0x09

    /** A parity packet's group, stride, fragment count and XOR of payload lengths. */
    const val FEC_PREFIX = 8

    /** The capture timestamp before each Opus frame. */
    const val AUDIO_PREFIX = 8

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

    /** Decodes a decrypted parity payload; null when its group is invalid. */
    fun parseParity(h: Header, pt: ByteArray): ParityPacket? {
        if (pt.size < FEC_PREFIX) return null
        val b = ByteBuffer.wrap(pt)
        val group = b.short.toInt() and 0xFFFF
        val stride = b.short.toInt() and 0xFFFF
        val count = b.short.toInt() and 0xFFFF
        val lenXor = b.short.toInt() and 0xFFFF
        if (stride == 0 || group >= stride || count == 0) return null
        return ParityPacket(h.frameId, group, stride, count, lenXor, pt.copyOfRange(FEC_PREFIX, pt.size))
    }

    /** A fragment's video payload as the host sent it: the 12-byte prefix, then the data. */
    fun videoPayload(index: Int, count: Int, keyframe: Boolean, captureMicros: Long, data: ByteArray): ByteArray =
        ByteBuffer.allocate(VIDEO_PREFIX + data.size)
            .put(if (keyframe) VIDEO_FLAG_KEYFRAME.toByte() else 0)
            .put((index shr 8).toByte())
            .put((count shr 8).toByte())
            .put(0)
            .putLong(captureMicros)
            .put(data)
            .array()

    /** Decodes a decrypted audio payload; null when it holds no Opus frame. */
    fun parseAudio(h: Header, pt: ByteArray): AudioPacket? {
        if (pt.size <= AUDIO_PREFIX) return null
        return AudioPacket(h.frameId, ByteBuffer.wrap(pt, 0, AUDIO_PREFIX).long, pt.copyOfRange(AUDIO_PREFIX, pt.size))
    }

    /** The payload of an audio packet: the capture time, then the Opus frame. */
    fun encodeAudio(captureMicros: Long, frame: ByteArray): ByteArray =
        ByteBuffer.allocate(AUDIO_PREFIX + frame.size).putLong(captureMicros).put(frame).array()

    /** The header of microphone frame [counter] (16 bits, wrapping). */
    fun micHeader(sessionId: Int, seq: Int, counter: Int) = Header(TYPE_MIC, sessionId, seq, frameId = counter and 0xFFFF)

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
 * The XOR of the video payloads of fragments [group], group + [stride], ...
 * of a frame of [count] fragments, and the XOR of their payload lengths; see
 * host/internal/net/packet.go. Groups are strided, so a burst of up to
 * [stride] consecutive losses hits each group at most once.
 */
class ParityPacket(val frameId: Int, val group: Int, val stride: Int, val count: Int, val lenXor: Int, val data: ByteArray) {
    /** The fragment indices this packet covers. */
    val members: List<Int> get() = (group until count step stride).toList()

    /** The payload of the one member missing from [present], the other members' payloads. */
    fun recover(present: List<ByteArray>): ByteArray? {
        val buf = data.copyOf(maxOf(data.size, present.maxOfOrNull { it.size } ?: 0))
        var length = lenXor
        for (q in present) {
            for (i in q.indices) buf[i] = (buf[i].toInt() xor q[i].toInt()).toByte()
            length = length xor q.size
        }
        return if (length <= buf.size) buf.copyOf(length) else null
    }
}

/** One Opus frame; [counter] numbers audio packets (16 bits, wrapping). */
class AudioPacket(val counter: Int, val captureMicros: Long, val data: ByteArray)

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

    /** An absolute mouse position, x and y 0..[ABS_MAX] across the video frame. */
    const val ABS_POINTER = 6

    /**
     * A snapshot of what the client holds down, sent every 250 ms so the host
     * can lift keys whose up packet was lost. Flags name the item.
     */
    const val HELD = 7

    /**
     * The tilt of the pen record that follows: x towards +x and y towards +y
     * in hundredths of a degree (-9000..9000). Older hosts ignore it.
     */
    const val PEN_TILT = 8

    /** Full pen pressure; an ABS_MOVE record's code carries 1..this while touching. */
    const val PRESSURE_MAX = 65535

    /** Tilt limit in hundredths of a degree. */
    const val TILT_MAX = 9000
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
        /** The pen at [x], [y]; [pressure] is 1..PRESSURE_MAX while touching, 0 when unknown. */
        fun pen(flags: Int, x: Int, y: Int, pressure: Int = 0) = InputEvent(InputKind.ABS_MOVE, flags, pressure, x, y)
        fun penTilt(tiltX: Int, tiltY: Int) = InputEvent(InputKind.PEN_TILT, x = tiltX, y = tiltY)
        fun pointer(x: Int, y: Int) = InputEvent(InputKind.ABS_POINTER, x = x, y = y)
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
    const val SCALE = "scale"
    const val KEEP_AWAKE = "keep_awake"
    const val LOCK_AFTER = "lock_after"
    const val PRIMARY = "primary"
    const val DESK = "desk"
    const val MIRROR = "mirror"
    const val RESIZE = "resize"
    const val TYPE = "type"
    const val MIC = "mic"

    /** Caps the stream's frame rate at the panel's rate, or lifts the cap ([MaxFps]). */
    const val MAX_FPS = "max_fps"

    /** Turns exact tiles on or off ([TilesChange]); tile messages are binary ([TileWire]). */
    const val TILES = "tiles"
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
    /**
     * Keeps the quantizer, size and rate for the whole session, whatever the
     * connection does, and lets the quantizer go below the host's min_qp
     * (the Sharp preset). It overrides [auto].
     */
    val fixed: Boolean = false,
    /** Asks for the desktop's sound as Opus packets. */
    val audio: Boolean = false,
    /** What to show: [ScreenMode.AUTO], [ScreenMode.OWN] or [ScreenMode.MIRROR]. */
    val screen: String = ScreenMode.AUTO,
    /** The own screen's scale; 0 lets the host choose (200 %). */
    val scale: Double = 0.0,
    /** How long the computer stays awake after the session: a [KeepAwake] value. */
    @SerialName("keep_awake") val keepAwake: Int = KeepAwake.OFF,
    /** Lock the desk when the session ends, also when it was unlocked as the client came. */
    @SerialName("lock_after") val lockAfter: Boolean = false,
    /** The largest PNG, in bytes, this client takes in clipboard messages; 0 means text only. */
    @SerialName("clipboard_images") val clipboardImages: Int = 0,
    /**
     * The connector a mirroring session shows, the last one chosen for this
     * host; null lets the host choose, as it does when that monitor is off.
     */
    val monitor: String? = null,
    /** Asks for the desk's virtual microphone, which plays this tablet's microphone packets. */
    val mic: Boolean = false,
    /** Asks for exact tiles of the parts of the screen that stopped changing ([TileWire]). */
    val tiles: Boolean = false,
    /** Takes each frame's tile invalidation as [Wire.TYPE_TILES] packets with the video; hosts before them ignore it. */
    @SerialName("tiles_udp") val tilesUdp: Boolean = false,
)

/** Switches a mirroring session to another of [SessionInfo.monitors]. */
@Serializable
data class MirrorChange(val connector: String)

/** Asks for the own screen at a new size in physical pixels, as when the tablet rotates. */
@Serializable
data class Resize(val width: Int, val height: Int)

/** The sides of a stream or own screen a hello or resize may ask for, in pixels. */
object ScreenSide {
    const val MIN = 320
    const val MAX = 4096
}

/**
 * Turns exact tiles on or off during a session; turning them on while they
 * are on starts over, and the host sends every settled part again.
 */
@Serializable
data class TilesChange(val on: Boolean)

/** Asks for another scale of the client's own screen during a session. */
@Serializable
data class ScaleChange(val scale: Double)

/** Changes how long the computer stays awake after the session. */
@Serializable
data class KeepAwakeChange(val minutes: Int)

/** Sets whether the desk locks when the session ends. */
@Serializable
data class LockAfterChange(val lock: Boolean)

/** Makes the client's own screen GNOME's primary monitor, or gives the primary back to the desk. */
@Serializable
data class PrimaryChange(val primary: Boolean)

/** Turns the desk's monitors off, so every window moves to the own screen, or on again. */
@Serializable
data class DeskChange(val off: Boolean)

/** Turns the desk's virtual microphone on or off. */
@Serializable
data class MicChange(val on: Boolean)

/**
 * Caps the stream's frame rate at [fps], the rate the panel shows while
 * something holds it below its highest (power saving, say); 0 lifts the
 * cap. The host's quality ladder stays as the hello asked for it.
 */
@Serializable
data class MaxFps(val fps: Int)

/**
 * Text for the host to type as keysyms, in order with the keys sent before
 * and after it; only hosts whose [SessionInfo.keysyms] is set accept it.
 */
@Serializable
data class TypeText(val text: String) {
    companion object {
        /** The most characters (code points) one message may carry; longer text goes in several. */
        const val MAX_CHARS = 1000
    }
}

/**
 * How long the host keeps the computer from suspending after the session
 * ends, so a job left running goes on: off, minutes, or until a client
 * connects again. The desk still locks.
 */
object KeepAwake {
    const val OFF = 0
    const val UNTIL_RETURN = -1

    /** Suspend the computer when the session ends, unless a client reconnects within 20 s. */
    const val SUSPEND = -2
    const val ONE_HOUR = 60
    const val FOUR_HOURS = 240
}

/** The own screen's scale steps the HUD offers. */
object OwnScale {
    const val MIN = 1.0
    const val MAX = 2.0
    const val STEP = 0.25
    const val DEFAULT = 2.0

    /** The scale one step from [current] in [direction] (-1 or +1), within MIN..MAX. */
    fun step(current: Double, direction: Int): Double =
        ((Math.round(current / STEP) + direction) * STEP).coerceIn(MIN, MAX)

    /**
     * Whether [scale] keeps every app sharp: a whole scale (100 or 200 %).
     * At the others GNOME draws apps that run through XWayland at 100 % and
     * stretches them, which blurs their text.
     */
    fun isSharp(scale: Double): Boolean = abs(scale - Math.round(scale)) < 0.01
}

/** Values of `hello.screen` and `session.screen`. */
object ScreenMode {
    /** The client's own screen when the desk is locked, otherwise a mirrored monitor. */
    const val AUTO = "auto"

    /** A virtual monitor of the client's size. */
    const val OWN = "own"

    /** A physical monitor, unchanged. */
    const val MIRROR = "mirror"
}

/** One rung of the auto-quality ladder. */
@Serializable
data class LevelInfo(val width: Int, val height: Int, val fps: Int)

/** The lowest ladder level remembered for the next session: one below the top, which keeps the full size. */
const val MAX_REMEMBERED_LEVEL = 1

/**
 * The auto-quality level to remember from [info] for the next session on this
 * network and for reconnects, or null when the session is not on Auto. A level
 * further down is remembered as one below the top: the host starts no lower
 * anyway, and a level from a bad moment would otherwise blur every later session.
 */
fun levelToRemember(info: SessionInfo): LevelInfo? {
    if (!info.auto || info.level !in info.levels.indices) return null
    return info.levels[minOf(info.level, MAX_REMEMBERED_LEVEL)]
}

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
    /** Set when the host sends the desktop's sound. */
    val audio: Boolean = false,
    /** [ScreenMode.OWN] or [ScreenMode.MIRROR]: what the session shows. */
    val screen: String = "",
    /** The own screen's scale as applied; 0 when mirroring. */
    val scale: Double = 0.0,
    /** The session's keep-awake setting, as in [Hello.keepAwake]. */
    @SerialName("keep_awake") val keepAwake: Int = KeepAwake.OFF,
    /** The session's lock-after setting, as in [Hello.lockAfter]. */
    @SerialName("lock_after") val lockAfter: Boolean = false,
    /**
     * Whether the own screen is GNOME's primary monitor, with the top bar and
     * the overview; null when the session cannot move the primary (mirroring,
     * or a host from before the Primary screen switch).
     */
    val primary: Boolean? = null,
    /**
     * Whether the desk's monitors are off; null when the client cannot switch
     * them (mirroring, or a session that found the desk locked keeps it dark).
     */
    @SerialName("desk_off") val deskOff: Boolean? = null,
    /**
     * Whether the desk's virtual microphone plays this tablet's microphone;
     * null from hosts without one, so the HUD offers the switch only when present.
     */
    val mic: Boolean? = null,
    /**
     * Whether the host types [TypeText]: its screen cast belongs to a Mutter
     * remote desktop session. False from hosts before typing, which get text
     * as US-layout key taps instead.
     */
    val keysyms: Boolean = false,
    /**
     * The largest PNG, in bytes, the host takes in clipboard messages; 0 (also
     * from hosts before clipboard images) means text only, so the client
     * sends an image only when it fits this.
     */
    @SerialName("clipboard_images") val clipboardImages: Int = 0,
    /** Set while the host sends exact tiles; hosts before them leave it out. */
    val tiles: Boolean = false,
    /**
     * Set when each frame's tile invalidation comes as [Wire.TYPE_TILES]
     * packets instead of tile_frame messages; the session takes both.
     */
    @SerialName("tiles_udp") val tilesUdp: Boolean = false,
    /** The monitors that are on, which a mirroring session can show; the own screen is left out. */
    val monitors: List<MonitorInfo> = emptyList(),
    /** The connector of the monitor the session mirrors; empty for the own screen. */
    val mirroring: String = "",
)

/** A host monitor that is on: its connector, current mode in physical pixels and whether it is primary. */
@Serializable
data class MonitorInfo(
    val connector: String,
    val width: Int,
    val height: Int,
    val refresh: Double = 0.0,
    val primary: Boolean = false,
)

/** An app the host can launch. */
@Serializable
data class AppEntry(val id: String, val name: String, val exec: String)

/** Asks the host to start an app from its list. */
@Serializable
data class Launch(val id: String, val exec: String)

/**
 * Clipboard text in either direction, or one part of a PNG image
 * ([ClipboardImages.parts]): [image] is the part's bytes in standard base64,
 * [part] counts from 0 and [parts] is how many the image has. The host omits
 * `part` when it is 0.
 */
@Serializable
data class ClipboardText(
    val text: String = "",
    val image: String? = null,
    val part: Int = 0,
    val parts: Int = 0,
) {
    /** Whether the message carries part of an image rather than text. */
    val isImage: Boolean get() = parts > 0
}

/**
 * Clipboard images: a PNG goes as several clipboard messages of at most
 * [PART_BYTES] each, so the WebSocket's pings and pongs get through between
 * them; one 10 MB message on a slow link would hold them back until the
 * connection was given up. host/internal/net/clipimage.go is the reference.
 */
object ClipboardImages {
    /** The most PNG bytes one clipboard message carries. */
    const val PART_BYTES = 512 * 1024

    /** The largest image this client takes, as `hello.clipboard_images` says. */
    const val DEFAULT_LIMIT = 8 * 1024 * 1024

    private val PNG_SIGNATURE = byteArrayOf(0x89.toByte(), 'P'.code.toByte(), 'N'.code.toByte(), 'G'.code.toByte(), 0x0D, 0x0A, 0x1A, 0x0A)

    /** Whether [data] starts with the PNG signature. */
    fun isPng(data: ByteArray): Boolean =
        data.size >= PNG_SIGNATURE.size && PNG_SIGNATURE.indices.all { data[it] == PNG_SIGNATURE[it] }

    /** The clipboard messages that carry [png], in the order they are sent. */
    fun parts(png: ByteArray): List<ClipboardText> {
        val n = (png.size + PART_BYTES - 1) / PART_BYTES
        return List(n) { i ->
            val chunk = png.copyOfRange(i * PART_BYTES, minOf((i + 1) * PART_BYTES, png.size))
            ClipboardText(image = B64.encode(chunk), part = i, parts = n)
        }
    }
}

/** What [ImageAssembler.add] made of one part. */
sealed interface ImagePart {
    /** The image needs more parts. */
    data object Pending : ImagePart

    /** The part completed [png]. */
    class Done(val png: ByteArray) : ImagePart

    /** The image this part belongs to was dropped, for [reason]. */
    data class Dropped(val reason: String) : ImagePart
}

/**
 * Joins the parts of clipboard images into PNGs of at most [limit] bytes,
 * like the host's ImageAssembler. The WebSocket delivers messages in order,
 * so the parts of an image arrive one after the other; anything else drops
 * the image, and the assembler waits for the next part 0.
 */
class ImageAssembler(private val limit: Int) {
    private var buf = java.io.ByteArrayOutputStream()
    private var next = 0
    private var parts = 0

    /** Takes one image part. */
    @Synchronized
    fun add(c: ClipboardText): ImagePart {
        val maxParts = (limit + ClipboardImages.PART_BYTES - 1) / ClipboardImages.PART_BYTES
        if (c.part == 0) {
            reset()
            parts = c.parts
        }
        val bytes = runCatching { B64.decode(c.image.orEmpty()) }.getOrNull()
        val dropped = when {
            parts == 0 -> "part ${c.part} without its first part"
            c.parts != parts || c.part != next -> "part ${c.part} of ${c.parts} out of order"
            bytes == null -> "part ${c.part} is not base64"
            c.parts > maxParts || buf.size() + bytes.size > limit -> "larger than $limit bytes"
            else -> null
        }
        if (dropped != null || bytes == null) {
            reset()
            return ImagePart.Dropped(dropped ?: "")
        }
        buf.write(bytes)
        next++
        if (next < parts) return ImagePart.Pending
        val png = buf.toByteArray()
        reset()
        return if (ClipboardImages.isPng(png)) ImagePart.Done(png) else ImagePart.Dropped("not a PNG")
    }

    private fun reset() {
        buf = java.io.ByteArrayOutputStream()
        next = 0
        parts = 0
    }
}

/** The host's reply to `POST /files`: the name it saved the file under, or why it failed. */
@Serializable
data class UploadReply(val name: String = "", val size: Long = 0, val error: String? = null)

/** Changes the stream size or quantizer during a session. */
@Serializable
data class Quality(val width: Int, val height: Int, val qp: Int)

/** A host error; before the session message it ends the session. */
@Serializable
data class ErrorInfo(
    val message: String,
    /** Names an error the client acts on, such as [ErrorReason.REPLACED]; empty for the others. */
    val reason: String = "",
)

/** [ErrorInfo.reason] values. */
object ErrorReason {
    /**
     * Another device took the session over. The client must not reconnect by
     * itself: that would take the desk back, and two devices that both
     * reconnect would take it from each other in turn.
     */
    const val REPLACED = "replaced"
}

/** The unauthenticated /health reply; [timeMs] stamps bearer tokens in host time. */
@Serializable
data class Health(
    val name: String = "",
    val version: String = "",
    val hostname: String = "",
    @SerialName("time_ms") val timeMs: Long = 0,
    val mac: String = "",
    /** Every address the host may be reached on, LAN and VPN. */
    val addresses: List<String> = emptyList(),
    /** The host's [ControlProtocol] version; hosts before it existed leave it out, which means 1. */
    val protocol: Int = 1,
    /** The media port, for a [UdpProbe] before a session; 0 from older hosts. */
    @SerialName("udp_port") val udpPort: Int = 0,
)

/**
 * The set of control messages and fields this app speaks, as the host's
 * ControlProtocolVersion numbers it. An older host or app lacks some
 * features, which only hide, so the user is told which side to update.
 */
object ControlProtocol {
    const val VERSION = 3

    /** Which side should be updated, for a host speaking [host], or null when both match. */
    fun advice(host: Int): Update? = when {
        host < VERSION -> Update.HOST
        host > VERSION -> Update.APP
        else -> null
    }

    enum class Update { HOST, APP }
}

/**
 * The UDP probe: whether the host's media port answers from an address,
 * before any session, authenticated with the pairing key. Mirrors
 * host/internal/net/probe.go: the request is the header (type 0x09, session
 * 0), the device ID's length and bytes, a 16-byte nonce and
 * HMAC-SHA256(key, "desklink-udp-probe" | id | nonce); the reply is the
 * header, the nonce and HMAC-SHA256(key, "desklink-udp-probe-reply" | nonce).
 */
object UdpProbe {
    const val NONCE_SIZE = 16
    private const val MAC_SIZE = 32
    const val REPLY_SIZE = Wire.HEADER_SIZE + NONCE_SIZE + MAC_SIZE

    fun request(id: String, key: ByteArray, nonce: ByteArray): ByteArray {
        val idBytes = id.toByteArray(Charsets.US_ASCII)
        require(idBytes.size in 1..64 && nonce.size == NONCE_SIZE)
        val out = ByteArray(Wire.HEADER_SIZE + 1 + idBytes.size + NONCE_SIZE + MAC_SIZE)
        Header(Wire.TYPE_PROBE, 0, 0).write(out)
        out[Wire.HEADER_SIZE] = idBytes.size.toByte()
        idBytes.copyInto(out, Wire.HEADER_SIZE + 1)
        nonce.copyInto(out, Wire.HEADER_SIZE + 1 + idBytes.size)
        mac(key, "desklink-udp-probe", idBytes, nonce).copyInto(out, Wire.HEADER_SIZE + 1 + idBytes.size + NONCE_SIZE)
        return out
    }

    /** Whether [reply] is the host's answer to the request with [nonce], made with [key]. */
    fun checkReply(reply: ByteArray, length: Int, key: ByteArray, nonce: ByteArray): Boolean {
        val h = Header.parse(reply, length) ?: return false
        if (h.type != Wire.TYPE_PROBE || length != REPLY_SIZE) return false
        val gotNonce = reply.copyOfRange(Wire.HEADER_SIZE, Wire.HEADER_SIZE + NONCE_SIZE)
        val gotMac = reply.copyOfRange(Wire.HEADER_SIZE + NONCE_SIZE, REPLY_SIZE)
        return java.security.MessageDigest.isEqual(gotNonce, nonce) &&
            java.security.MessageDigest.isEqual(gotMac, mac(key, "desklink-udp-probe-reply", nonce))
    }

    private fun mac(key: ByteArray, label: String, vararg parts: ByteArray): ByteArray {
        val m = javax.crypto.Mac.getInstance("HmacSHA256")
        m.init(javax.crypto.spec.SecretKeySpec(key, "HmacSHA256"))
        m.update(label.toByteArray(Charsets.US_ASCII))
        parts.forEach(m::update)
        return m.doFinal()
    }
}

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

/**
 * The JSON inside the pairing QR code printed by `desklinkd pair`. A missing
 * [fingerprint] reads as empty so [PairingCode.parse] can refuse the code
 * with its own message.
 */
@Serializable
data class PairingQr(
    val ip: String,
    val port: Int,
    val pin: String,
    val fingerprint: String = "",
    /** The host's other addresses (other networks, VPNs). */
    val addresses: List<String> = emptyList(),
)

/** Go encodes []byte as standard padded base64. */
object B64 {
    /** Standard padded base64 of [b]. */
    fun encode(b: ByteArray): String = Base64.getEncoder().encodeToString(b)

    /** Decodes standard padded base64. */
    fun decode(s: String): ByteArray = Base64.getDecoder().decode(s)
}
