package com.desklink.client

import android.annotation.SuppressLint
import android.content.res.Configuration
import android.os.Bundle
import android.view.KeyEvent
import android.view.MotionEvent
import android.view.WindowManager
import android.widget.Toast
import androidx.activity.ComponentActivity
import androidx.activity.compose.BackHandler
import androidx.activity.compose.setContent
import androidx.activity.viewModels
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.WindowInsetsControllerCompat
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.desklink.client.input.InputView
import com.desklink.client.ui.AppViewModel
import com.desklink.client.ui.DeskLinkTheme
import com.desklink.client.ui.HomeScreen
import com.desklink.client.ui.PanelWatch
import com.desklink.client.ui.StreamScreen
import com.desklink.client.input.KeyCapture
import com.desklink.client.input.KeySink

/**
 * Hosts the main page, its settings and the stream. While streaming the window is
 * immersive, keeps the screen on, asks for the display's highest refresh
 * rate and, while the stream is on screen with the HUD closed, sends every
 * hardware key to the host before Android handles it.
 */
class MainActivity : ComponentActivity() {
    private val vm: AppViewModel by viewModels()
    private var inputView: InputView? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        WindowCompat.setDecorFitsSystemWindows(window, false)
        reportWindowSize()
        setContent {
            DeskLinkTheme {
                val session by vm.session.collectAsStateWithLifecycle()
                val message by vm.message.collectAsStateWithLifecycle()
                val hudOpen by vm.hudOpen.collectAsStateWithLifecycle()
                LaunchedEffect(message) {
                    message?.let {
                        Toast.makeText(this@MainActivity, it, Toast.LENGTH_LONG).show()
                        vm.showMessage(null)
                    }
                }
                // Asked again when the panel's fastest mode changes mid-session.
                val highestHz by vm.panelHighestHz.collectAsStateWithLifecycle()
                LaunchedEffect(session != null, highestHz) { setStreaming(session != null) }
                val s = session
                if (s == null) {
                    HomeScreen(vm)
                } else {
                    BackHandler { vm.setHudOpen(!hudOpen) }
                    StreamScreen(vm, s) { inputView = it }
                }
            }
        }
    }

    // Rotation and window resizes arrive here, not as a new activity (configChanges).
    override fun onConfigurationChanged(newConfig: Configuration) {
        super.onConfigurationChanged(newConfig)
        reportWindowSize()
    }

    /**
     * The window's size in physical pixels, which the full-screen stream view
     * takes: it sizes the first hello if the stream screen has not been laid
     * out yet.
     */
    private fun reportWindowSize() {
        val bounds = windowManager.currentWindowMetrics.bounds
        vm.setWindowSize(bounds.width(), bounds.height())
    }

    private fun setStreaming(on: Boolean) {
        val controller = WindowInsetsControllerCompat(window, window.decorView)
        if (on) {
            controller.systemBarsBehavior = WindowInsetsControllerCompat.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE
            controller.hide(WindowInsetsCompat.Type.systemBars())
            window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
            preferHighestRefreshRate()
        } else {
            controller.show(WindowInsetsCompat.Type.systemBars())
            window.clearFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
            window.attributes = window.attributes.apply { preferredDisplayModeId = 0 }
        }
    }

    private fun preferHighestRefreshRate() {
        val best = display?.let(PanelWatch::highestMode) ?: return
        window.attributes = window.attributes.apply { preferredDisplayModeId = best.modeId }
    }

    // Every touch, on the video or the HUD, tells the panel judgement that
    // an adaptive panel would be at its highest rate now.
    override fun dispatchTouchEvent(ev: MotionEvent): Boolean {
        vm.noteTouch(ev.eventTime)
        return super.dispatchTouchEvent(ev)
    }

    /**
     * Hardware keys go to the host first while the stream is on screen with
     * the HUD closed, so Ctrl+W or Esc never reach Android then; the mapper
     * decides ([AppViewModel.keysToDesktop]) and sends a key's release where
     * its press went.
     */
    // androidx.core marks its override of the public Activity method as restricted; overriding it is supported.
    @SuppressLint("RestrictedApi")
    override fun dispatchKeyEvent(event: KeyEvent): Boolean {
        if (vm.mapper.onKeyEvent(event)) return true
        return super.dispatchKeyEvent(event)
    }

    override fun onStart() {
        super.onStart()
        vm.setForeground(true)
    }

    override fun onStop() {
        vm.setForeground(false)
        super.onStop()
    }

    override fun onResume() {
        super.onResume()
        KeyCapture.sink = keySink
    }

    override fun onPause() {
        // Keys stop reaching the session here, so a key released from now on
        // would stay down on the desktop: lift everything first.
        KeyCapture.sink = null
        vm.releaseInput()
        super.onPause()
    }

    /**
     * Physical keys from [com.desklink.client.input.KeyCaptureService], before
     * Android's shortcuts: a press is taken for the desktop while the activity
     * has focus and the stream is on screen with the HUD closed, so Win and
     * Alt+Tab reach it too. [KeyCapture] sends its release the same way.
     */
    private val keySink = object : KeySink {
        override fun wants(event: KeyEvent): Boolean =
            hasWindowFocus() && vm.keysToDesktop() && vm.mapper.isHardwareKeyboard(event)

        override fun send(event: KeyEvent): Boolean = vm.mapper.sendKey(event)
    }

    override fun onKeyShortcut(keyCode: Int, event: KeyEvent): Boolean =
        vm.keysToDesktop() || super.onKeyShortcut(keyCode, event)

    override fun onWindowFocusChanged(hasFocus: Boolean) {
        super.onWindowFocusChanged(hasFocus)
        // Without focus no key reaches the session (captureKey checks it too),
        // so release what the host holds before its release can be missed.
        if (!hasFocus) vm.releaseInput()
        // Android only lets the focused app read the clipboard, so sync on focus.
        if (hasFocus && vm.session.value != null) {
            vm.pushClipboard(onlyIfChanged = true)
            inputView?.let { if (it.captureEnabled) it.requestPointerCapture() }
        }
    }
}
