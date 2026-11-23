package com.desklink.client.input

import com.desklink.client.model.InputKind
import kotlin.math.PI
import kotlin.math.atan2
import kotlin.math.cos
import kotlin.math.roundToInt
import kotlin.math.sin

/** Converts the S-Pen's Android axes into the wire's pressure and tilt. */
object PenAxes {
    /** Wire pressure for Android's 0..1 pressure while touching: 1..PRESSURE_MAX, never 0 (0 means unknown). */
    fun pressure(p: Float): Int = (p.coerceIn(0f, 1f) * InputKind.PRESSURE_MAX).roundToInt().coerceIn(1, InputKind.PRESSURE_MAX)

    /**
     * Tilt towards +x and +y in hundredths of a degree, from Android's
     * AXIS_TILT ([tilt], radians from perpendicular) and AXIS_ORIENTATION
     * ([orientation], radians, 0 pointing up, positive clockwise). The same
     * conversion Chromium uses for web pointer events' tiltX and tiltY.
     */
    fun tilt(tilt: Float, orientation: Float): Pair<Int, Int> {
        val t = tilt.toDouble().coerceIn(0.0, PI / 2)
        val o = orientation.toDouble()
        val r = sin(t)
        val z = cos(t)
        val x = Math.toDegrees(atan2(sin(-o) * r, z))
        val y = Math.toDegrees(atan2(cos(-o) * r, z))
        return centi(x) to centi(y)
    }

    private fun centi(deg: Double) = (deg * 100).roundToInt().coerceIn(-InputKind.TILT_MAX, InputKind.TILT_MAX)
}
