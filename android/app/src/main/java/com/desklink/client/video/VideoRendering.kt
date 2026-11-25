package com.desklink.client.video

/** How a session draws its video, for the stats overlay. */
sealed interface VideoRendering {
    /** Sharper colours is off: the decoder draws straight onto the screen. */
    data object Direct : VideoRendering

    /**
     * [GuidedRenderer] draws; [second] is its last second of render times,
     * null until one has passed, measured on the GPU when [gpuTimed].
     */
    data class Guided(val second: RenderSecond? = null, val gpuTimed: Boolean = false) : VideoRendering

    /** Sharper colours is on, but the GPU renderer failed or was too slow for this session, for [reason]. */
    data class FellBack(val reason: String) : VideoRendering
}
