package com.desklink.client.ui

import android.content.Context
import android.hardware.display.DisplayManager
import android.os.PowerManager
import android.util.Log
import android.view.Display

/**
 * Reads the tablet's panel: the refresh rate of its current mode, the
 * highest mode at the same resolution, and whether power saving is on. The
 * mode's rate, not Display.getRefreshRate(), is read on purpose: the latter
 * may report a frame-rate override or the rate the app's content is
 * rendered at, which follows the stream's own frame rate and would make a
 * capped stream look like a held panel for good.
 */
class PanelWatch(context: Context) {
    private val displays = context.getSystemService(DisplayManager::class.java)
    private val power = context.getSystemService(PowerManager::class.java)

    /** The panel now, or null when Android does not say. */
    fun sample(): PanelState? = try {
        displays?.getDisplay(Display.DEFAULT_DISPLAY)?.let { d ->
            val mode = d.mode
            val max = highestMode(d)?.refreshRate ?: mode.refreshRate
            PanelState(mode.refreshRate, max, power?.isPowerSaveMode == true)
        }
    } catch (e: RuntimeException) {
        Log.d(TAG, "read the panel's mode: ${e.message}")
        null
    }

    /** Panel helpers shared with the activity and the stream screen. */
    companion object {
        private const val TAG = "DeskLink"

        /** The display's fastest mode at its current resolution, which streaming asks for. */
        fun highestMode(d: Display): Display.Mode? {
            val current = d.mode
            return d.supportedModes
                .filter { it.physicalWidth == current.physicalWidth && it.physicalHeight == current.physicalHeight }
                .maxByOrNull { it.refreshRate }
        }

        /** Every refresh rate the display offers at its current resolution, for the log. */
        fun rates(d: Display): List<Float> {
            val current = d.mode
            return d.supportedModes
                .filter { it.physicalWidth == current.physicalWidth && it.physicalHeight == current.physicalHeight }
                .map { it.refreshRate }.distinct().sorted()
        }
    }
}
