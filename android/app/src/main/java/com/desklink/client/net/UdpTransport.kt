package com.desklink.client.net

import android.util.Log
import com.desklink.client.crypto.PacketCipher
import com.desklink.client.crypto.SessionCrypto
import com.desklink.client.model.Header
import com.desklink.client.model.InputEvent
import com.desklink.client.model.VideoFragment
import com.desklink.client.model.Wire
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.InetSocketAddress
import java.net.SocketException
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicLong

/** A complete encoded frame, ready for the decoder. */
class EncodedFrame(val frameId: Int, val data: ByteArray, val keyframe: Boolean, val captureMicros: Long)

/** Receives frames in decode order. */
fun interface FrameSink {
    fun onFrame(frame: EncodedFrame)
}

/** Live numbers for the HUD. */
data class LinkStats(val rttMs: Int = 0, val fps: Int = 0, val mbps: Double = 0.0, val lossPct: Double = 0.0)

/**
 * The UDP side of a session: receives and reassembles video, reports
 * telemetry every 250 ms, exchanges keepalives every 2 s and sends input.
 */
class UdpTransport(
    host: String,
    port: Int,
    sessionId: Long,
    keys: SessionCrypto.SessionKeys,
    private val sink: FrameSink,
    private val scope: CoroutineScope,
) {
    private val remote = InetSocketAddress(host, port)
    private val socket = DatagramSocket().apply {
        receiveBufferSize = SOCKET_BUFFER
        connect(remote)
    }
    private val sid = sessionId.toInt()
    private val send = PacketCipher(keys.clientToHost)
    private val recv = PacketCipher(keys.hostToClient)
    private val seq = AtomicInteger(0)
    private val assembler = FrameAssembler()
    private val loss = LossMeter()
    private val bytes = AtomicLong(0)
    private val frames = AtomicInteger(0)
    private val rttMs = AtomicInteger(0)
    // Input is sent from the UI thread, where Android forbids network calls.
    private val sender = Executors.newSingleThreadExecutor { r -> Thread(r, "desklink-input").apply { priority = Thread.MAX_PRIORITY } }
    private val jobs = mutableListOf<Job>()

    private val _stats = MutableStateFlow(LinkStats())
    val stats: StateFlow<LinkStats> = _stats

    fun start() {
        jobs += scope.launch(Dispatchers.IO) { receiveLoop() }
        jobs += scope.launch(Dispatchers.IO) {
            while (isActive) {
                sendTelemetry()
                delay(TELEMETRY_INTERVAL_MS)
            }
        }
        jobs += scope.launch(Dispatchers.IO) {
            while (isActive) {
                sendPacket(Header(Wire.TYPE_KEEPALIVE, sid, nextSeq()), Wire.encodeKeepalive(nowMicros()))
                delay(KEEPALIVE_INTERVAL_MS)
            }
        }
        jobs += scope.launch(Dispatchers.Default) {
            while (isActive) {
                delay(STATS_INTERVAL_MS)
                val b = bytes.getAndSet(0)
                val f = frames.getAndSet(0)
                _stats.value = LinkStats(
                    rttMs = rttMs.get(),
                    fps = (f * 1000 / STATS_INTERVAL_MS).toInt(),
                    mbps = b * 8.0 / 1_000_000 * 1000 / STATS_INTERVAL_MS,
                    lossPct = _stats.value.lossPct,
                )
            }
        }
    }

    fun sendInput(vararg events: InputEvent) {
        if (events.isEmpty()) return
        val payload = Wire.encodeInput(events.toList())
        sender.execute { sendPacket(Header(Wire.TYPE_INPUT, sid, nextSeq()), payload) }
    }

    /** Asks for a keyframe now instead of waiting for the next telemetry report. */
    fun requestKeyframe() {
        assembler.forceKeyframe()
        sender.execute { sendTelemetry() }
    }

    fun close() {
        jobs.forEach { it.cancel() }
        sender.shutdownNow()
        socket.close()
    }

    private fun nextSeq(): Int = seq.incrementAndGet()

    private fun nowMicros(): Long = System.currentTimeMillis() * 1000

    private fun sendTelemetry() {
        val lossX100 = loss.sample()
        _stats.value = _stats.value.copy(lossPct = lossX100 / 100.0)
        val payload = Wire.encodeTelemetry(assembler.lastContiguousFrameId, lossX100, assembler.needKeyframe)
        sendPacket(Header(Wire.TYPE_TELEMETRY, sid, nextSeq()), payload)
    }

    private fun sendPacket(header: Header, payload: ByteArray) {
        try {
            val dg = send.seal(header, payload)
            socket.send(DatagramPacket(dg, dg.size))
        } catch (e: java.io.IOException) {
            Log.d(TAG, "send failed: ${e.message}")
        }
    }

    private fun receiveLoop() {
        val buf = ByteArray(2048)
        val packet = DatagramPacket(buf, buf.size)
        while (scope.isActive) {
            try {
                packet.length = buf.size
                socket.receive(packet)
            } catch (e: SocketException) {
                return
            } catch (e: java.io.IOException) {
                Log.d(TAG, "receive failed: ${e.message}")
                continue
            }
            val (header, pt) = recv.open(buf, packet.length) ?: continue
            if (header.sessionId != sid) continue
            bytes.addAndGet(packet.length.toLong())
            // Every host packet takes a sequence number, so all of them count.
            loss.onPacket(header.seq)
            when (header.type) {
                Wire.TYPE_VIDEO -> {
                    val fragment = Wire.parseVideo(header, pt) ?: continue
                    val wasWaiting = assembler.needKeyframe
                    for (frame in assembler.add(fragment)) {
                        frames.incrementAndGet()
                        sink.onFrame(frame)
                    }
                    if (assembler.needKeyframe && !wasWaiting) sendTelemetry()
                }
                Wire.TYPE_KEEPALIVE -> {
                    val ts = Wire.parseKeepalive(pt) ?: continue
                    if (header.fragIdx == Wire.KEEPALIVE_ECHO) {
                        rttMs.set(((nowMicros() - ts) / 1000).toInt().coerceAtLeast(0))
                    } else {
                        sendPacket(Header(Wire.TYPE_KEEPALIVE, sid, nextSeq(), fragIdx = Wire.KEEPALIVE_ECHO), Wire.encodeKeepalive(ts))
                    }
                }
            }
        }
    }

    companion object {
        private const val TAG = "DeskLink"
        private const val SOCKET_BUFFER = 4 shl 20
        const val TELEMETRY_INTERVAL_MS = 250L
        const val KEEPALIVE_INTERVAL_MS = 2000L
        private const val STATS_INTERVAL_MS = 1000L
    }
}

/**
 * Reassembles fragments into frames and releases them in frame order. A frame
 * still incomplete when a frame two IDs newer has arrived counts as lost; the
 * assembler then drops frames until a keyframe arrives, because frames that
 * reference a lost one would decode as garbage, and [needKeyframe] asks the
 * host for one.
 */
class FrameAssembler {
    private class Partial(val count: Int, val keyframe: Boolean, val captureMicros: Long) {
        val parts = arrayOfNulls<ByteArray>(count)
        var received = 0
        var size = 0
    }

    private val partial = HashMap<Int, Partial>()
    private val ready = HashMap<Int, EncodedFrame>()
    private var next: Int? = null
    private var newest: Int? = null

    @Volatile
    var needKeyframe = true
        private set

    /** The frame before the next one expected, reported in telemetry. */
    val lastContiguousFrameId: Int
        @Synchronized get() = ((next ?: 0) - 1) and 0xFFFF

    @Synchronized
    fun forceKeyframe() {
        needKeyframe = true
    }

    @Synchronized
    fun add(f: VideoFragment): List<EncodedFrame> {
        val expected = next
        if (expected != null && distance(f.frameId, expected) < 0) return emptyList()
        if (newest == null || distance(f.frameId, newest!!) > 0) newest = f.frameId
        if (ready.containsKey(f.frameId)) return emptyList()

        val p = partial.getOrPut(f.frameId) { Partial(f.count, f.keyframe, f.captureMicros) }
        if (f.count == p.count && p.parts[f.index] == null) {
            p.parts[f.index] = f.data.copyOfRange(f.offset, f.offset + f.length)
            p.received++
            p.size += f.length
        }
        if (p.received == p.count) {
            partial.remove(f.frameId)
            val data = ByteArray(p.size)
            var pos = 0
            for (part in p.parts) {
                System.arraycopy(part!!, 0, data, pos, part.size)
                pos += part.size
            }
            ready[f.frameId] = EncodedFrame(f.frameId, data, p.keyframe, p.captureMicros)
        }
        return drain()
    }

    private fun drain(): List<EncodedFrame> {
        val out = ArrayList<EncodedFrame>()
        while (true) {
            if (needKeyframe) {
                val key = ready.values.filter { it.keyframe }.minWithOrNull { a, b -> distance(a.frameId, b.frameId) }
                    ?: break
                discardBefore(key.frameId)
                needKeyframe = false
                next = key.frameId
            }
            val expected = next ?: break
            val frame = ready.remove(expected)
            if (frame != null) {
                out += frame
                next = (expected + 1) and 0xFFFF
                continue
            }
            val latest = newest ?: break
            if (distance(latest, expected) >= LOSS_DISTANCE) {
                needKeyframe = true
                continue
            }
            break
        }
        trim()
        return out
    }

    private fun discardBefore(frameId: Int) {
        ready.keys.removeAll { distance(it, frameId) < 0 }
        partial.keys.removeAll { distance(it, frameId) < 0 }
    }

    private fun trim() {
        if (partial.size + ready.size <= MAX_BUFFERED) return
        val latest = newest ?: return
        partial.keys.removeAll { distance(latest, it) > MAX_BUFFERED }
        ready.keys.removeAll { distance(latest, it) > MAX_BUFFERED }
    }

    companion object {
        /** An expected frame still incomplete when the newest frame is this many IDs ahead is lost. */
        const val LOSS_DISTANCE = 2
        private const val MAX_BUFFERED = 64

        /** Signed distance a - b on the 16-bit frame ID circle. */
        fun distance(a: Int, b: Int): Int = ((a - b + 0x8000) and 0xFFFF) - 0x8000
    }
}

/** Estimates video packet loss from gaps in the host's sequence numbers. */
class LossMeter {
    private var highest = 0L
    private var windowStart = -1L
    private var received = 0

    @Synchronized
    fun onPacket(seq: Int) {
        val s = seq.toLong() and 0xFFFFFFFFL
        if (windowStart < 0) windowStart = s - 1
        if (s > highest) highest = s
        received++
    }

    /** Loss since the previous sample in hundredths of a percent. */
    @Synchronized
    fun sample(): Int {
        if (windowStart < 0) return 0
        val expected = highest - windowStart
        val lost = (expected - received).coerceAtLeast(0)
        val result = if (expected <= 0) 0 else (lost * 10_000 / expected).toInt()
        windowStart = highest
        received = 0
        return result
    }
}
