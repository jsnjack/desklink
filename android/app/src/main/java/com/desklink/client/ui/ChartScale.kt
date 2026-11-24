package com.desklink.client.ui

import java.util.Locale
import kotlin.math.abs
import kotlin.math.floor
import kotlin.math.log10
import kotlin.math.pow
import kotlin.math.roundToLong

/** Round multiples of a power of ten that a chart's scale may end at. */
private val NICE_STEPS = doubleArrayOf(1.0, 1.2, 1.5, 2.0, 2.5, 3.0, 4.0, 5.0, 6.0, 8.0, 10.0)

/**
 * The top of a chart's scale: the smallest round number (a [NICE_STEPS]
 * multiple of a power of ten) at or above both the largest of [values] and
 * [minimum]. The minimum keeps a quiet metric, such as a loss of 0.1 %, from
 * filling the chart.
 */
internal fun chartCeiling(values: List<Double>, minimum: Double): Double {
    val top = maxOf(values.maxOrNull() ?: 0.0, minimum)
    if (top <= 0.0) return 1.0
    val magnitude = 10.0.pow(floor(log10(top)))
    for (step in NICE_STEPS) {
        val candidate = step * magnitude
        if (candidate >= top * (1 - 1e-9)) return candidate
    }
    return 10 * magnitude
}

/** A scale's top as a label: whole numbers without decimals, others with one. */
internal fun formatScale(v: Double): String =
    if (abs(v - v.roundToLong()) < 1e-6) v.roundToLong().toString() else String.format(Locale.ROOT, "%.1f", v)

/** A point of a chart's line, in the chart's pixels. */
internal data class ChartPoint(val x: Float, val y: Float)

/**
 * Where a chart [width] × [height] pixels wide draws [values], oldest first,
 * as the last of [capacity] one-second slots: the newest sits at the right
 * edge, so the time axis stays put while the history fills. A value is
 * clamped to 0..[ceiling]; a null (unknown) value is a gap in the line.
 */
internal fun chartPoints(values: List<Double?>, capacity: Int, width: Float, height: Float, ceiling: Double): List<ChartPoint?> {
    val step = if (capacity > 1) width / (capacity - 1) else 0f
    val first = capacity - values.size
    return values.mapIndexed { i, v ->
        v?.let {
            val share = (it / ceiling).coerceIn(0.0, 1.0).toFloat()
            ChartPoint(x = (first + i) * step, y = height - share * height)
        }
    }
}
