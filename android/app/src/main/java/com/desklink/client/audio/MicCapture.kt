package com.desklink.client.audio

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
import android.media.AudioFormat
import android.media.AudioRecord
import android.media.MediaCodec
import android.media.MediaFormat
import android.media.MediaRecorder
import android.media.audiofx.AcousticEchoCanceler
import android.media.audiofx.AudioEffect
import android.media.audiofx.NoiseSuppressor
import android.util.Log
import com.desklink.client.model.Wire

/**
 * Captures this tablet's microphone for the desk. AudioRecord records 48 kHz
 * mono from the VOICE_COMMUNICATION source, which applies the platform's
 * echo cancellation against what the tablet plays (the desktop's sound would
 * otherwise go back into the call) and its noise suppression; the effects are
 * also switched on explicitly where the device offers them. MediaCodec's Opus
 * encoder (`audio/opus`, built in from Android 10) takes 16-bit PCM in any
 * chunk size and emits 20 ms packets at [BITRATE]; each goes to [onFrame]
 * with its capture time in Unix microseconds. All work runs on one thread.
 */
class MicCapture(
    private val context: Context,
    private val onFrame: (captureMicros: Long, frame: ByteArray) -> Unit,
    /** Called on the capture thread when the microphone or the encoder fails; capture has stopped. */
    private val onFailure: (String) -> Unit,
) {
    @Volatile
    private var running = false
    private var thread: Thread? = null

    /** Starts capturing. */
    fun start() {
        running = true
        thread = Thread(::run, "DeskLink microphone").apply {
            priority = Thread.MAX_PRIORITY
            start()
        }
    }

    /** Stops capturing and waits briefly for the thread, which frees the recorder and the codec. */
    fun stop() {
        running = false
        thread?.join(JOIN_MS)
        thread = null
    }

    private fun run() {
        var record: AudioRecord? = null
        var codec: MediaCodec? = null
        val effects = mutableListOf<AudioEffect>()
        try {
            // The switch asks for the permission first; it can be revoked
            // while the app runs.
            if (context.checkSelfPermission(Manifest.permission.RECORD_AUDIO) != PackageManager.PERMISSION_GRANTED) {
                throw SecurityException("no permission to record")
            }
            val minBuffer = AudioRecord.getMinBufferSize(RATE, AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT)
            val r = AudioRecord.Builder()
                .setAudioSource(MediaRecorder.AudioSource.VOICE_COMMUNICATION)
                .setAudioFormat(
                    AudioFormat.Builder()
                        .setSampleRate(RATE)
                        .setChannelMask(AudioFormat.CHANNEL_IN_MONO)
                        .setEncoding(AudioFormat.ENCODING_PCM_16BIT)
                        .build(),
                )
                .setBufferSizeInBytes(maxOf(minBuffer, CHUNK_BYTES * 4))
                .build()
            record = r
            check(r.state == AudioRecord.STATE_INITIALIZED) { "the microphone is not available" }
            if (AcousticEchoCanceler.isAvailable()) AcousticEchoCanceler.create(r.audioSessionId)?.let { it.enabled = true; effects += it }
            if (NoiseSuppressor.isAvailable()) NoiseSuppressor.create(r.audioSessionId)?.let { it.enabled = true; effects += it }
            val format = MediaFormat.createAudioFormat(MediaFormat.MIMETYPE_AUDIO_OPUS, RATE, 1).apply {
                setInteger(MediaFormat.KEY_BIT_RATE, BITRATE)
                setInteger(MediaFormat.KEY_MAX_INPUT_SIZE, CHUNK_BYTES)
            }
            val c = MediaCodec.createEncoderByType(MediaFormat.MIMETYPE_AUDIO_OPUS)
            codec = c
            c.configure(format, null, null, MediaCodec.CONFIGURE_FLAG_ENCODE)
            c.start()
            r.startRecording()
            Log.d(TAG, "microphone: capturing with ${c.name}, echo canceller ${AcousticEchoCanceler.isAvailable()}")
            capture(r, c)
        } catch (e: Exception) {
            // SecurityException, IllegalStateException, IOException from the codec.
            Log.w(TAG, "microphone stopped", e)
            if (running) onFailure(e.message ?: e.toString())
        } finally {
            effects.forEach { it.release() }
            record?.let {
                runCatching { it.stop() }
                it.release()
            }
            codec?.let {
                runCatching { it.stop() }
                it.release()
            }
        }
    }

    private fun capture(r: AudioRecord, c: MediaCodec) {
        val pcm = ByteArray(CHUNK_BYTES)
        val info = MediaCodec.BufferInfo()
        while (running) {
            val n = r.read(pcm, 0, pcm.size)
            check(n >= 0) { "AudioRecord.read failed: $n" }
            if (n == 0) continue
            // read returns once the chunk is recorded: it began n bytes ago.
            val captured = System.currentTimeMillis() * 1000 - micros(n)
            var off = 0
            while (off < n && running) {
                val index = c.dequeueInputBuffer(INPUT_WAIT_US)
                if (index < 0) {
                    drain(c, info)
                    continue
                }
                val buf = c.getInputBuffer(index) ?: break
                buf.clear()
                val len = minOf(n - off, buf.remaining())
                buf.put(pcm, off, len)
                c.queueInputBuffer(index, 0, len, captured + micros(off), 0)
                off += len
            }
            drain(c, info)
        }
    }

    private fun drain(c: MediaCodec, info: MediaCodec.BufferInfo) {
        while (true) {
            val index = c.dequeueOutputBuffer(info, 0)
            when {
                index >= 0 -> {
                    val out = c.getOutputBuffer(index)
                    if (out != null && info.size > 0) {
                        val frame = ByteArray(info.size)
                        out.position(info.offset)
                        out.get(frame)
                        val config = info.flags and MediaCodec.BUFFER_FLAG_CODEC_CONFIG != 0
                        if (MicFrames.isPacket(config, frame)) onFrame(info.presentationTimeUs, frame)
                    }
                    c.releaseOutputBuffer(index, false)
                }
                index == MediaCodec.INFO_OUTPUT_FORMAT_CHANGED -> Unit
                else -> return
            }
        }
    }

    companion object {
        private const val TAG = "DeskLink"
        private const val RATE = 48000
        private const val BYTES_PER_SAMPLE = 2

        /** 20 ms of 16-bit mono, the encoder's frame. */
        private const val CHUNK_BYTES = RATE / 50 * BYTES_PER_SAMPLE

        /** Opus at 32 kbit/s carries speech at full band, about 80 bytes a frame, as the laptop client sends. */
        const val BITRATE = 32_000
        private const val INPUT_WAIT_US = 10_000L
        private const val JOIN_MS = 500L

        private fun micros(bytes: Int): Long = bytes / BYTES_PER_SAMPLE * 1_000_000L / RATE
    }
}

/** Which encoder output buffers go to the desk. */
object MicFrames {
    /** The largest Opus frame that fits one datagram after the capture time. */
    const val MAX_FRAME = Wire.MAX_PLAINTEXT - Wire.AUDIO_PREFIX

    /**
     * Whether an encoder output buffer is an Opus packet: not the codec's
     * header, which Android's encoder emits first, flagged as codec config
     * ([config]) and starting with "OpusHead" or its own "AOPUSHDR" marker;
     * and small enough for one datagram.
     */
    fun isPacket(config: Boolean, frame: ByteArray): Boolean {
        if (config || frame.isEmpty() || frame.size > MAX_FRAME) return false
        return HEADERS.none { h -> frame.size >= h.size && frame.copyOfRange(0, h.size).contentEquals(h) }
    }

    private val HEADERS = listOf("OpusHead", "AOPUSHDR").map { it.toByteArray(Charsets.US_ASCII) }
}

/** Whether the tablet's microphone streams to the desk, and what the HUD switch does. */
object MicPolicy {
    /**
     * Capture runs only while the user has the switch on ([wanted]), the
     * host reports that its virtual microphone plays it ([hostOn]), the app is
     * in the foreground (Android hands a background app silence, and the
     * stream stops there anyway) and the app may record.
     */
    fun capture(wanted: Boolean, hostOn: Boolean?, foreground: Boolean, permitted: Boolean): Boolean =
        wanted && hostOn == true && foreground && permitted

    /** The `hello.mic` of a connection: the saved switch, if the app may still record. */
    fun ask(wanted: Boolean, permitted: Boolean): Boolean = wanted && permitted

    /** What moving the switch to [on] does. */
    fun toggle(on: Boolean, permitted: Boolean): Step = when {
        !on -> Step.TURN_OFF
        permitted -> Step.TURN_ON
        else -> Step.ASK_PERMISSION
    }

    /** What the answer to the permission dialog does. */
    fun answered(granted: Boolean): Step = if (granted) Step.TURN_ON else Step.EXPLAIN_REFUSAL

    enum class Step { TURN_ON, TURN_OFF, ASK_PERMISSION, EXPLAIN_REFUSAL }
}
