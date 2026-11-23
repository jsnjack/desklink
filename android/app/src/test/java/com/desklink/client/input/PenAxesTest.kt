package com.desklink.client.input

import com.desklink.client.model.InputEvent
import com.desklink.client.model.InputKind
import com.desklink.client.model.Wire
import org.junit.Assert.assertEquals
import org.junit.Test
import kotlin.math.PI

class PenAxesTest {
    @Test
    fun pressureNeverReadsAsUnknown() {
        assertEquals(1, PenAxes.pressure(0f))
        assertEquals(32768, PenAxes.pressure(0.5f))
        assertEquals(InputKind.PRESSURE_MAX, PenAxes.pressure(1.2f))
    }

    @Test
    fun tiltFollowsTheStylusOrientation() {
        assertEquals(0 to 0, PenAxes.tilt(0f, 1f))
        // Pointing up (orientation 0) at 45 degrees from upright.
        assertEquals(0 to 4500, PenAxes.tilt((PI / 4).toFloat(), 0f))
        // Pointing right.
        assertEquals(-4500 to 0, PenAxes.tilt((PI / 4).toFloat(), (PI / 2).toFloat()))
        // Lying flat stays within the range.
        assertEquals(0 to 9000, PenAxes.tilt((PI / 2).toFloat(), 0f))
    }

    /** The same bytes as TestPenRecordsMatchSharedVector in packet_test.go. */
    @Test
    fun penRecordsMatchTheHost() {
        val b = Wire.encodeInput(listOf(InputEvent.penTilt(3000, -1500), InputEvent.pen(InputKind.PEN_TOUCH, 100, 200, 20000)))
        assertEquals("0800000000000bb8fffffa24" + "03014e2000000064000000c8", b.joinToString("") { "%02x".format(it) })
    }
}
