package com.desklink.client.net

/** The HUD's last [CAPACITY] one-second [LinkStats] samples, oldest first. */
data class StatsHistory(val samples: List<LinkStats> = emptyList()) {
    /** This history with [sample] added, dropping the oldest beyond [CAPACITY]. */
    operator fun plus(sample: LinkStats): StatsHistory = StatsHistory((samples + sample).takeLast(CAPACITY))

    companion object {
        /** A minute of samples. */
        const val CAPACITY = 60
    }
}
