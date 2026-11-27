package com.desklink.client.input

import com.desklink.client.input.TouchGestureEngine.Action
import com.desklink.client.input.TouchGestureEngine.Point
import com.desklink.client.model.InputEvent
import com.desklink.client.model.InputKind
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class DirectTouchEngineTest {
    private val out = mutableListOf<InputEvent>()
    private var huds = 0
    private val zoom = VideoZoom()
    private val engine = DirectTouchEngine({ out += it }, { huds++ }, slop = 10f, zoom = zoom)

    /** The same, for a host that scrolls like a touchpad. */
    private val pixels = DirectTouchEngine({ out += it }, { huds++ }, slop = 10f, zoom = zoom, scrollPixels = { true })
    private fun touchPx(a: Action, t: Long, vararg pts: Point) = pixels.onTouch(a, pts.toList(), t, 1000, 500)
    private fun px(x: Float, y: Float, finish: Boolean = false) =
        InputEvent.scrollPx((x * InputKind.SCROLL_PX_UNIT).toInt(), (y * InputKind.SCROLL_PX_UNIT).toInt(), finish)
    private val hold = DirectTouchEngine.LONG_PRESS_MS

    // A 1000 x 500 view, so view pixels map to ABS_MAX/1000 and ABS_MAX/500 units.
    private fun touch(a: Action, t: Long, vararg pts: Point) = engine.onTouch(a, pts.toList(), t, 1000, 500)
    private fun p(x: Float, y: Float) = Point(x, y)
    private fun at(x: Float, y: Float) =
        InputEvent.pointer((x / 1000 * InputKind.ABS_MAX).toInt(), (y / 500 * InputKind.ABS_MAX).toInt())
    private fun click(button: Int, x: Float, y: Float) =
        listOf(at(x, y), InputEvent.button(button, true), InputEvent.button(button, false))

    @Test
    fun tapClicksWhereTheFingerTouched() {
        touch(Action.DOWN, 0, p(500f, 250f))
        touch(Action.UP, 80)
        assertEquals(click(InputKind.BUTTON_LEFT, 500f, 250f), out)
    }

    @Test
    fun secondTapNearTheFirstReusesItsPositionForADoubleClick() {
        touch(Action.DOWN, 0, p(500f, 250f))
        touch(Action.UP, 60)
        touch(Action.DOWN, 150, p(506f, 253f))
        touch(Action.UP, 210)
        assertEquals(click(InputKind.BUTTON_LEFT, 500f, 250f) + click(InputKind.BUTTON_LEFT, 500f, 250f), out)
    }

    @Test
    fun pressHeldStillRightClicks() {
        touch(Action.DOWN, 0, p(200f, 100f))
        touch(Action.UP, DirectTouchEngine.LONG_PRESS_MS + 50)
        assertEquals(click(InputKind.BUTTON_RIGHT, 200f, 100f), out)
    }

    @Test
    fun oneFingerDraggedScrollsTheWindowUnderWhereItWentDown() {
        touch(Action.DOWN, 0, p(100f, 100f))
        touch(Action.MOVE, 20, p(100f, 130f))
        touch(Action.MOVE, 40, p(100f, 160f))
        touch(Action.UP, 60)
        assertEquals(at(100f, 100f), out.first())
        val wheel = out.drop(1).filter { it.kind == InputKind.SCROLL }.sumOf { it.y }
        assertEquals("fingers moving down scroll up", (60 * TouchGestureEngine.SCROLL_UNITS_PER_PX).toInt(), wheel)
        assertTrue("scrolling never selects", out.none { it.kind == InputKind.MOUSE_BUTTON })
    }

    // The content stays under the finger: the movement goes in pixels, and
    // lifting ends the scroll so the desk's apps can let a fling run on.
    @Test
    fun aHostThatScrollsLikeATouchpadGetsTheFingersMovementInPixels() {
        touchPx(Action.DOWN, 0, p(100f, 100f))
        touchPx(Action.MOVE, 20, p(100f, 130.5f))
        touchPx(Action.MOVE, 40, p(90f, 150f))
        touchPx(Action.UP, 60)
        assertEquals(listOf(at(100f, 100f), px(0f, 30.5f), px(-10f, 19.5f), px(0f, 0f, finish = true)), out)
    }

    @Test
    fun aPressHeldThenDraggedDragsFromWhereItWentDown() {
        touch(Action.DOWN, 0, p(100f, 100f))
        touch(Action.MOVE, hold + 10, p(130f, 100f))
        touch(Action.MOVE, hold + 30, p(160f, 120f))
        touch(Action.UP, hold + 50)
        assertEquals(
            listOf(
                at(100f, 100f), InputEvent.button(InputKind.BUTTON_LEFT, true), at(130f, 100f),
                at(160f, 120f),
                at(160f, 120f), InputEvent.button(InputKind.BUTTON_LEFT, false),
            ),
            out,
        )
    }

    @Test
    fun smallJitterStaysATap() {
        touch(Action.DOWN, 0, p(100f, 100f))
        touch(Action.MOVE, 20, p(105f, 103f))
        touch(Action.UP, 60)
        assertEquals(click(InputKind.BUTTON_LEFT, 100f, 100f), out)
    }

    @Test
    fun twoFingersScrollTheWindowUnderThem() {
        touch(Action.DOWN, 0, p(100f, 100f))
        touch(Action.POINTER_DOWN, 10, p(100f, 100f), p(200f, 100f))
        touch(Action.MOVE, 20, p(100f, 120f), p(200f, 120f))
        touch(Action.MOVE, 30, p(100f, 130f), p(200f, 130f))
        touch(Action.POINTER_UP, 40, p(100f, 130f))
        touch(Action.UP, 50)
        // The pointer first moves to where the fingers landed, then the wheel follows them.
        assertEquals(at(150f, 100f), out.first())
        val wheel = out.drop(1).filter { it.kind == InputKind.SCROLL }.sumOf { it.y }
        assertEquals((30 * TouchGestureEngine.SCROLL_UNITS_PER_PX).toInt(), wheel)
        assertTrue(out.none { it.kind == InputKind.MOUSE_BUTTON })
    }

    @Test
    fun twoFingerTapRightClicks() {
        touch(Action.DOWN, 0, p(100f, 100f))
        touch(Action.POINTER_DOWN, 10, p(100f, 100f), p(200f, 100f))
        touch(Action.POINTER_UP, 80, p(100f, 100f))
        touch(Action.UP, 90)
        assertEquals(click(InputKind.BUTTON_RIGHT, 150f, 100f), out)
    }

    @Test
    fun threeFingerTapTogglesTheHud() {
        touch(Action.DOWN, 0, p(100f, 100f))
        touch(Action.POINTER_DOWN, 10, p(100f, 100f), p(200f, 100f))
        touch(Action.POINTER_DOWN, 20, p(100f, 100f), p(200f, 100f), p(300f, 100f))
        touch(Action.UP, 120)
        assertEquals(1, huds)
        assertTrue(out.isEmpty())
    }

    @Test
    fun cancelledDragReleasesTheButton() {
        touch(Action.DOWN, 0, p(100f, 100f))
        touch(Action.MOVE, hold + 10, p(150f, 100f))
        out.clear()
        touch(Action.CANCEL, hold + 30)
        assertEquals(listOf(at(150f, 100f), InputEvent.button(InputKind.BUTTON_LEFT, false)), out)
    }

    @Test
    fun aCancelledScrollEnds() {
        touchPx(Action.DOWN, 0, p(100f, 100f))
        touchPx(Action.MOVE, 20, p(100f, 120f))
        out.clear()
        touchPx(Action.CANCEL, 30)
        assertEquals(listOf(px(0f, 0f, finish = true)), out)
    }

    @Test
    fun aHoldLastsUntilTheFingerMovesLiftsOrIsJoined() {
        touch(Action.DOWN, 0, p(100f, 100f))
        touch(Action.MOVE, 100, p(104f, 103f))
        assertTrue("jitter keeps the hold", engine.holdingStill(0))
        assertFalse("another touch's hold", engine.holdingStill(1))
        touch(Action.MOVE, hold + 10, p(140f, 100f))
        assertFalse("a drag is no longer a hold", engine.holdingStill(0))
        touch(Action.UP, hold + 20)
        touch(Action.DOWN, 1000, p(100f, 100f))
        touch(Action.POINTER_DOWN, 1010, p(100f, 100f), p(200f, 100f))
        assertFalse("two fingers are no hold", engine.holdingStill(1000))
        touch(Action.UP, 1020)
        assertFalse("a lifted finger is no hold", engine.holdingStill(1000))
    }

    // A two-finger scroll whose first finger moved before the second landed
    // used to start a left-button drag, selecting text on the page; now the
    // first finger scrolls and the second joins in.
    @Test
    fun aSecondFingerLandingLateStillScrollsWithoutSelecting() {
        touch(Action.DOWN, 0, p(100f, 100f))
        touch(Action.MOVE, 40, p(100f, 130f))
        touch(Action.POINTER_DOWN, 70, p(100f, 135f), p(200f, 135f))
        touch(Action.MOVE, 90, p(100f, 160f), p(200f, 160f))
        touch(Action.MOVE, 110, p(100f, 190f), p(200f, 190f))
        touch(Action.POINTER_UP, 130, p(100f, 190f))
        touch(Action.UP, 140)
        assertTrue(out.none { it.kind == InputKind.MOUSE_BUTTON })
        // 30 px with one finger, then the pair's centre 55 px, without a jump
        // when the second finger landed.
        val wheel = out.filter { it.kind == InputKind.SCROLL }.sumOf { it.y }
        assertEquals((85 * TouchGestureEngine.SCROLL_UNITS_PER_PX).toInt(), wheel)
    }

    @Test
    fun theFingerLeftAfterATwoFingerScrollScrollsOnAndLiftingEndsIt() {
        touchPx(Action.DOWN, 0, p(100f, 100f))
        touchPx(Action.POINTER_DOWN, 10, p(100f, 100f), p(200f, 100f))
        touchPx(Action.MOVE, 20, p(100f, 120f), p(200f, 120f))
        touchPx(Action.POINTER_UP, 30, p(100f, 120f))
        touchPx(Action.MOVE, 40, p(100f, 130f))
        touchPx(Action.UP, 50)
        assertEquals(listOf(at(150f, 100f), px(0f, 20f), px(0f, 10f), px(0f, 0f, finish = true)), out)
    }

    @Test
    fun aSecondFingerDuringADragTurnsItIntoAScroll() {
        touch(Action.DOWN, 0, p(100f, 100f))
        touch(Action.MOVE, hold + 10, p(100f, 130f))
        touch(Action.POINTER_DOWN, hold + 100, p(100f, 130f), p(200f, 130f))
        touch(Action.MOVE, hold + 120, p(100f, 160f), p(200f, 160f))
        touch(Action.UP, hold + 140)
        val release = out.indexOf(InputEvent.button(InputKind.BUTTON_LEFT, false))
        val firstScroll = out.indexOfFirst { it.kind == InputKind.SCROLL }
        assertTrue("the drag is released", release >= 0)
        assertTrue("then the fingers scroll", firstScroll > release)
        assertEquals(1, out.count { it == InputEvent.button(InputKind.BUTTON_LEFT, false) })
    }

    // Three fingers in a row around the view's centre, moved by dx, dy.
    private fun three(dx: Float = 0f, dy: Float = 0f) =
        arrayOf(p(400f + dx, 250f + dy), p(500f + dx, 250f + dy), p(600f + dx, 250f + dy))

    // Three fingers land one after another and move together by dx, dy in two steps.
    private fun threeFingerSwipe(dx: Float, dy: Float) {
        touch(Action.DOWN, 0, p(400f, 250f))
        touch(Action.POINTER_DOWN, 10, p(400f, 250f), p(500f, 250f))
        touch(Action.POINTER_DOWN, 20, *three())
        touch(Action.MOVE, 40, *three(dx / 2, dy / 2))
        touch(Action.MOVE, 60, *three(dx, dy))
        touch(Action.UP, 80)
    }

    private fun keys(vararg codes: Int) =
        codes.map { InputEvent.key(it, true) } + codes.reversed().map { InputEvent.key(it, false) }

    @Test
    fun threeFingersSwipedUpOpenTheOverview() {
        threeFingerSwipe(0f, -150f)
        assertEquals(keys(LinuxKeyCodes.KEY_LEFTMETA), out)
        assertEquals(0, huds)
    }

    @Test
    fun threeFingersSwipedLeftGoToTheNextWorkspace() {
        threeFingerSwipe(-150f, 0f)
        assertEquals(keys(LinuxKeyCodes.KEY_LEFTMETA, LinuxKeyCodes.KEY_PAGEDOWN), out)
        assertEquals(0, huds)
    }

    @Test
    fun threeFingersSwipedRightGoToThePreviousWorkspace() {
        threeFingerSwipe(150f, 0f)
        assertEquals(keys(LinuxKeyCodes.KEY_LEFTMETA, LinuxKeyCodes.KEY_PAGEUP), out)
        assertEquals(0, huds)
    }

    @Test
    fun aLongSwipeSendsItsShortcutOnce() {
        touch(Action.DOWN, 0, p(400f, 250f))
        touch(Action.POINTER_DOWN, 10, *three())
        touch(Action.MOVE, 30, *three(-150f, 0f))
        touch(Action.MOVE, 50, *three(-300f, 0f))
        touch(Action.MOVE, 70, *three(-390f, 0f))
        touch(Action.UP, 90)
        assertEquals(keys(LinuxKeyCodes.KEY_LEFTMETA, LinuxKeyCodes.KEY_PAGEDOWN), out)
    }

    // Three moving fingers used to scroll the desktop.
    @Test
    fun threeFingersMovingShortOfASwipeSendNothing() {
        threeFingerSwipe(0f, 80f)
        assertTrue(out.isEmpty())
        assertEquals(0, huds)
    }

    @Test
    fun aThreeFingerSwipeDownSendsNothing() {
        threeFingerSwipe(0f, 150f)
        assertTrue(out.isEmpty())
        assertEquals(0, huds)
    }

    @Test
    fun aThreeFingerTapWithJitterStillTogglesTheHud() {
        touch(Action.DOWN, 0, p(400f, 250f))
        touch(Action.POINTER_DOWN, 10, p(400f, 250f), p(500f, 250f))
        touch(Action.POINTER_DOWN, 20, *three())
        touch(Action.MOVE, 60, *three(4f, -3f))
        touch(Action.UP, 120)
        assertEquals(1, huds)
        assertTrue(out.isEmpty())
    }

    // Two fingers 200 px apart at the view's centre spread to 400 px: 2x, centred.
    private fun pinchToTwice() {
        touch(Action.DOWN, 0, p(400f, 250f))
        touch(Action.POINTER_DOWN, 10, p(400f, 250f), p(600f, 250f))
        touch(Action.MOVE, 30, p(350f, 250f), p(650f, 250f))
        touch(Action.MOVE, 50, p(300f, 250f), p(700f, 250f))
        touch(Action.POINTER_UP, 70, p(300f, 250f))
        touch(Action.UP, 80)
    }

    // Two fingers move right by dx without changing their distance.
    private fun twoFingerDrag(dx: Float) {
        touch(Action.DOWN, 0, p(400f, 250f))
        touch(Action.POINTER_DOWN, 10, p(400f, 250f), p(600f, 250f))
        touch(Action.MOVE, 30, p(400f + dx / 2, 250f), p(600f + dx / 2, 250f))
        touch(Action.MOVE, 50, p(400f + dx, 250f), p(600f + dx, 250f))
        touch(Action.POINTER_UP, 70, p(400f + dx, 250f))
        touch(Action.UP, 80)
    }

    @Test
    fun aPinchZoomsTheVideoAroundItsCentreAndSendsNothing() {
        zoom.enabled = true
        pinchToTwice()
        assertEquals(Zoom(2f, -0.5f, -0.5f), zoom.current)
        // Not even the right click of a quick two-finger tap.
        assertTrue(out.isEmpty())
    }

    @Test
    fun aPinchOnTheOwnScreenDoesNotZoom() {
        pinchToTwice()
        assertEquals(Zoom(), zoom.current)
    }

    @Test
    fun aTwoFingerDragWhileZoomedPansTheVideoAndSendsNoScroll() {
        zoom.enabled = true
        pinchToTwice()
        twoFingerDrag(125f)
        // The video follows the fingers: 125 of 1000 px is an eighth of the view.
        assertEquals(Zoom(2f, -0.375f, -0.5f), zoom.current)
        assertTrue(out.isEmpty())
    }

    @Test
    fun aTwoFingerScrollAtOneTimesStillScrollsWhereZoomIsPossible() {
        zoom.enabled = true
        touch(Action.DOWN, 0, p(100f, 100f))
        touch(Action.POINTER_DOWN, 10, p(100f, 100f), p(200f, 100f))
        touch(Action.MOVE, 20, p(100f, 120f), p(200f, 120f))
        touch(Action.MOVE, 30, p(100f, 130f), p(200f, 130f))
        touch(Action.POINTER_UP, 40, p(100f, 130f))
        touch(Action.UP, 50)
        val wheel = out.filter { it.kind == InputKind.SCROLL }.sumOf { it.y }
        assertEquals((30 * TouchGestureEngine.SCROLL_UNITS_PER_PX).toInt(), wheel)
        assertEquals(Zoom(), zoom.current)
    }

    @Test
    fun aPinchEndsTheScrollItInterrupts() {
        zoom.enabled = true
        touchPx(Action.DOWN, 0, p(400f, 250f))
        touchPx(Action.MOVE, 20, p(400f, 280f))
        touchPx(Action.POINTER_DOWN, 40, p(400f, 280f), p(600f, 280f))
        touchPx(Action.MOVE, 60, p(350f, 280f), p(650f, 280f))
        touchPx(Action.MOVE, 80, p(300f, 280f), p(700f, 280f))
        touchPx(Action.UP, 100)
        assertEquals(listOf(at(400f, 250f), px(0f, 30f), px(0f, 0f, finish = true)), out)
        assertTrue("the video zoomed", zoom.current.scale > 1f)
    }

    @Test
    fun aTapWhileZoomedAndPannedClicksTheDesktopWhereTheVideoShowsIt() {
        zoom.enabled = true
        pinchToTwice()
        twoFingerDrag(125f)
        touch(Action.DOWN, 1000, p(0f, 0f))
        touch(Action.UP, 1050)
        touch(Action.DOWN, 2000, p(1000f, 500f))
        touch(Action.UP, 2050)
        // At 2x with the video's left edge 0.375 views off screen, the view's
        // left edge shows the video at 0.1875 and its right edge at 0.6875;
        // vertically, centred, it shows 0.25 to 0.75.
        val topLeft = InputEvent.pointer((0.1875f * InputKind.ABS_MAX).toInt(), (0.25f * InputKind.ABS_MAX).toInt())
        val bottomRight = InputEvent.pointer((0.6875f * InputKind.ABS_MAX).toInt(), (0.75f * InputKind.ABS_MAX).toInt())
        val left = listOf(InputEvent.button(InputKind.BUTTON_LEFT, true), InputEvent.button(InputKind.BUTTON_LEFT, false))
        assertEquals(listOf(topLeft) + left + bottomRight + left, out)
    }
}
