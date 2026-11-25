package com.desklink.client.audio

import android.media.AudioAttributes
import android.media.AudioFormat
import android.media.AudioTrack
import android.media.MediaCodec
import android.media.MediaFormat
import android.os.Handler
import android.os.HandlerThread
import android.os.Process
import android.os.SystemClock
import android.util.Log
import com.desklink.client.model.AudioPacket
import java.nio.ByteBuffer
import java.nio.ByteOrder

/**
 * Plays the host's sound: 48 kHz stereo Opus in 10 ms packets. Each packet is
 * decoded as it arrives and written to a low-latency AudioTrack. Playback
 * starts once [PREBUFFER_MS] is queued, so the first network jitter does not
 * underrun; after that [AudioCatchUp] keeps the queue near 40 ms, dropping
 * silence (or, rarely, a packet of sound) after a hiccup has lengthened it,
 * so sound never drifts behind the picture. Late or repeated packets (by
 * their 16-bit counter) are dropped. All work runs on one thread, at audio
 * priority: with so little queued, a decode delayed by the UI would be heard.
 */
class AudioPlayer {
    private val thread = HandlerThread("DeskLink audio", Process.THREAD_PRIORITY_AUDIO).apply { start() }
    private val handler = Handler(thread.looper)
    private var codec: MediaCodec? = null
    private var track: AudioTrack? = null
    private var lastCounter = -1
    private var writtenFrames = 0L
    private var playing = false
    private var pcm = ByteArray(0)
    private val info = MediaCodec.BufferInfo()
    private val catchUp = AudioCatchUp()
    private var packets = 0L
    private var decoded = 0L
    private var dropped = 0L
    private var caughtUp = 0L

    /** Opens the decoder and the audio output. */
    fun start() {
        handler.post {
            try {
                open()
            } catch (e: Exception) {
                Log.e(TAG, "audio output failed to start", e)
                close()
            }
        }
    }

    /** Queues one packet from the network thread. */
    fun onPacket(p: AudioPacket) {
        handler.post { decode(p) }
    }

    /** Stops playback and frees the decoder; the player cannot be restarted. */
    fun release() {
        handler.post { close() }
        thread.quitSafely()
    }

    private fun open() {
        val format = MediaFormat.createAudioFormat(MediaFormat.MIMETYPE_AUDIO_OPUS, RATE, CHANNELS).apply {
            setByteBuffer("csd-0", ByteBuffer.wrap(opusHead()))
            setByteBuffer("csd-1", nanos(PRE_SKIP_NS))
            setByteBuffer("csd-2", nanos(SEEK_PREROLL_NS))
        }
        codec = MediaCodec.createDecoderByType(MediaFormat.MIMETYPE_AUDIO_OPUS).apply {
            configure(format, null, null, 0)
            start()
        }
        val minBuffer = AudioTrack.getMinBufferSize(RATE, AudioFormat.CHANNEL_OUT_STEREO, AudioFormat.ENCODING_PCM_16BIT)
        // The smallest buffer that holds the most AudioCatchUp lets queue
        // first, larger ones on a device that refuses it. What is queued, not
        // the buffer's size, makes the delay.
        var failure: Exception? = null
        for (ms in TRACK_BUFFERS_MS) {
            val t = try {
                buildTrack(maxOf(minBuffer, BYTES_PER_MS * ms))
            } catch (e: Exception) {
                failure = e
                Log.w(TAG, "audio output with a $ms ms buffer failed: ${e.message}")
                continue
            }
            if (t.state == AudioTrack.STATE_INITIALIZED) {
                track = t
                Log.d(TAG, "audio output: $ms ms buffer asked, ${t.bufferSizeInFrames * 1000L / RATE} ms given")
                return
            }
            t.release()
            Log.w(TAG, "audio output with a $ms ms buffer did not initialise")
        }
        throw IllegalStateException("no audio output could be created", failure)
    }

    private fun buildTrack(bytes: Int): AudioTrack = AudioTrack.Builder()
        .setAudioAttributes(
            AudioAttributes.Builder()
                .setUsage(AudioAttributes.USAGE_MEDIA)
                .setContentType(AudioAttributes.CONTENT_TYPE_UNKNOWN)
                .build(),
        )
        .setAudioFormat(
            AudioFormat.Builder()
                .setSampleRate(RATE)
                .setChannelMask(AudioFormat.CHANNEL_OUT_STEREO)
                .setEncoding(AudioFormat.ENCODING_PCM_16BIT)
                .build(),
        )
        .setBufferSizeInBytes(bytes)
        .setPerformanceMode(AudioTrack.PERFORMANCE_MODE_LOW_LATENCY)
        .setTransferMode(AudioTrack.MODE_STREAM)
        .build()

    private fun decode(p: AudioPacket) {
        packets++
        if (packets == 1L || packets % STATUS_EVERY == 0L) {
            val t = track
            val queued = t?.let { queuedMs(it) } ?: 0
            Log.d(
                TAG,
                "audio: $packets packets, $decoded decoded, $dropped dropped, $caughtUp dropped to catch up, " +
                    "${writtenFrames * 1000 / RATE} ms written, $queued ms queued, ${t?.underrunCount ?: 0} underruns, playing=$playing",
            )
        }
        val c = codec ?: return
        if (lastCounter >= 0) {
            val ahead = (p.counter - lastCounter) and 0xFFFF
            if (ahead == 0 || ahead > 0x8000) return
        }
        lastCounter = p.counter
        try {
            val index = c.dequeueInputBuffer(INPUT_WAIT_US)
            if (index < 0) {
                dropped++
                drain(c)
                return
            }
            c.getInputBuffer(index)?.apply {
                clear()
                put(p.data)
            }
            c.queueInputBuffer(index, 0, p.data.size, p.captureMicros, 0)
            drain(c)
        } catch (e: IllegalStateException) {
            Log.w(TAG, "audio decoder failed; sound stops", e)
            close()
        }
    }

    private fun drain(c: MediaCodec) {
        var wait = OUTPUT_WAIT_US
        while (true) {
            val index = c.dequeueOutputBuffer(info, wait)
            wait = 0
            when {
                index >= 0 -> {
                    val out = c.getOutputBuffer(index)
                    if (out != null && info.size > 0) {
                        decoded++
                        if (pcm.size < info.size) pcm = ByteArray(info.size)
                        out.position(info.offset)
                        out.get(pcm, 0, info.size)
                        write(pcm, info.size)
                    }
                    c.releaseOutputBuffer(index, false)
                }
                index == MediaCodec.INFO_OUTPUT_FORMAT_CHANGED -> Unit
                else -> return
            }
        }
    }

    // The sound written but not yet played, in milliseconds.
    private fun queuedMs(t: AudioTrack): Long = (writtenFrames - (t.playbackHeadPosition.toLong() and 0xFFFFFFFFL)) * 1000 / RATE

    private fun write(buf: ByteArray, size: Int) {
        val t = track ?: return
        if (playing) {
            val packetMs = size / BYTES_PER_FRAME * 1000L / RATE
            if (!catchUp.play(SystemClock.uptimeMillis(), queuedMs(t), packetMs, AudioCatchUp.isSilent(buf, size))) {
                caughtUp++
                return
            }
        }
        val n = t.write(buf, 0, size, AudioTrack.WRITE_NON_BLOCKING)
        if (n > 0) writtenFrames += n / BYTES_PER_FRAME
        if (!playing && writtenFrames * 1000 / RATE >= PREBUFFER_MS) {
            t.play()
            playing = true
            Log.d(TAG, "audio: playing after $packets packets")
        }
    }

    private fun close() {
        try {
            codec?.stop()
        } catch (e: IllegalStateException) {
            Log.d(TAG, "stop audio decoder: ${e.message}")
        }
        codec?.release()
        codec = null
        track?.let {
            try {
                it.stop()
            } catch (e: IllegalStateException) {
                Log.d(TAG, "stop audio track: ${e.message}")
            }
            it.release()
        }
        track = null
    }

    companion object {
        private const val TAG = "DeskLink"
        private const val RATE = 48000
        private const val CHANNELS = 2
        private const val BYTES_PER_FRAME = 2 * CHANNELS
        private const val BYTES_PER_MS = RATE / 1000 * BYTES_PER_FRAME

        /**
         * Track buffers to try, smallest first: room for the most
         * [AudioCatchUp] lets queue and two packets, then 120 and 240 ms.
         */
        private val TRACK_BUFFERS_MS = listOf(AudioCatchUp.MAX_MS.toInt() + 20, 120, 240)

        /** Playback starts once this much is queued. */
        const val PREBUFFER_MS = 30L
        private const val INPUT_WAIT_US = 5_000L
        private const val OUTPUT_WAIT_US = 2_000L
        private const val STATUS_EVERY = 500L

        /** The host encodes with audio-type=restricted-lowdelay: 2.5 ms lookahead. */
        private const val PRE_SKIP_SAMPLES = 120
        private const val PRE_SKIP_NS = PRE_SKIP_SAMPLES * 1_000_000_000L / RATE
        private const val SEEK_PREROLL_NS = 80_000_000L

        /** The 19-byte Opus identification header (RFC 7845) Android's decoder needs as csd-0. */
        fun opusHead(): ByteArray = ByteBuffer.allocate(19).order(ByteOrder.LITTLE_ENDIAN)
            .put("OpusHead".toByteArray(Charsets.US_ASCII))
            .put(1) // version
            .put(CHANNELS.toByte())
            .putShort(PRE_SKIP_SAMPLES.toShort())
            .putInt(RATE)
            .putShort(0) // output gain
            .put(0) // channel mapping family 0: mono or stereo
            .array()

        private fun nanos(v: Long): ByteBuffer =
            ByteBuffer.allocate(8).order(ByteOrder.nativeOrder()).putLong(v).apply { flip() }
    }
}
