package com.desklink.client.audio

import kotlin.math.abs

/**
 * Decides for each decoded sound packet whether to play or drop it, so the
 * sound queued ahead of the speaker, which is how far it lags the picture,
 * stays near [targetMs]. A Wi-Fi hiccup holds packets back and then delivers
 * them at once, and without catching up the queue would stay that much
 * longer for the rest of the session. While the queue stays above the
 * target for [settleMs], packets of silence are dropped, which nobody
 * hears; sound that goes on regardless is caught up after [soundAfterMs], a
 * packet at most every [soundSpacingMs], each a 10 ms skip. More than
 * [maxMs] queued drops a packet at once.
 */
class AudioCatchUp(
    private val targetMs: Long = TARGET_MS,
    private val maxMs: Long = MAX_MS,
    private val settleMs: Long = SETTLE_MS,
    private val soundAfterMs: Long = SOUND_AFTER_MS,
    private val soundSpacingMs: Long = SOUND_SPACING_MS,
) {
    private var aboveSince = NONE
    private var lastSoundDrop = NONE

    /**
     * Whether to play a packet of [packetMs] that is [silent] at [nowMs],
     * with [queuedMs] already queued.
     */
    fun play(nowMs: Long, queuedMs: Long, packetMs: Long, silent: Boolean): Boolean {
        if (queuedMs + packetMs > maxMs) return false
        if (queuedMs <= targetMs) {
            aboveSince = NONE
            return true
        }
        if (aboveSince == NONE) aboveSince = nowMs
        val above = nowMs - aboveSince
        if (above < settleMs) return true
        if (silent) return false
        if (above >= soundAfterMs && (lastSoundDrop == NONE || nowMs - lastSoundDrop >= soundSpacingMs)) {
            lastSoundDrop = nowMs
            return false
        }
        return true
    }

    /** Queue limits. */
    companion object {
        /** The queue to keep: about four packets. */
        const val TARGET_MS = 40L

        /** Above this, new sound is dropped at once. */
        const val MAX_MS = 80L

        /** How long the queue stays above the target before silence is dropped: a burst drains by itself sooner. */
        const val SETTLE_MS = 500L

        /** How long before sound itself is cut to catch up. */
        const val SOUND_AFTER_MS = 2_000L

        /** The least time between two cuts of sound. */
        const val SOUND_SPACING_MS = 250L
        private const val NONE = Long.MIN_VALUE

        /**
         * A 16-bit sample this small is silence: -54 dBFS, below what the
         * tablet's speakers play audibly.
         */
        const val SILENCE = 64

        /** Whether the [size] bytes of 16-bit little-endian PCM in [pcm] are silence. */
        fun isSilent(pcm: ByteArray, size: Int): Boolean {
            var i = 0
            while (i + 1 < size) {
                val sample = (pcm[i].toInt() and 0xFF) or (pcm[i + 1].toInt() shl 8)
                if (abs(sample) >= SILENCE) return false
                i += 2
            }
            return true
        }
    }
}
