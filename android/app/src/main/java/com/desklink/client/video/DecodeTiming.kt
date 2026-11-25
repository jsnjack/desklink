package com.desklink.client.video

import java.util.Locale

/**
 * Times each frame from the moment the frame assembler completed it to the
 * moment the decoder outputs it, matched by presentation time, the host's
 * capture time, which a frame keeps through the decoder. The time includes
 * waiting for an input buffer. A frame the decoder drops never comes out;
 * the oldest are forgotten once [capacity] wait.
 */
class DecodeClock(private val capacity: Int = CAPACITY) {
    private val queued = LinkedHashMap<Long, Long>()

    /** The frame presented at [ptsMicros] went to the decoder at [nowNanos]; 0, unknown, is not timed. */
    @Synchronized
    fun queued(ptsMicros: Long, nowNanos: Long) {
        if (ptsMicros <= 0) return
        queued.putIfAbsent(ptsMicros, nowNanos)
        if (queued.size > capacity) queued.remove(queued.keys.first())
    }

    /** The frame presented at [ptsMicros] came out at [nowNanos]: its decode time in microseconds, or null when unknown. */
    @Synchronized
    fun decoded(ptsMicros: Long, nowNanos: Long): Long? {
        val start = queued.remove(ptsMicros) ?: return null
        return (nowNanos - start) / 1000
    }

    /** Clock limits. */
    companion object {
        /** Half a second of frames at 120 fps. */
        const val CAPACITY = 64
    }
}

/** Decode times over a stats period: the mean and the 95th percentile, in microseconds. */
data class DecodeTime(val meanMicros: Long, val p95Micros: Long, val frames: Int)

/**
 * Collects decode times between two stats samples without allocating per
 * frame; more than [capacity] in one period are not kept.
 */
class DecodeMeter(private val capacity: Int = CAPACITY) {
    private val values = LongArray(capacity)
    private var n = 0

    /** Records one frame's decode time. */
    @Synchronized
    fun add(micros: Long) {
        if (n < capacity) values[n++] = micros
    }

    /** The period's mean and 95th percentile (nearest rank), or null when no frame was decoded; starts a new period. */
    @Synchronized
    fun take(): DecodeTime? {
        if (n == 0) return null
        val sorted = values.copyOf(n).apply { sort() }
        // Nearest rank: the ceil(0.95 n)-th smallest, in integers.
        val p95 = sorted[((n * P95_PERCENT + 99) / 100 - 1).coerceIn(0, n - 1)]
        val time = DecodeTime(sorted.sum() / n, p95, n)
        n = 0
        return time
    }

    /** Meter limits. */
    companion object {
        /** Two seconds of frames at 120 fps, more than a stats period holds. */
        const val CAPACITY = 256
        private const val P95_PERCENT = 95
    }
}

/** Whether a decoder is rated for a stream, from what it reports about itself. */
enum class DecoderRating {
    /** Its performance points, or the frame rates it measured, cover the stream. */
    RATED,

    /** It does not support the size and rate, or reports less than the stream needs. */
    NOT_RATED,

    /** It supports the size and rate but reports no performance for them. */
    UNKNOWN,
}

/**
 * What a decoder reports about a stream of one size and rate, from
 * MediaCodecInfo.VideoCapabilities: whether it supports them at all
 * (areSizeAndRateSupported), whether a listed performance point covers them
 * (null when it lists none), and the highest rate it measured at that size
 * (getAchievableFrameRatesFor, null when not measured).
 */
data class DecoderCapability(
    val supported: Boolean,
    val pointCovers: Boolean?,
    val measuredMaxFps: Double?,
) {
    /**
     * The verdict for [fps]. Performance points are what Android's media
     * performance class is certified against, so they decide when listed;
     * otherwise the measured rates do.
     */
    fun rating(fps: Int): DecoderRating = when {
        !supported -> DecoderRating.NOT_RATED
        pointCovers != null -> if (pointCovers) DecoderRating.RATED else DecoderRating.NOT_RATED
        measuredMaxFps != null -> if (measuredMaxFps >= fps) DecoderRating.RATED else DecoderRating.NOT_RATED
        else -> DecoderRating.UNKNOWN
    }
}

/**
 * The decoder a session uses and what it reports, for the stats overlay
 * and the log: its codec name, the low-latency options it accepted, its
 * rating for the session's top size and rate with the detail behind it (a
 * performance point or a measured rate), and its vendor parameters.
 */
data class DecoderReport(
    val name: String,
    val options: Set<DecoderOption>,
    val width: Int,
    val height: Int,
    val fps: Int,
    val rating: DecoderRating,
    val ratingDetail: String,
    val vendorParameters: List<String>,
) {
    /** The options the decoder refused, in a word each, for the overlay; empty when it took them all. */
    val refused: List<String> get() = DecoderOption.entries.filter { it !in options }.map { it.label }

    /** One line for the log. */
    fun describe(): String = String.format(
        Locale.ROOT, "%s for %dx%d@%d: %s%s; options %s; vendor parameters %s",
        name, width, height, fps, rating.name.lowercase(Locale.ROOT).replace('_', ' '),
        if (ratingDetail.isEmpty()) "" else " ($ratingDetail)",
        options.joinToString { it.label }.ifEmpty { "none" },
        vendorParameters.joinToString().ifEmpty { "none" },
    )
}
