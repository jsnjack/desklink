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
import java.util.Locale
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

/**
 * Decodes the stream with a hardware MediaCodec onto [surface], the
 * SurfaceView's or the input of a [GuidedRenderer], in low-latency mode,
 * releasing each output buffer for display as soon as it is ready.
 */
class HardwareDecoder(
    private val surface: Surface,
    private val codecName: String,
    private val width: Int,
    private val height: Int,
    /** The frame rate to rate the decoder for: the session's highest. */
    private val fps: Int,
    /** Called when the decoder needs a keyframe to recover. */
    private val onNeedKeyframe: () -> Unit,
    /** Called once when the codec failed for good, or hangs, and must be rebuilt. */
    private val onFatal: () -> Unit,
    /** Called on the decoder thread with the host's capture time of each frame sent to the screen. */
    private val onShown: (captureMicros: Long) -> Unit = {},
    /** Called on the decoder thread with each frame's time from arriving here to leaving the decoder. */
    private val onDecoded: (micros: Long) -> Unit = {},
) : FrameSink {
    private val thread = HandlerThread("desklink-decoder", android.os.Process.THREAD_PRIORITY_DISPLAY).apply { start() }
    private val handler = Handler(thread.looper)
    private val lock = Any()
    private val freeInputs = ArrayDeque<Int>()
    private val pending = ArrayDeque<EncodedFrame>()
    private var codec: MediaCodec? = null
    private val hang = HangWatch()
    private val clock = DecodeClock()

    @Volatile
    private var failed = false

    /** The decoder and what it reports about itself, once [start] returned. */
    @Volatile
    var report: DecoderReport? = null
        private set

    private val callback = object : MediaCodec.Callback() {
        override fun onInputBufferAvailable(codec: MediaCodec, index: Int) {
            synchronized(lock) {
                freeInputs.add(index)
                feedLocked()
            }
        }

        override fun onOutputBufferAvailable(codec: MediaCodec, index: Int, info: MediaCodec.BufferInfo) {
            try {
                if (info.size > 0) clock.decoded(info.presentationTimeUs, System.nanoTime())?.let(onDecoded)
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
     * Creates, configures and starts the codec, with fewer low-latency
     * options each time a setup is refused ([DecoderSetup]); throws when no
     * hardware decoder exists or no setup starts, after releasing every codec
     * it created, since hardware decoders are few.
     */
    fun start() {
        val mime = mimeFor(codecName)
        val name = hardwareDecoder(mime) ?: throw IllegalStateException("no hardware decoder for $mime")
        val started = DecoderSetup.withFewerOptions(
            refused = { options, e ->
                Log.w(TAG, "decoder $name refused the setup with ${options.joinToString { it.label }.ifEmpty { "no options" }}: ${e.message}")
            },
            // No option explains hardware that is still taken.
            optionsMayExplain = { !isBusy(it) },
        ) { options -> startCodec(name, format(mime, options)) }
        val c = started.value
        synchronized(lock) { codec = c }
        val r = describe(name, mime, started.options, c)
        report = r
        Log.i(TAG, "decoding $mime with ${r.describe()}")
    }

    private fun startCodec(name: String, format: MediaFormat): MediaCodec {
        val c = MediaCodec.createByCodecName(name)
        try {
            c.setCallback(callback, handler)
            c.configure(format, surface, null, 0)
            c.start()
        } catch (e: Exception) {
            runCatching { c.release() }.onFailure { Log.d(TAG, "release unstarted decoder: ${it.message}") }
            throw e
        }
        return c
    }

    private fun format(mime: String, options: Set<DecoderOption>) = MediaFormat.createVideoFormat(mime, width, height).apply {
        if (DecoderOption.LOW_LATENCY in options) setInteger(MediaFormat.KEY_LOW_LATENCY, 1)
        if (DecoderOption.REALTIME in options) {
            setInteger(MediaFormat.KEY_OPERATING_RATE, OPERATING_RATE)
            setInteger(MediaFormat.KEY_PRIORITY, 0)
        }
        // Qualcomm decoders' own low-latency switch; other vendors ignore it.
        if (DecoderOption.VENDOR_LOW_LATENCY in options) setInteger("vendor.qti-ext-dec-low-latency.enable", 1)
        setInteger(MediaFormat.KEY_MAX_INPUT_SIZE, MAX_INPUT_SIZE)
        // The host's VA-API encoders mark the stream limited range but leave
        // its primaries, transfer and matrix unspecified, and for 10-bit video
        // decoders tend to assume BT.2020, which shifts and dims the colours.
        // The stream carries the desktop's sRGB values with the BT.709 matrix;
        // these fill in what the stream omits. The transfer is sRGB, so the
        // compositor shows the values as they are, like any app's pixels.
        // With COLOR_TRANSFER_SDR_VIDEO, Android 16 on the Tab S9 decoded
        // them as gamma 2.2 and re-encoded them as sRGB, which darkened
        // near-black greys (a dark theme's 28 showed as 22).
        setInteger(MediaFormat.KEY_COLOR_STANDARD, MediaFormat.COLOR_STANDARD_BT709)
        setInteger(MediaFormat.KEY_COLOR_TRANSFER, COLOR_TRANSFER_SRGB)
        setInteger(MediaFormat.KEY_COLOR_RANGE, MediaFormat.COLOR_RANGE_LIMITED)
    }

    /** What the started decoder reports about itself, for the stats and the log. */
    private fun describe(name: String, mime: String, options: Set<DecoderOption>, c: MediaCodec): DecoderReport {
        val vendor = try {
            c.supportedVendorParameters.sorted()
        } catch (e: RuntimeException) {
            Log.d(TAG, "decoder vendor parameters: ${e.message}")
            emptyList()
        }
        val (capability, detail) = capability(name, mime, width, height, fps)
        val rating = capability?.rating(fps) ?: DecoderRating.UNKNOWN
        return DecoderReport(name, options, width, height, fps, rating, detail, vendor)
    }

    override fun onFrame(frame: EncodedFrame) {
        if (failed) return
        val hung = synchronized(lock) {
            if (hang.frameArrived(nowMs())) {
                true
            } else {
                take(frame)
                false
            }
        }
        if (hung) {
            Log.w(TAG, "the decoder took no input for ${HangWatch.HANG_MS} ms while frames arrived; rebuilding it")
            fail()
        }
    }

    private fun take(frame: EncodedFrame) {
        if (pending.size >= MAX_PENDING) {
            // The decoder fell far behind; start again from a keyframe.
            pending.clear()
            onNeedKeyframe()
            if (!frame.keyframe) return
        }
        pending.add(frame)
        clock.queued(frame.captureMicros, System.nanoTime())
        feedLocked()
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
                hang.inputTaken()
                onNeedKeyframe()
                continue
            }
            buf.clear()
            buf.put(frame.data)
            val flags = if (frame.keyframe) MediaCodec.BUFFER_FLAG_KEY_FRAME else 0
            c.queueInputBuffer(index, 0, frame.data.size, frame.captureMicros, flags)
            hang.inputTaken()
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

    private fun nowMs(): Long = System.nanoTime() / 1_000_000

    companion object {
        private const val TAG = "DeskLink"
        private const val OPERATING_RATE = 120
        private const val MAX_INPUT_SIZE = 8 shl 20
        private const val MAX_PENDING = 30
        private const val RELEASE_WAIT_MS = 1000L

        /** Codec errors that say the hardware is taken, not that the setup is wrong. */
        private val RESOURCE_ERRORS = setOf(
            MediaCodec.CodecException.ERROR_INSUFFICIENT_RESOURCE,
            MediaCodec.CodecException.ERROR_RECLAIMED,
        )

        /**
         * The platform's sRGB transfer (ColorUtils kColorTransferSRGB), which
         * MediaFormat accepts for KEY_COLOR_TRANSFER but does not name.
         */
        private const val COLOR_TRANSFER_SRGB = 2
        // Codec names on the wire.
        const val CODEC_AV1 = "av1"
        const val CODEC_HEVC = "hevc"

        /**
         * Whether [e] says the decoder hardware is taken rather than the
         * setup wrong: as when a hung codec's release has not finished yet.
         */
        fun isBusy(e: Throwable): Boolean = e is MediaCodec.CodecException && e.errorCode in RESOURCE_ERRORS

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

        /**
         * What the decoder [name] reports for [mime] at [width]×[height] and
         * [fps], with the performance point or measured rate behind it; null
         * capability when it reports nothing. Vendors fill these tables in
         * themselves, so a failure to read them only leaves the rating
         * unknown.
         */
        fun capability(name: String, mime: String, width: Int, height: Int, fps: Int): Pair<DecoderCapability?, String> = try {
            val caps = MediaCodecList(MediaCodecList.REGULAR_CODECS).codecInfos.firstOrNull { it.name == name }
                ?.getCapabilitiesForType(mime)?.videoCapabilities
            if (caps == null) {
                null to ""
            } else {
                val points = caps.supportedPerformancePoints?.takeIf { it.isNotEmpty() }
                val covering = points?.firstOrNull { it.covers(MediaCodecInfo.VideoCapabilities.PerformancePoint(width, height, fps)) }
                // Throws for a size the decoder does not support.
                val measured = runCatching { caps.getAchievableFrameRatesFor(width, height)?.upper }.getOrNull()
                val detail = when {
                    covering != null -> covering.toString()
                    points != null -> "points ${points.joinToString()}".take(MAX_DETAIL)
                    measured != null -> String.format(Locale.ROOT, "measured up to %.0f fps", measured)
                    else -> ""
                }
                DecoderCapability(caps.areSizeAndRateSupported(width, height, fps.toDouble()), points?.let { covering != null }, measured) to detail
            }
        } catch (e: RuntimeException) {
            Log.d(TAG, "decoder capabilities of $name: ${e.message}")
            null to ""
        }

        private const val MAX_DETAIL = 160

        /** AV1 when the device decodes it in hardware, otherwise HEVC. */
        fun bestCodec(): String =
            if (hardwareDecoder(MediaFormat.MIMETYPE_VIDEO_AV1) != null) CODEC_AV1 else CODEC_HEVC
    }
}
