package com.desklink.client.video

import android.media.MediaCodec
import android.media.MediaCodecInfo
import android.media.MediaCodecList
import android.media.MediaFormat
import android.os.Handler
import android.os.HandlerThread
import android.util.Log
import android.view.Surface
import com.desklink.client.net.EncodedFrame
import com.desklink.client.net.FrameSink
import java.util.ArrayDeque

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
    /** Called when the decoder failed and needs a keyframe to recover. */
    private val onNeedKeyframe: () -> Unit,
) : FrameSink {
    private val thread = HandlerThread("desklink-decoder", android.os.Process.THREAD_PRIORITY_DISPLAY).apply { start() }
    private val handler = Handler(thread.looper)
    private val lock = Any()
    private val freeInputs = ArrayDeque<Int>()
    private val pending = ArrayDeque<EncodedFrame>()
    private var codec: MediaCodec? = null

    fun start() {
        val mime = mimeFor(codecName)
        val name = hardwareDecoder(mime) ?: throw IllegalStateException("no hardware decoder for $mime")
        val c = MediaCodec.createByCodecName(name)
        val format = MediaFormat.createVideoFormat(mime, width, height).apply {
            setInteger(MediaFormat.KEY_LOW_LATENCY, 1)
            setInteger(MediaFormat.KEY_OPERATING_RATE, OPERATING_RATE)
            setInteger(MediaFormat.KEY_PRIORITY, 0)
            setInteger(MediaFormat.KEY_MAX_INPUT_SIZE, MAX_INPUT_SIZE)
            // Qualcomm decoders' own low-latency switch; other vendors ignore it.
            setInteger("vendor.qti-ext-dec-low-latency.enable", 1)
        }
        c.setCallback(object : MediaCodec.Callback() {
            override fun onInputBufferAvailable(codec: MediaCodec, index: Int) {
                synchronized(lock) {
                    freeInputs.add(index)
                    feedLocked()
                }
            }

            override fun onOutputBufferAvailable(codec: MediaCodec, index: Int, info: MediaCodec.BufferInfo) {
                codec.releaseOutputBuffer(index, info.size > 0)
            }

            override fun onError(codec: MediaCodec, e: MediaCodec.CodecException) {
                Log.w(TAG, "decoder error, recoverable=${e.isRecoverable} transient=${e.isTransient}", e)
                onNeedKeyframe()
            }

            override fun onOutputFormatChanged(codec: MediaCodec, format: MediaFormat) {
                Log.d(TAG, "decoder output format $format")
            }
        }, handler)
        c.configure(format, surface, null, 0)
        c.start()
        codec = c
        Log.i(TAG, "decoding $mime with $name at ${width}x$height")
    }

    override fun onFrame(frame: EncodedFrame) {
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
        while (freeInputs.isNotEmpty() && pending.isNotEmpty()) {
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

    fun release() {
        synchronized(lock) {
            pending.clear()
            freeInputs.clear()
        }
        val c = codec
        codec = null
        handler.post {
            runCatching { c?.stop() }.onFailure { Log.d(TAG, "stop decoder: ${it.message}") }
            c?.release()
            thread.quitSafely()
        }
    }

    companion object {
        private const val TAG = "DeskLink"
        private const val OPERATING_RATE = 120
        private const val MAX_INPUT_SIZE = 8 shl 20
        private const val MAX_PENDING = 30
        const val CODEC_AV1 = "av1"
        const val CODEC_HEVC = "hevc"

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
