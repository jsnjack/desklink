package com.desklink.client.video

/** One second of the GPU renderer's frames, for the stats overlay and the log. */
data class RenderSecond(
    /** Frames drawn in the second. */
    val frames: Int,
    /** Frames that took longer than the budget. */
    val slowFrames: Int,
    /** Their average render time. */
    val meanMicros: Long,
    /** The longest render time. */
    val maxMicros: Long,
)

/**
 * Decides whether the GPU renderer keeps up, from each frame's render time.
 * Frames are judged a second at a time: a second is slow when more than half
 * of its frames took longer than [budgetNanos], and [slowSeconds] slow
 * seconds in a row exhaust the budget, after which the session draws
 * directly again. A second with fewer than [minFrames] frames (a still
 * desktop sends few) breaks the run, because a GPU woken from idle draws its
 * first frames slowly. The first [warmupFrames] frames are not judged: they
 * include compiling the shaders.
 */
class RenderBudget(
    private val budgetNanos: Long = DEFAULT_BUDGET_NANOS,
    private val slowSeconds: Int = 3,
    private val minFrames: Int = 30,
    private val warmupFrames: Int = 30,
    private val secondNanos: Long = 1_000_000_000L,
) {
    private var seen = 0
    private var start = 0L
    private var frames = 0
    private var slow = 0
    private var total = 0L
    private var longest = 0L
    private var slowRun = 0

    /** Whether the renderer has been too slow for long enough to give up. */
    var exhausted = false
        private set

    /**
     * Takes one frame's render time, measured at [nowNanos]; returns the
     * second it closed, or null while the current second runs.
     */
    fun record(renderNanos: Long, nowNanos: Long): RenderSecond? {
        if (seen < warmupFrames) {
            seen++
            if (seen == warmupFrames) start = nowNanos
            return null
        }
        var closed: RenderSecond? = null
        if (nowNanos - start >= secondNanos) {
            closed = close()
            start = nowNanos
        }
        frames++
        if (renderNanos > budgetNanos) slow++
        total += renderNanos
        if (renderNanos > longest) longest = renderNanos
        return closed
    }

    private fun close(): RenderSecond {
        val second = RenderSecond(frames, slow, if (frames > 0) total / frames / 1000 else 0, longest / 1000)
        slowRun = when {
            frames < minFrames -> 0
            slow * 2 > frames -> slowRun + 1
            else -> 0
        }
        if (slowRun >= slowSeconds) exhausted = true
        frames = 0
        slow = 0
        total = 0
        longest = 0
        return second
    }

    /** Budget defaults. */
    companion object {
        /**
         * Half a frame at the stream's highest rate, 120 fps: drawing adds
         * its time to every frame's delay, so it must stay well inside a
         * frame for the GPU path to show frames as early as the direct path.
         */
        const val DEFAULT_BUDGET_NANOS = 1_000_000_000L / 120 / 2
    }
}
