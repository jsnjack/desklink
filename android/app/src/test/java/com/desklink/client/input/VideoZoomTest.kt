package com.desklink.client.input

import com.desklink.client.model.InputKind
import org.junit.Assert.assertEquals
import org.junit.Test

class VideoZoomTest {
    private fun units(f: Float) = (f * InputKind.ABS_MAX).toInt()

    @Test
    fun atOneTimesTouchesMapStraightAcrossTheVideo() {
        val z = Zoom()
        assertEquals((500f / 1000 * InputKind.ABS_MAX).toInt(), z.absX(500f, 1000))
        assertEquals((123f / 500 * InputKind.ABS_MAX).toInt(), z.absY(123f, 500))
    }

    @Test
    fun touchesMapThroughZoomAndPan() {
        // 2x, with the video's left edge 0.375 views and its top 0.5 views off screen.
        val z = Zoom(2f, -0.375f, -0.5f)
        assertEquals(units(0.1875f), z.absX(0f, 1000))
        assertEquals(units(0.6875f), z.absX(1000f, 1000))
        assertEquals(units(0.25f), z.absY(0f, 500))
        assertEquals(units(0.5f), z.absY(250f, 500))
    }

    @Test
    fun aPinchKeepsTheVideoUnderItsCentre() {
        val z = Zoom().moved(0.25f, 0.75f, 0.25f, 0.75f, 2f)
        assertEquals(Zoom(2f, -0.25f, -0.75f), z)
        // The point under the centre before is still under it.
        assertEquals(Zoom().absX(250f, 1000), z.absX(250f, 1000))
        assertEquals(Zoom().absY(375f, 500), z.absY(375f, 500))
    }

    @Test
    fun theVideoFollowsTheFingersCentre() {
        val z = Zoom(2f, -0.5f, -0.5f).moved(0.5f, 0.5f, 0.625f, 0.25f, 1f)
        assertEquals(Zoom(2f, -0.375f, -0.75f), z)
    }

    @Test
    fun zoomStaysBetweenOneAndFourTimes() {
        assertEquals(Zoom.MAX_SCALE, Zoom().moved(0.5f, 0.5f, 0.5f, 0.5f, 10f).scale)
        assertEquals(Zoom(), Zoom(2f, -0.5f, -0.5f).moved(0.5f, 0.5f, 0.5f, 0.5f, 0.1f))
    }

    @Test
    fun theVideoAlwaysCoversTheView() {
        val z = Zoom(2f, -0.5f, -0.5f)
        assertEquals(Zoom(2f, 0f, 0f), z.moved(0.5f, 0.5f, 1.5f, 1.5f, 1f))
        assertEquals(Zoom(2f, -1f, -1f), z.moved(0.5f, 0.5f, -1f, -1f, 1f))
    }

    @Test
    fun aZoomJustAboveOneTimesShowsTheWholeVideo() {
        assertEquals(Zoom(), Zoom().moved(0.3f, 0.3f, 0.3f, 0.3f, 1.03f))
    }

    @Test
    fun pinchesDoNotZoomWhileZoomIsOff() {
        val zoom = VideoZoom()
        zoom.show(Zoom(2f, -0.5f, -0.5f))
        assertEquals(Zoom(), zoom.current)
    }

    @Test
    fun turningZoomOffShowsTheWholeVideo() {
        val zoom = VideoZoom()
        zoom.enabled = true
        zoom.show(Zoom(2f, -0.5f, -0.5f))
        zoom.enabled = false
        assertEquals(Zoom(), zoom.current)
    }

    @Test
    fun resetShowsTheWholeVideo() {
        val zoom = VideoZoom()
        zoom.enabled = true
        zoom.show(Zoom(3f, -1f, -2f))
        zoom.reset()
        assertEquals(Zoom(), zoom.state.value)
    }
}
