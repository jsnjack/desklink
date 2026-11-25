package com.desklink.client.video

/** A low-latency option the decoder is configured with, which a firmware may refuse. */
enum class DecoderOption(val label: String) {
    /** Vendor keys, such as Qualcomm's vendor.qti-ext-dec-low-latency.enable. */
    VENDOR_LOW_LATENCY("vendor low latency"),

    /** MediaFormat.KEY_LOW_LATENCY. */
    LOW_LATENCY("low latency"),

    /** KEY_OPERATING_RATE and realtime KEY_PRIORITY. */
    REALTIME("operating rate and priority"),
}

/**
 * Configures a decoder with fewer low-latency options each time the last
 * setup was refused, so a firmware update that rejects one of them costs
 * latency instead of the stream.
 */
object DecoderSetup {
    /** What starting returned, and the options it started with. */
    class Started<T>(val value: T, val options: Set<DecoderOption>)

    /**
     * Every option first; then without the vendor keys, without
     * KEY_LOW_LATENCY too, and last with none.
     */
    val ATTEMPTS: List<Set<DecoderOption>> = listOf(
        setOf(DecoderOption.VENDOR_LOW_LATENCY, DecoderOption.LOW_LATENCY, DecoderOption.REALTIME),
        setOf(DecoderOption.LOW_LATENCY, DecoderOption.REALTIME),
        setOf(DecoderOption.REALTIME),
        emptySet(),
    )

    /**
     * Runs [start] with each of [attempts] in turn until one returns, telling
     * [refused] about each failure. A failure the options cannot explain
     * ([optionsMayExplain] false, such as a decoder another codec still
     * holds) is thrown at once, with the earlier ones suppressed: fewer
     * options would fail the same way and only cost latency. When every
     * attempt fails, it throws the last failure the same way.
     */
    fun <T> withFewerOptions(
        attempts: List<Set<DecoderOption>> = ATTEMPTS,
        refused: (Set<DecoderOption>, Exception) -> Unit = { _, _ -> },
        optionsMayExplain: (Exception) -> Boolean = { true },
        start: (Set<DecoderOption>) -> T,
    ): Started<T> {
        val failures = ArrayList<Exception>()
        for (options in attempts) {
            try {
                return Started(start(options), options)
            } catch (e: Exception) {
                refused(options, e)
                failures += e
                if (!optionsMayExplain(e)) break
            }
        }
        val last = failures.lastOrNull() ?: IllegalStateException("no decoder setup to try")
        failures.dropLast(1).forEach(last::addSuppressed)
        throw last
    }
}

/**
 * Tells a hung decoder from a busy one. A working decoder takes each frame
 * within milliseconds of a free input buffer, so a frame that has waited
 * [limitMs] with no input taken since means the codec stopped. Frames keep
 * arriving while the host streams, at least ten a second on a still screen
 * (PipeWire repeats the last frame every 100 ms), so each arrival checks.
 */
class HangWatch(private val limitMs: Long = HANG_MS) {
    // When the first frame the decoder has not taken arrived; NONE while it keeps up.
    private var waitingSince = NONE

    /** A frame arrived for the decoder at [nowMs], before it is fed; returns whether the decoder hangs. */
    fun frameArrived(nowMs: Long): Boolean {
        if (waitingSince == NONE) waitingSince = nowMs
        return nowMs - waitingSince >= limitMs
    }

    /** The decoder took a frame. */
    fun inputTaken() {
        waitingSince = NONE
    }

    /** Hang limits. */
    companion object {
        /** No input taken for this long while frames wait means the decoder hangs. */
        const val HANG_MS = 5_000L
        private const val NONE = Long.MIN_VALUE
    }
}

/**
 * Bounds how often a session replaces a failed or hung decoder: at most
 * [max] times within [windowMs]. A decoder that fails again at once would
 * otherwise be rebuilt for good while the screen stays frozen; an occasional
 * failure in a long session never comes near the limit.
 */
class RebuildLimit(private val max: Int = MAX_REBUILDS, private val windowMs: Long = WINDOW_MS) {
    private val times = ArrayDeque<Long>()

    /** Counts a rebuild at [nowMs]; returns false, counting nothing, once the limit is reached. */
    @Synchronized
    fun allow(nowMs: Long): Boolean {
        while (times.isNotEmpty() && nowMs - times.first() >= windowMs) times.removeFirst()
        if (times.size >= max) return false
        times.addLast(nowMs)
        return true
    }

    /** Rebuild limits. */
    companion object {
        /** The most rebuilds within [WINDOW_MS]. */
        const val MAX_REBUILDS = 10

        /** Five minutes. */
        const val WINDOW_MS = 5 * 60_000L
    }
}
