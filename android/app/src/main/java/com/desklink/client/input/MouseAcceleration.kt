package com.desklink.client.input

import com.desklink.client.model.MouseInfo
import kotlin.math.hypot
import kotlin.math.max
import kotlin.math.min

/**
 * Makes a captured mouse move the desk's pointer as a mouse at the desk moves
 * it. GNOME passes a mouse's motion through libinput's acceleration at the
 * desk's speed setting and moves its pointer by the result in logical pixels;
 * the host moves the pointer by the pixels of the client's screen it is sent
 * (docs/desktop.md, Input). So each motion is accelerated as libinput would
 * and multiplied by [MouseInfo.scale], the pixels of the client's screen per
 * logical pixel.
 *
 * A port of libinput 1.32's mouse filter (src/filter-mouse.c, src/filter.c):
 * the adaptive profile with two motion trackers, as libinput uses for a mouse
 * without the velocity-averaging quirk, so the velocity is that of the latest
 * motion, and Simpson's rule over it and the one before; and the flat
 * profile's constant factor. Android does not report a mouse's resolution, so
 * motion counts at libinput's default of 1000 dpi, where a count is a unit.
 */
class MouseAcceleration {
    /** When the last motion happened, in microseconds; none yet. */
    private var lastUs: Long? = null

    /** libinput's last_velocity, in units per microsecond. */
    private var lastVelocity = 0.0

    /**
     * The motion of [dx], [dy] mouse counts that the mouse reported at
     * [timeNs] (one monotonic clock), in pixels of the client's screen for
     * the desk's [mouse] settings; null means GNOME's defaults at scale 1. A
     * sample without motion leaves the velocity alone, as libinput sees no
     * motion for a button.
     */
    fun accelerate(dx: Float, dy: Float, timeNs: Long, mouse: MouseInfo?): Pair<Float, Float> {
        if (dx == 0f && dy == 0f) return 0f to 0f
        val m = mouse ?: MouseInfo()
        val factor = if (m.flat) max(FLAT_MIN, 1 + m.speed) else adaptive(dx, dy, timeNs / 1000, m.speed)
        val k = (factor * m.scale).toFloat()
        return dx * k to dy * k
    }

    /** calculate_acceleration_factor with trackers_velocity over two trackers. */
    private fun adaptive(dx: Float, dy: Float, nowUs: Long, speed: Double): Double {
        val last = lastUs
        lastUs = nowUs
        val distance = hypot(dx.toDouble(), dy.toDouble())
        val velocity = when {
            // Before any motion libinput's tracker is at time 0, long past.
            last == null || nowUs - last > MOTION_TIMEOUT_US -> distance / (MOTION_TIMEOUT_US + 1)
            // Time running backwards.
            last > nowUs -> 0.0
            else -> distance / (nowUs - last + 1)
        }
        val profile = AdaptiveProfile(speed)
        val factor = (profile.factor(velocity) + profile.factor(lastVelocity) +
            4 * profile.factor((lastVelocity + velocity) / 2)) / 6
        lastVelocity = velocity
        return factor
    }

    /** pointer_accel_profile_linear at a speed setting (accelerator_set_speed). */
    internal class AdaptiveProfile(speed: Double) {
        /** Where acceleration starts, in units per microsecond. */
        private val threshold = max(THRESHOLD - 0.25e-3 * speed, MIN_THRESHOLD)
        private val maxFactor = 2.0 + 1.5 * speed
        private val incline = 1.1 + 0.75 * speed

        /** The factor for a velocity in units per microsecond. */
        fun factor(velocityUs: Double): Double {
            val perMs = velocityUs * 1000
            val f = when {
                // Slow motion is slowed down, to 30 % at a standstill, for precision.
                perMs < 0.07 -> 10 * perMs + 0.3
                velocityUs < threshold -> 1.0
                else -> incline * (velocityUs - threshold) * 1000 + 1
            }
            return min(maxFactor, f)
        }
    }

    companion object {
        /** libinput's MOTION_TIMEOUT: a motion after a longer pause counts as slow. */
        private const val MOTION_TIMEOUT_US = 1_000_000L

        /** DEFAULT_THRESHOLD, 0.4 units/ms, and MINIMUM_THRESHOLD, 0.2 units/ms, per microsecond. */
        private const val THRESHOLD = 0.4e-3
        private const val MIN_THRESHOLD = 0.2e-3

        /** The flat profile's smallest factor (filter-flat.c). */
        private const val FLAT_MIN = 0.005
    }
}
