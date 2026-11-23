package com.desklink.client.net

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

/** The same cases as client/delay_test.go. */
class HostClockTest {
    private val base = 1_795_000_000_000_000L

    @Test
    fun measuresDelayAcrossClocks() {
        val c = HostClock()
        // This clock is 250 ms ahead of the host's; trips take 3 ms each way,
        // one keepalive was held up 20 ms in a queue.
        val ahead = 250_000L
        assertNull(c.sinceMicros(base, base))
        listOf(0L, 20_000L, 0L).forEachIndexed { i, queued ->
            val sent = base + i * 2_000_000L
            c.keepalive(sent, sent + ahead + 3_000 + queued)
            c.roundTrip(6_000 + queued)
        }
        assertEquals(ahead, c.offsetMicros())
        // A frame the host captured at 10 s is shown here 21 ms later.
        val captured = base + 10_000_000L
        assertEquals(21_000L, c.sinceMicros(captured, captured + ahead + 21_000))
        assertNull(c.sinceMicros(captured, captured + 60_000_000L))
    }

    @Test
    fun forgetsOldKeepalives() {
        val c = HostClock()
        c.keepalive(base, base) // before the host's clock was stepped 1 s back
        c.roundTrip(0)
        repeat(HostClock.WINDOW) { i ->
            val sent = base + i * 1_000_000L
            c.keepalive(sent, sent + 1_000_000L)
        }
        assertEquals(1_000_000L, c.offsetMicros())
    }

    @Test
    fun delayMeterAveragesAPeriod() {
        val m = DelayMeter()
        assertNull(m.take())
        m.add(10_000)
        m.add(20_000)
        assertEquals(15_000L, m.take())
        assertNull(m.take())
    }
}
