package com.desklink.client.net

import android.util.Log
import com.desklink.client.crypto.PacketCipher
import com.desklink.client.crypto.SessionCrypto
import com.desklink.client.model.AudioPacket
import com.desklink.client.model.ParityPacket
import com.desklink.client.model.Header
import com.desklink.client.model.InputEvent
import com.desklink.client.model.InputKind
import com.desklink.client.model.VideoFragment
import com.desklink.client.model.Wire
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.channels.BufferOverflow
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.flow.updateAndGet
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.InetSocketAddress
import java.net.SocketException
import java.util.concurrent.Executors
import java.util.concurrent.RejectedExecutionException
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicLong

/** A complete encoded frame, ready for the decoder. */
class EncodedFrame(val frameId: Int, val data: ByteArray, val keyframe: Boolean, val captureMicros: Long)

/** Receives frames in decode order. */
fun interface FrameSink {
    /** Called on the UDP receive thread for every complete frame. */
    fun onFrame(frame: EncodedFrame)
}

/** Live numbers for the HUD. */
data class LinkStats(
    val rttMs: Int = 0,
    val fps: Int = 0,
    val mbps: Double = 0.0,
    val lossPct: Double = 0.0,
    /** Bytes received since the session started, across reconnects. */
    val receivedBytes: Long = 0,
    /**
     * Average time from the host capturing a frame to the decoder handing it
     * to the screen, over the last second with frames; 0 until known.
     */
    val delayMs: Int = 0,
) {
    /** How usable the link is, from round-trip time and loss. */
    val quality: LinkQuality
        get() = when {
            lossPct >= 5 || rttMs >= 150 -> LinkQuality.POOR
            lossPct >= 2 || rttMs >= 80 -> LinkQuality.FAIR
            lossPct >= 0.5 || rttMs >= 30 -> LinkQuality.GOOD
            else -> LinkQuality.EXCELLENT
        }
}

/** A grade for the overlay; thresholds match where the host starts adapting (2 % loss). */
enum class LinkQuality { EXCELLENT, GOOD, FAIR, POOR }

/**
 * The UDP side of a session: receives and reassembles video, reports
 * telemetry every 250 ms, exchanges keepalives every 2 s and sends input.
 * It reports once when the first authenticated host datagram arrives and
 * once when none arrived in time ([MediaWatchdog]).
 */
class UdpTransport(
    host: String,
    port: Int,
    sessionId: Long,
    keys: SessionCrypto.SessionKeys,
    private val sink: FrameSink,
    private val scope: CoroutineScope,
    /** Bytes the session received before this transport, so totals survive reconnects. */
    private val receivedBefore: Long = 0,
    /** Receives audio packets on the network thread. */
    private val audioSink: (AudioPacket) -> Unit = {},
    /** Called once, on the network thread, when the first authenticated host datagram arrives. */
    private val onReady: () -> Unit = {},
    /**
     * Called once when the host went silent: no authenticated datagram within
     * [MediaWatchdog.FIRST_PACKET_MS] of starting ([everArrived] false) or
     * for [MediaWatchdog.SILENCE_MS] after the last one ([everArrived] true).
     */
    private val onLost: (everArrived: Boolean) -> Unit = {},
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
    private val totalBytes = AtomicLong(0)
    private val frames = AtomicInteger(0)
    private val rttMs = AtomicInteger(0)
    private val clock = HostClock()
    private val shown = DelayMeter()
    // Input is sent from the UI thread, where Android forbids network calls.
    private val sender = Executors.newSingleThreadExecutor { r -> Thread(r, "desklink-input").apply { priority = Thread.MAX_PRIORITY } }
    private val jobs = mutableListOf<Job>()

    private val held = HeldInput()
    private val replay = ReplayWindow()
    private val watchdog = MediaWatchdog(nowMs())
    private val firstArrived = AtomicBoolean(false)

    @Volatile
    private var closed = false

    private val _stats = MutableStateFlow(LinkStats())

    /**
     * Link numbers: everything every second, and the loss again with every
     * telemetry report (every 250 ms).
     */
    val stats: StateFlow<LinkStats> = _stats

    private val _samples = MutableSharedFlow<LinkStats>(extraBufferCapacity = 8, onBufferOverflow = BufferOverflow.DROP_OLDEST)

    /** One [LinkStats] a second, for the HUD's charts; the loss-only updates between are left out. */
    val samples: SharedFlow<LinkStats> = _samples

    /** Whether an authenticated host datagram has arrived: the media path works both ways. */
    val ready: Boolean get() = firstArrived.get()

    /** Starts receiving, telemetry, keepalives and the receive deadline. */
    fun start() {
        jobs += scope.launch(Dispatchers.IO) { receiveLoop() }
        jobs += scope.launch(Dispatchers.Default) {
            while (isActive) {
                delay(MediaWatchdog.CHECK_INTERVAL_MS)
                when (watchdog.check(nowMs())) {
                    MediaWatchdog.Verdict.NEVER_ARRIVED -> return@launch onLost(false)
                    MediaWatchdog.Verdict.LOST -> return@launch onLost(true)
                    else -> Unit
                }
            }
        }
        jobs += scope.launch(Dispatchers.IO) {
            while (isActive) {
                sendTelemetry()
                // On the sender thread, after every input queued before it.
                submit { sendPacket(Header(Wire.TYPE_INPUT, sid, nextSeq()), Wire.encodeInput(held.snapshot())) }
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
                val rtt = rttMs.get()
                val received = receivedBefore + totalBytes.get()
                val delayMs = shown.take()?.let { (it / 1000).toInt() }
                // An atomic update: the telemetry loop sets lossPct from another thread.
                val next = _stats.updateAndGet {
                    LinkStats(
                        rttMs = rtt,
                        fps = (f * 1000 / STATS_INTERVAL_MS).toInt(),
                        mbps = b * 8.0 / 1_000_000 * 1000 / STATS_INTERVAL_MS,
                        lossPct = it.lossPct,
                        receivedBytes = received,
                        delayMs = delayMs ?: it.delayMs,
                    )
                }
                _samples.tryEmit(next)
            }
        }
    }

    /** Sends input, split into as many datagrams as needed, in order. */
    fun sendInput(vararg events: InputEvent) {
        if (events.isEmpty()) return
        val list = events.toList()
        submit {
            list.forEach(held::observe)
            for (chunk in list.chunked(Wire.MAX_EVENTS_PER_PACKET)) {
                sendPacket(Header(Wire.TYPE_INPUT, sid, nextSeq()), Wire.encodeInput(chunk))
            }
        }
    }

    /**
     * Releases every key, button and pen contact the outgoing input holds
     * down and sends the then empty held snapshot, on the sender thread after
     * all input queued before it, for when keys stop reaching the session
     * (focus lost, the HUD took the keyboard) and their releases never come.
     */
    fun releaseHeld() = submit {
        for (chunk in held.releaseAll().chunked(Wire.MAX_EVENTS_PER_PACKET)) {
            sendPacket(Header(Wire.TYPE_INPUT, sid, nextSeq()), Wire.encodeInput(chunk))
        }
        sendPacket(Header(Wire.TYPE_INPUT, sid, nextSeq()), Wire.encodeInput(held.snapshot()))
    }

    /** Asks for a keyframe now instead of waiting for the next telemetry report. */
    fun requestKeyframe() {
        assembler.forceKeyframe()
        submit { sendTelemetry() }
    }

    /**
     * Stops all work; later calls to send are ignored. Sends already queued,
     * such as a [releaseHeld], still go out before the socket closes.
     */
    fun close() {
        closed = true
        jobs.forEach { it.cancel() }
        try {
            sender.execute { socket.close() }
            sender.shutdown()
        } catch (e: RejectedExecutionException) {
            socket.close()
        }
    }

    private fun submit(task: () -> Unit) {
        if (closed) return
        try {
            sender.execute(task)
        } catch (e: RejectedExecutionException) {
            Log.d(TAG, "transport closed, dropping a send")
        }
    }

    private fun nextSeq(): Int = seq.incrementAndGet()

    private fun nowMicros(): Long = System.currentTimeMillis() * 1000

    // Monotonic, for the receive deadline.
    private fun nowMs(): Long = System.nanoTime() / 1_000_000

    /** Records that the frame the host captured at [captureMicros] (its clock) is being shown now. */
    fun frameShown(captureMicros: Long) {
        clock.sinceMicros(captureMicros, nowMicros())?.let(shown::add)
    }

    private fun sendTelemetry() {
        val lossX100 = loss.sample()
        _stats.update { it.copy(lossPct = lossX100 / 100.0) }
        val payload = Wire.encodeTelemetry(assembler.lastContiguousFrameId, lossX100, assembler.needKeyframe)
        sendPacket(Header(Wire.TYPE_TELEMETRY, sid, nextSeq()), payload)
    }

    private fun sendPacket(header: Header, payload: ByteArray) {
        try {
            val dg = send.seal(header, payload)
            socket.send(DatagramPacket(dg, dg.size))
        } catch (e: java.io.IOException) {
            Log.d(TAG, "send failed: ${e.message}")
        } catch (e: RuntimeException) {
            // A bad packet must not kill the sender thread and with it the app.
            Log.w(TAG, "send failed", e)
        }
    }

    private fun receiveLoop() {
        val buf = ByteArray(2048)
        val packet = DatagramPacket(buf, buf.size)
        while (scope.isActive && !closed) {
            try {
                packet.length = buf.size
                socket.receive(packet)
            } catch (e: SocketException) {
                return
            } catch (e: java.io.IOException) {
                Log.d(TAG, "receive failed: ${e.message}")
                continue
            }
            if (closed) return
            val (header, pt) = recv.open(buf, packet.length) ?: continue
            if (header.sessionId != sid) continue
            // A repeated or very old datagram is dropped before it can count
            // as received, skew loss and clock or feed the assembler again.
            if (!replay.accept(header.seq)) continue
            watchdog.onPacket(nowMs())
            if (firstArrived.compareAndSet(false, true)) onReady()
            bytes.addAndGet(packet.length.toLong())
            totalBytes.addAndGet(packet.length.toLong())
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
                Wire.TYPE_FEC -> {
                    val parity = Wire.parseParity(header, pt) ?: continue
                    val wasWaiting = assembler.needKeyframe
                    for (frame in assembler.addParity(parity)) {
                        frames.incrementAndGet()
                        sink.onFrame(frame)
                    }
                    if (assembler.needKeyframe && !wasWaiting) sendTelemetry()
                }
                Wire.TYPE_AUDIO -> Wire.parseAudio(header, pt)?.let(audioSink)
                Wire.TYPE_KEEPALIVE -> {
                    val ts = Wire.parseKeepalive(pt) ?: continue
                    if (header.fragIdx == Wire.KEEPALIVE_ECHO) {
                        val rtt = (nowMicros() - ts).coerceAtLeast(0)
                        rttMs.set((rtt / 1000).toInt())
                        clock.roundTrip(rtt)
                    } else {
                        clock.keepalive(ts, nowMicros())
                        sendPacket(Header(Wire.TYPE_KEEPALIVE, sid, nextSeq(), fragIdx = Wire.KEEPALIVE_ECHO), Wire.encodeKeepalive(ts))
                    }
                }
            }
        }
    }

    companion object {
        private const val TAG = "DeskLink"
        private const val SOCKET_BUFFER = 4 shl 20
        private const val TELEMETRY_INTERVAL_MS = 250L
        private const val KEEPALIVE_INTERVAL_MS = 2000L
        private const val STATS_INTERVAL_MS = 1000L
    }
}

/**
 * Accepts each host sequence number once and rejects numbers more than
 * [SIZE] behind the newest one seen: the host's replayWindow
 * (host/internal/net/udp.go) for the other direction. The host numbers its
 * datagrams from 1 and ends the session before the 32-bit number wraps.
 * Used by the receive thread only.
 */
class ReplayWindow {
    private var top = 0L
    private val seen = LongArray(SIZE / 64)

    /** Whether [seq] (an unsigned 32-bit value's bit pattern) is new; records it if so. */
    fun accept(seq: Int): Boolean {
        val s = seq.toLong() and 0xFFFFFFFFL
        if (s == 0L) return false
        if (s > top) {
            if (s - top >= SIZE) {
                seen.fill(0)
            } else {
                for (skipped in top + 1 until s) clear(skipped)
            }
            top = s
            mark(s)
            return true
        }
        if (top - s >= SIZE) return false
        if (seen[word(s)] and mask(s) != 0L) return false
        mark(s)
        return true
    }

    private fun word(s: Long) = ((s % SIZE) / 64).toInt()

    private fun mask(s: Long) = 1L shl (s % 64).toInt()

    private fun mark(s: Long) {
        seen[word(s)] = seen[word(s)] or mask(s)
    }

    private fun clear(s: Long) {
        seen[word(s)] = seen[word(s)] and mask(s).inv()
    }

    companion object {
        /** How far behind the newest number a datagram may arrive; the host uses the same. */
        const val SIZE = 1024
    }
}

/**
 * Decides from the arrival of authenticated host datagrams whether the UDP
 * path works. The host echoes each client keepalive at once and sends its own
 * every 2 s besides video, so a working path delivers a datagram within a
 * round trip of starting and then at least every 2 s. The transport calls
 * [onPacket] on its receive thread and [check] every [CHECK_INTERVAL_MS].
 */
class MediaWatchdog(
    startMs: Long,
    private val firstPacketMs: Long = FIRST_PACKET_MS,
    private val silenceMs: Long = SILENCE_MS,
) {
    /** What [check] found. */
    enum class Verdict {
        /** Nothing arrived yet, but it is early. */
        WAITING,

        /** Datagrams arrive. */
        ALIVE,

        /** Nothing arrived within [FIRST_PACKET_MS]: UDP is blocked one way or the other. */
        NEVER_ARRIVED,

        /** Datagrams arrived, then none for [SILENCE_MS]: the path was lost. */
        LOST,
    }

    @Volatile
    private var lastPacket = NONE
    private var graceFrom = startMs
    private var lastCheck = startMs

    /** Records an authenticated datagram from the host. */
    fun onPacket(nowMs: Long) {
        lastPacket = nowMs
    }

    /** Whether the path still works at [nowMs]. */
    fun check(nowMs: Long): Verdict {
        // A check this late means the app itself did not run (frozen in the
        // background): the receive thread could not read either, so the
        // deadline starts again instead of counting that pause as silence.
        if (nowMs - lastCheck > STALL_MS) graceFrom = nowMs
        lastCheck = nowMs
        val last = lastPacket
        val quiet = nowMs - maxOf(last, graceFrom)
        return when {
            last == NONE && quiet >= firstPacketMs -> Verdict.NEVER_ARRIVED
            last == NONE -> Verdict.WAITING
            quiet >= silenceMs -> Verdict.LOST
            else -> Verdict.ALIVE
        }
    }

    companion object {
        /** The first datagram must arrive this soon after the transport starts. */
        const val FIRST_PACKET_MS = 5_000L

        /** After that, this long without one means the path is lost. */
        const val SILENCE_MS = 10_000L

        /** How often the transport checks. */
        const val CHECK_INTERVAL_MS = 500L

        /** A gap between checks longer than this is the app pausing, not the network. */
        const val STALL_MS = 3_000L
        private const val NONE = Long.MIN_VALUE
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
    private class Partial(val count: Int) {
        val parts = arrayOfNulls<ByteArray>(count)
        var received = 0
        var size = 0

        // Set by the first fragment; a frame first seen through parity lacks them.
        var known = false
        var keyframe = false
        var captureMicros = 0L

        // The first record of each group, indexed by group; the first record
        // sizes it to the frame's stride.
        var parity = arrayOfNulls<ParityPacket>(0)

        fun insert(index: Int, keyframe: Boolean, captureMicros: Long, data: ByteArray) {
            if (parts[index] != null) return
            if (!known) {
                known = true
                this.keyframe = keyframe
                this.captureMicros = captureMicros
            }
            parts[index] = data
            received++
            size += data.size
        }

        fun payload(i: Int): ByteArray = Wire.videoPayload(i, count, keyframe, captureMicros, parts[i]!!)
    }

    private val partial = HashMap<Int, Partial>()
    private val ready = HashMap<Int, EncodedFrame>()
    private var next: Int? = null
    private var newest: Int? = null

    /** Whether frames are being dropped until a keyframe arrives. */
    @Volatile
    var needKeyframe = true
        private set

    /** The frame before the next one expected, reported in telemetry. */
    val lastContiguousFrameId: Int
        @Synchronized get() = ((next ?: 0) - 1) and 0xFFFF

    /** Drops frames until the next keyframe, for decoder errors. */
    @Synchronized
    fun forceKeyframe() {
        needKeyframe = true
    }

    /** The parity records kept for an incomplete frame, for tests. */
    @Synchronized
    internal fun parityHeld(frameId: Int): Int = partial[frameId]?.parity?.count { it != null } ?: 0

    /** Takes one fragment and returns the frames it made ready, in order. */
    @Synchronized
    fun add(f: VideoFragment): List<EncodedFrame> {
        val expected = next
        if (expected != null && distance(f.frameId, expected) < 0) return emptyList()
        if (newest == null || distance(f.frameId, newest!!) > 0) newest = f.frameId
        if (ready.containsKey(f.frameId)) return emptyList()

        val p = partial.getOrPut(f.frameId) { Partial(f.count) }
        if (f.count == p.count) {
            p.insert(f.index, f.keyframe, f.captureMicros, f.data.copyOfRange(f.offset, f.offset + f.length))
            if (p.parity.isNotEmpty()) recover(f.frameId, p, f.index % p.parity.size)
            complete(f.frameId, p)
        }
        return drain()
    }

    /** Takes one parity packet and returns the frames it made ready, in order. */
    @Synchronized
    fun addParity(q: ParityPacket): List<EncodedFrame> {
        val expected = next
        if (expected != null && distance(q.frameId, expected) < 0) return emptyList()
        if (ready.containsKey(q.frameId)) return emptyList()
        if (newest == null || distance(q.frameId, newest!!) > 0) newest = q.frameId
        val p = partial.getOrPut(q.frameId) { Partial(q.count) }
        if (q.count != p.count) return emptyList()
        // The first record fixes the frame's groups. The host derives the stride from the
        // fragment count, so a record with another stride, or more groups than fragments, is
        // not one it sends and is ignored.
        if (p.parity.isEmpty() && q.stride > 0 && q.stride <= q.count) p.parity = arrayOfNulls(q.stride)
        if (q.stride != p.parity.size || q.group >= p.parity.size) return emptyList()
        // One record per group: a repeated one adds nothing and would pile up.
        if (p.parity[q.group] != null) return emptyList()
        p.parity[q.group] = q
        recover(q.frameId, p, q.group)
        complete(q.frameId, p)
        return drain()
    }

    // Rebuilds the fragment that [group] lacks when it lacks exactly one and its
    // parity record is in. Groups do not overlap, so only the group of the packet
    // that just arrived can have become recoverable, and the fragment it rebuilds
    // completes no other group.
    private fun recover(frameId: Int, p: Partial, group: Int) {
        val q = p.parity[group] ?: return
        // Count first: a group that lacks nothing, or two or more, cannot use
        // its parity, and rebuilding the members' payloads is the costly part.
        var missing = -1
        var lost = 0
        for (i in group until p.count step q.stride) {
            if (p.parts[i] == null) {
                missing = i
                lost++
            }
        }
        if (lost != 1) return
        val present = ArrayList<ByteArray>()
        for (i in group until p.count step q.stride) {
            if (i != missing) present += p.payload(i)
        }
        val payload = q.recover(present) ?: return
        val f = Wire.parseVideo(Header(Wire.TYPE_VIDEO, 0, 0, frameId, missing and 0xFF, p.count and 0xFF), payload) ?: return
        if (f.index != missing || f.count != p.count) return
        p.insert(missing, f.keyframe, f.captureMicros, f.data.copyOfRange(f.offset, f.offset + f.length))
    }

    private fun complete(frameId: Int, p: Partial) {
        if (p.received != p.count) return
        partial.remove(frameId)
        val data = ByteArray(p.size)
        var pos = 0
        for (part in p.parts) {
            System.arraycopy(part!!, 0, data, pos, part.size)
            pos += part.size
        }
        ready[frameId] = EncodedFrame(frameId, data, p.keyframe, p.captureMicros)
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

    /** Records a received sequence number. */
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

/**
 * Tracks what the outgoing input holds down, for the [InputKind.HELD]
 * snapshot that lets the host lift keys whose up packet was lost.
 */
class HeldInput {
    private val keys = linkedSetOf<Int>()
    private val buttons = linkedSetOf<Int>()
    private var penFlags = InputKind.PEN_OUT
    private var penX = 0
    private var penY = 0

    /** Updates the state from one outgoing event. */
    @Synchronized
    fun observe(e: InputEvent) {
        val down = e.flags == 1
        when (e.kind) {
            InputKind.KEY -> if (down) keys.add(e.code) else keys.remove(e.code)
            InputKind.MOUSE_BUTTON -> if (down) buttons.add(e.code) else buttons.remove(e.code)
            InputKind.ABS_MOVE -> {
                penFlags = e.flags
                penX = e.x
                penY = e.y
            }
        }
    }

    /**
     * The events that release everything held: buttons, the pen (out of
     * range where it was), then keys, the last pressed first, so modifiers
     * lift last. Forgets it all, so the next snapshot holds nothing.
     */
    @Synchronized
    fun releaseAll(): List<InputEvent> {
        val out = ArrayList<InputEvent>()
        buttons.toList().asReversed().forEach { out += InputEvent.button(it, false) }
        if (penFlags != InputKind.PEN_OUT) out += InputEvent.pen(InputKind.PEN_OUT, penX, penY)
        keys.toList().asReversed().forEach { out += InputEvent.key(it, false) }
        buttons.clear()
        keys.clear()
        penFlags = InputKind.PEN_OUT
        return out
    }

    /** The snapshot records; one [InputKind.HELD_NONE] record when nothing is held. */
    @Synchronized
    fun snapshot(): List<InputEvent> {
        val out = ArrayList<InputEvent>()
        keys.forEach { out += InputEvent(InputKind.HELD, InputKind.HELD_KEY, it) }
        buttons.forEach { out += InputEvent(InputKind.HELD, InputKind.HELD_BUTTON, it) }
        val pen = when (penFlags) {
            InputKind.PEN_TOUCH -> InputKind.PEN_HELD_TOUCH or InputKind.PEN_HELD_IN_RANGE
            InputKind.PEN_HOVER -> InputKind.PEN_HELD_IN_RANGE
            else -> 0
        }
        if (pen != 0) out += InputEvent(InputKind.HELD, InputKind.HELD_PEN, pen)
        if (out.isEmpty()) out += InputEvent(InputKind.HELD, InputKind.HELD_NONE)
        return out.take(Wire.MAX_EVENTS_PER_PACKET)
    }
}
