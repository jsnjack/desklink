package com.desklink.client.ui

import kotlin.math.roundToInt

/** One look at the panel: its current mode's refresh rate, its highest mode's, and whether power saving is on. */
data class PanelState(val hz: Float, val maxHz: Float, val powerSave: Boolean)

/**
 * Judges the rate the panel shows during a session, from a sample of
 * [PanelState] every half second. The app asks for the highest rate while
 * streaming, but power saving or Samsung's "Standard" motion smoothness keep
 * the panel lower, and frames above that rate are then encoded, sent and
 * decoded for nothing. When the panel offers no faster mode at all, its
 * highest mode's rate is what it shows. Otherwise a lower current rate
 * counts as held only from samples taken within [touchMs] of a touch or with
 * power saving on, because Samsung's adaptive mode idles low while nothing
 * on screen moves and comes back up at a touch; such samples must show it
 * for [holdMs], no two of them more than [maxGapMs] apart, so low samples
 * minutes apart do not add up. Any sample at the highest rate ends the
 * verdict at once.
 */
class PanelRateJudge(
    private val holdMs: Long = HOLD_MS,
    private val touchMs: Long = TOUCH_MS,
    private val minSamples: Int = MIN_SAMPLES,
    private val maxGapMs: Long = MAX_GAP_MS,
) {
    private var lowSince = NONE
    private var lastLow = NONE
    private var lowSamples = 0
    private var peakHz = 0f

    /** The rate something holds the panel at below its highest mode, in whole hertz; 0 while it is not held. */
    var heldHz = 0
        private set

    /**
     * Takes the sample [s] at [nowMs]; [lastTouchMs] is when the screen was
     * last touched, on the same clock. Returns the rate the panel shows: the
     * held rate, or else its highest mode's.
     */
    fun sample(nowMs: Long, s: PanelState, lastTouchMs: Long): Int {
        if (s.hz >= s.maxHz - TOLERANCE_HZ) {
            lowSince = NONE
            heldHz = 0
            return shown(s)
        }
        // Idle samples neither confirm nor refute: the panel may just be resting.
        if (!s.powerSave && nowMs - lastTouchMs > touchMs) return shown(s)
        if (lowSince == NONE || nowMs - lastLow > maxGapMs) {
            lowSince = nowMs
            lowSamples = 0
            peakHz = 0f
        }
        lastLow = nowMs
        lowSamples++
        peakHz = maxOf(peakHz, s.hz)
        if (nowMs - lowSince >= holdMs && lowSamples >= minSamples) heldHz = peakHz.roundToInt()
        return shown(s)
    }

    private fun shown(s: PanelState): Int = if (heldHz > 0) heldHz else s.maxHz.roundToInt()

    /** Judging limits. */
    companion object {
        /** How long touched or power-saving samples must show a low rate. */
        const val HOLD_MS = 3_000L

        /** A sample this soon after a touch would show the rate a touch brings. */
        const val TOUCH_MS = 1_000L

        /** The fewest such samples behind a verdict. */
        const val MIN_SAMPLES = 3

        /** Such samples further apart than this start the count again. */
        const val MAX_GAP_MS = 1_000L

        /** 119.88 Hz is the panel's 120 Hz mode. */
        const val TOLERANCE_HZ = 1f
        private const val NONE = Long.MIN_VALUE

        /**
         * The frame-rate cap to ask the host for: the rate the panel shows,
         * [shownHz], when it is below the stream's highest, [streamFps];
         * otherwise 0, no cap.
         */
        fun maxFps(shownHz: Int, streamFps: Int): Int = if (shownHz in 1 until streamFps) shownHz else 0
    }
}
