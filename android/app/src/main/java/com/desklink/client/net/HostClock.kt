package com.desklink.client.net

/**
 * Estimates how far this device's clock is ahead of the host's, so a frame's
 * capture time, which the host stamps with its clock, can be compared with
 * the time here. Each host keepalive says when the host sent it: its arrival
 * time minus that is the offset plus the trip across. The smallest such gap
 * of recent keepalives is the offset plus the quickest trip, taken as half the
 * quickest recent round trip. This assumes the path is about as fast both
 * ways, as on a LAN or Wi-Fi. The same estimate as the laptop client's
 * (`client/delay.go`), with the same tests.
 */
class HostClock {
    private val gaps = ArrayDeque<Long>()
    private val rtts = ArrayDeque<Long>()

    /** Records a host keepalive sent at [sentMicros] (host clock) that arrived at [arrivedMicros] (this clock). */
    @Synchronized
    fun keepalive(sentMicros: Long, arrivedMicros: Long) = push(gaps, arrivedMicros - sentMicros)

    /** Records the round trip of one of our keepalives. */
    @Synchronized
    fun roundTrip(micros: Long) = push(rtts, micros)

    /** How far this clock is ahead of the host's, or null until a keepalive and a round trip were seen. */
    @Synchronized
    fun offsetMicros(): Long? {
        val gap = gaps.minOrNull() ?: return null
        val rtt = rtts.minOrNull() ?: return null
        return gap - rtt / 2
    }

    /**
     * How long before [nowMicros] (this clock) the host captured a frame it
     * stamped [capturedMicros] (host clock), or null when unknown or not
     * believable.
     */
    fun sinceMicros(capturedMicros: Long, nowMicros: Long): Long? {
        val offset = offsetMicros() ?: return null
        val d = (nowMicros - capturedMicros - offset).coerceAtLeast(0)
        return d.takeIf { it < MAX_BELIEVABLE_DELAY_US }
    }

    private fun push(window: ArrayDeque<Long>, v: Long) {
        if (window.size >= WINDOW) window.removeFirst()
        window.addLast(v)
    }

    companion object {
        /** Recent keepalives and round trips used: one every 2 s, so about half a minute. */
        const val WINDOW = 16

        /** A larger delay means the estimate is off, for example right after a clock jump. */
        const val MAX_BELIEVABLE_DELAY_US = 5_000_000L
    }
}

/** Averages the delays of the frames shown in one stats period. */
class DelayMeter {
    private var sum = 0L
    private var n = 0

    @Synchronized
    fun add(micros: Long) {
        sum += micros
        n++
    }

    /** The average since the last take, or null when no frame was shown. */
    @Synchronized
    fun take(): Long? {
        if (n == 0) return null
        val avg = sum / n
        sum = 0
        n = 0
        return avg
    }
}
