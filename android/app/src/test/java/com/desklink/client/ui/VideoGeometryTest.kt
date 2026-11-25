package com.desklink.client.ui

import com.desklink.client.input.Zoom
import com.desklink.client.model.LevelInfo
import com.desklink.client.model.MonitorInfo
import com.desklink.client.model.ScreenMode
import com.desklink.client.model.SessionInfo
import com.desklink.client.model.StreamSize
import com.desklink.client.model.streamSizeFor
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class VideoGeometryTest {
    @Test
    fun theStreamTakesTheViewsSizeInPixels() {
        assertEquals(StreamSize(2560, 1600), streamSizeFor(2560, 1600))
        assertEquals("portrait after a rotation", StreamSize(1600, 2560), streamSizeFor(1600, 2560))
        assertEquals("Samsung's FHD+ power saving", StreamSize(1920, 1200), streamSizeFor(1920, 1200))
    }

    @Test
    fun anOddSideIsMadeEven() {
        assertEquals(StreamSize(1080, 2340), streamSizeFor(1081, 2341))
    }

    @Test
    fun theSizeStaysWithinWhatTheHostAccepts() {
        assertEquals("a long side shrinks both alike", StreamSize(4096, 2560), streamSizeFor(6400, 4000))
        assertEquals(StreamSize(320, 320), streamSizeFor(200, 100))
    }

    @Test
    fun aViewWithoutASizeAsksNothing() {
        assertNull(streamSizeFor(0, 1600))
    }

    @Test
    fun aStreamOfTheViewsSizeLandsPixelForPixel() {
        assertEquals(VideoRect(0, 0, 2560, 1600), videoRect(2560, 1600, 2560, 1600))
    }

    @Test
    fun aStreamALittleSmallerKeepsItsSizeWithABorder() {
        // An odd view gets an even stream one pixel smaller; a 1920x1200
        // stream from an older host on a 2000x1250 window grows only 4 %.
        assertEquals(VideoRect(0, 0, 2558, 1598), videoRect(2559, 1599, 2558, 1598))
        assertEquals(VideoRect(40, 25, 1920, 1200), videoRect(2000, 1250, 1920, 1200))
    }

    @Test
    fun aMuchSmallerStreamFillsTheView() {
        assertEquals(VideoRect(0, 0, 2560, 1600), videoRect(2560, 1600, 1920, 1200))
    }

    @Test
    fun aLandscapeStreamOnAPortraitViewIsLetterboxed() {
        // Mirroring after a rotation, or the own screen until the host has
        // started it again at the new size.
        assertEquals(VideoRect(0, 780, 1600, 1000), videoRect(1600, 2560, 2560, 1600))
    }

    @Test
    fun theStatsShowWhereTheDecodersOutputLands() {
        val placed = VideoRect(0, 0, 2560, 1600)
        val encoded = StreamSize(2560, 1600)
        val shown = shownRect(placed, StreamSize(2560, 1600), Zoom())
        assertEquals(placed, shown)
        assertNull("1:1", shownScale(shown, encoded))
        // Before surfaceChanged reports, the placed size stands in.
        assertEquals(placed, shownRect(placed, null, Zoom()))
    }

    @Test
    fun theStatsShowAScaleWhenTheScreenResamples() {
        val letterboxed = shownRect(VideoRect(0, 780, 1600, 1000), StreamSize(1600, 1000), Zoom())
        assertEquals(0.625, shownScale(letterboxed, StreamSize(2560, 1600))!!, 1e-9)
        val zoomed = shownRect(VideoRect(0, 0, 2560, 1600), StreamSize(2560, 1600), Zoom(2f, -0.5f, -0.25f))
        assertEquals(VideoRect(-1280, -400, 5120, 3200), zoomed)
        assertEquals(2.0, shownScale(zoomed, StreamSize(2560, 1600))!!, 1e-9)
    }

    @Test
    fun theCaptureSizeIsTheOwnScreensModeOrTheMirroredMonitors() {
        val own = info(ScreenMode.OWN).copy(width = 1920, height = 1200, levels = listOf(LevelInfo(2560, 1600, 120), LevelInfo(1920, 1200, 60)))
        assertEquals(StreamSize(2560, 1600), captureSize(own))
        val monitors = listOf(MonitorInfo("DP-5", 2560, 1440, 60.0, primary = true), MonitorInfo("eDP-1", 2560, 1600, 120.0))
        assertEquals(StreamSize(2560, 1440), captureSize(info(ScreenMode.MIRROR).copy(monitors = monitors, mirroring = "DP-5")))
        assertNull("a host that does not list its monitors", captureSize(info(ScreenMode.MIRROR)))
    }

    @Test
    fun aRotationIsAskedForOnceItHasHeld() {
        var now = 0L
        val resizer = ScreenResizer(800) { now }
        val landscape = StreamSize(2560, 1600)
        val portrait = StreamSize(1600, 2560)
        resizer.viewChanged(portrait)
        now = 500
        assertNull("still turning", resizer.due(landscape))
        now = 800
        assertEquals(portrait, resizer.due(landscape))
        now = 2000
        assertNull("asked once while the host starts the screen again", resizer.due(landscape))
        assertNull("the new screen", resizer.due(portrait))
    }

    @Test
    fun aRotationTurnedBackAsksNothing() {
        var now = 0L
        val resizer = ScreenResizer(800) { now }
        val landscape = StreamSize(2560, 1600)
        resizer.viewChanged(StreamSize(1600, 2560))
        now = 300
        resizer.viewChanged(landscape)
        now = 2000
        assertNull(resizer.due(landscape))
    }

    @Test
    fun eachSizeOnTheWayRestartsTheWait() {
        var now = 0L
        val resizer = ScreenResizer(800) { now }
        val landscape = StreamSize(2560, 1600)
        resizer.viewChanged(StreamSize(2000, 2560))
        now = 600
        resizer.viewChanged(StreamSize(1600, 2560))
        now = 1000
        assertNull(resizer.due(landscape))
        now = 1400
        assertEquals(StreamSize(1600, 2560), resizer.due(landscape))
    }

    @Test
    fun aSizeTheHostRefusedIsAskedAgainOnlyAfterTheViewChanged() {
        var now = 0L
        val resizer = ScreenResizer(800) { now }
        val landscape = StreamSize(2560, 1600)
        val portrait = StreamSize(1600, 2560)
        resizer.viewChanged(portrait)
        now = 1000
        assertEquals(portrait, resizer.due(landscape))
        // The host kept the landscape screen; the tablet turns back and again.
        resizer.viewChanged(landscape)
        assertNull(resizer.due(landscape))
        resizer.viewChanged(portrait)
        now = 2000
        assertEquals(portrait, resizer.due(landscape))
    }

    private fun info(screen: String) = SessionInfo(sessionId = 1, udpPort = 9444, codec = "av1", width = 2560, height = 1600, fps = 120, qp = 18, screen = screen)
}
