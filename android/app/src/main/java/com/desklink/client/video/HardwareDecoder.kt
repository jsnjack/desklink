package com.desklink.client.video

import android.media.MediaCodec
import android.media.MediaCodecInfo
import android.media.MediaCodecList
import android.media.MediaFormat
import android.os.Handler
import android.os.HandlerThread
import android.os.Looper
import android.util.Log
import android.view.Surface
import com.desklink.client.net.EncodedFrame
import com.desklink.client.net.FrameSink
import java.util.ArrayDeque
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

/**
 * Decodes the stream with a hardware MediaCodec straight onto the
 * SurfaceView's surface, in low-latency mode, releasing each output buffer
 * for display as soon as it is ready.
 */
class HardwareDecoder(
    private val surface: Surface,
    private val codecName: String,
    private val width: Int,
    private val height: Int,
    /** Called when the decoder needs a keyframe to recover. */
    private val onNeedKeyframe: () -> Unit,
    /** Called once when the codec failed for good and must be rebuilt. */
    private val onFatal: () -> Unit,
    /** Called on the decoder thread with the host's capture time of each frame sent to the screen. */
    private val onShown: (captureMicros: Long) -> Unit = {},
) : FrameSink {
    private val thread = HandlerThread("desklink-decoder", android.os.Process.THREAD_PRIORITY_DISPLAY).apply { start() }
    private val handler = Handler(thread.looper)
    private val lock = Any()
    private val freeInputs = ArrayDeque<Int>()
    private val pending = ArrayDeque<EncodedFrame>()
    private var codec: MediaCodec? = null

    @Volatile
    private var failed = false

    private val callback = object : MediaCodec.Callback() {
        override fun onInputBufferAvailable(codec: MediaCodec, index: Int) {
            synchronized(lock) {
                freeInputs.add(index)
                feedLocked()
            }
        }

        override fun onOutputBufferAvailable(codec: MediaCodec, index: Int, info: MediaCodec.BufferInfo) {
            try {
                codec.releaseOutputBuffer(index, info.size > 0)
                if (info.size > 0 && info.presentationTimeUs > 0) onShown(info.presentationTimeUs)
            } catch (e: IllegalStateException) {
                // The codec left the executing state; onError or fail() rebuilds it.
                Log.w(TAG, "release output buffer", e)
                fail()
            }
        }

        override fun onError(codec: MediaCodec, e: MediaCodec.CodecException) {
            Log.w(TAG, "decoder error, recoverable=${e.isRecoverable} transient=${e.isTransient}", e)
            if (e.isTransient) onNeedKeyframe() else fail()
        }

        override fun onOutputFormatChanged(codec: MediaCodec, format: MediaFormat) {
            Log.d(TAG, "decoder output format $format")
        }
    }

    /**
     * Creates, configures and starts the codec; throws when no hardware
     * decoder exists or the codec cannot start, after releasing the codec it
     * created, since hardware decoders are few.
     */
    fun start() {
        val mime = mimeFor(codecName)
        val name = hardwareDecoder(mime) ?: throw IllegalStateException("no hardware decoder for $mime")
        val format = MediaFormat.createVideoFormat(mime, width, height).apply {
            setInteger(MediaFormat.KEY_LOW_LATENCY, 1)
            setInteger(MediaFormat.KEY_OPERATING_RATE, OPERATING_RATE)
            setInteger(MediaFormat.KEY_PRIORITY, 0)
            setInteger(MediaFormat.KEY_MAX_INPUT_SIZE, MAX_INPUT_SIZE)
            // Qualcomm decoders' own low-latency switch; other vendors ignore it.
            setInteger("vendor.qti-ext-dec-low-latency.enable", 1)
        }
        val c = MediaCodec.createByCodecName(name)
        try {
            c.setCallback(callback, handler)
            c.configure(format, surface, null, 0)
            c.start()
        } catch (e: Exception) {
            runCatching { c.release() }.onFailure { Log.d(TAG, "release unstarted decoder: ${it.message}") }
            throw e
        }
        synchronized(lock) { codec = c }
        Log.i(TAG, "decoding $mime with $name at ${width}x$height")
    }

    override fun onFrame(frame: EncodedFrame) {
        if (failed) return
        synchronized(lock) {
            if (pending.size >= MAX_PENDING) {
                // The decoder fell far behind; start again from a keyframe.
                pending.clear()
                onNeedKeyframe()
                if (!frame.keyframe) return
            }
            pending.add(frame)
            feedLocked()
        }
    }

    private fun feedLocked() {
        val c = codec ?: return
        try {
            feedLocked(c)
        } catch (e: IllegalStateException) {
            // Covers CodecException: the codec left the executing state.
            Log.w(TAG, "decoder rejected input", e)
            fail()
        }
    }

    /** Stops using the codec and asks for a new one, once. */
    private fun fail() {
        synchronized(lock) {
            if (failed) return
            failed = true
            pending.clear()
            freeInputs.clear()
        }
        onFatal()
    }

    private fun feedLocked(c: MediaCodec) {
        while (!failed && freeInputs.isNotEmpty() && pending.isNotEmpty()) {
            val frame = pending.removeFirst()
            val index = freeInputs.removeFirst()
            val buf = c.getInputBuffer(index) ?: continue
            if (frame.data.size > buf.capacity()) {
                Log.w(TAG, "frame of ${frame.data.size} bytes exceeds the input buffer")
                c.queueInputBuffer(index, 0, 0, frame.captureMicros, 0)
                onNeedKeyframe()
                continue
            }
            buf.clear()
            buf.put(frame.data)
            val flags = if (frame.keyframe) MediaCodec.BUFFER_FLAG_KEY_FRAME else 0
            c.queueInputBuffer(index, 0, frame.data.size, frame.captureMicros, flags)
        }
    }

    /**
     * Stops and releases the codec on its own thread and waits up to
     * [RELEASE_WAIT_MS] for that, so a replacement does not compete with it
     * for the hardware decoder and nothing renders to a surface being
     * destroyed.
     */
    fun release() {
        val c = synchronized(lock) {
            pending.clear()
            freeInputs.clear()
            codec.also { codec = null }
        }
        val done = CountDownLatch(1)
        val posted = handler.post {
            runCatching { c?.stop() }.onFailure { Log.d(TAG, "stop decoder: ${it.message}") }
            runCatching { c?.release() }.onFailure { Log.d(TAG, "release decoder: ${it.message}") }
            done.countDown()
            thread.quitSafely()
        }
        // A second release finds the thread gone; the decoder's own thread cannot wait for itself.
        if (!posted || Looper.myLooper() == thread.looper) return
        if (!done.await(RELEASE_WAIT_MS, TimeUnit.MILLISECONDS)) Log.w(TAG, "decoder release still running")
    }

    companion object {
        private const val TAG = "DeskLink"
        private const val OPERATING_RATE = 120
        private const val MAX_INPUT_SIZE = 8 shl 20
        private const val MAX_PENDING = 30
        private const val RELEASE_WAIT_MS = 1000L
        // Codec names on the wire.
        const val CODEC_AV1 = "av1"
        const val CODEC_HEVC = "hevc"

        /** The MediaCodec MIME type for a wire codec name. */
        fun mimeFor(codec: String): String =
            if (codec == CODEC_HEVC) MediaFormat.MIMETYPE_VIDEO_HEVC else MediaFormat.MIMETYPE_VIDEO_AV1

        /** The hardware decoder for [mime], preferring one with low-latency support. */
        fun hardwareDecoder(mime: String): String? {
            val candidates = MediaCodecList(MediaCodecList.REGULAR_CODECS).codecInfos.filter { info ->
                !info.isEncoder && info.isHardwareAccelerated && !info.isSoftwareOnly &&
                    info.supportedTypes.any { it.equals(mime, ignoreCase = true) }
            }
            return (candidates.firstOrNull {
                it.getCapabilitiesForType(mime).isFeatureSupported(MediaCodecInfo.CodecCapabilities.FEATURE_LowLatency)
            } ?: candidates.firstOrNull())?.name
        }

        /** AV1 when the device decodes it in hardware, otherwise HEVC. */
        fun bestCodec(): String =
            if (hardwareDecoder(MediaFormat.MIMETYPE_VIDEO_AV1) != null) CODEC_AV1 else CODEC_HEVC
    }
}
