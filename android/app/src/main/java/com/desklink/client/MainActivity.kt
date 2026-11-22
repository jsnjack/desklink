package com.desklink.client

import android.annotation.SuppressLint
import android.os.Bundle
import android.view.KeyEvent
import android.view.WindowManager
import android.widget.Toast
import androidx.activity.ComponentActivity
import androidx.activity.compose.BackHandler
import androidx.activity.compose.setContent
import androidx.activity.viewModels
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.dynamicDarkColorScheme
import androidx.compose.material3.dynamicLightColorScheme
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.ui.platform.LocalContext
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.WindowInsetsControllerCompat
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.desklink.client.input.InputView
import com.desklink.client.ui.AppViewModel
import com.desklink.client.ui.ConnectScreen
import com.desklink.client.ui.StreamScreen

/**
 * Hosts the connect screen and the stream. While streaming the window is
 * immersive, keeps the screen on, asks for the display's highest refresh
 * rate and sends every hardware key to the host before Android handles it.
 */
class MainActivity : ComponentActivity() {
    private val vm: AppViewModel by viewModels()
    private var inputView: InputView? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        WindowCompat.setDecorFitsSystemWindows(window, false)
        setContent {
            val dark = isSystemInDarkTheme()
            val ctx = LocalContext.current
            MaterialTheme(colorScheme = if (dark) dynamicDarkColorScheme(ctx) else dynamicLightColorScheme(ctx)) {
                val session by vm.session.collectAsStateWithLifecycle()
                val message by vm.message.collectAsStateWithLifecycle()
                val hudOpen by vm.hudOpen.collectAsStateWithLifecycle()
                LaunchedEffect(message) {
                    message?.let {
                        Toast.makeText(this@MainActivity, it, Toast.LENGTH_LONG).show()
                        vm.showMessage(null)
                    }
                }
                LaunchedEffect(session != null) { setStreaming(session != null) }
                val s = session
                if (s == null) {
                    ConnectScreen(vm)
                } else {
                    BackHandler { vm.setHudOpen(!hudOpen) }
                    StreamScreen(vm, s) { inputView = it }
                }
            }
        }
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
        val d = display ?: return
        val current = d.mode
        val best = d.supportedModes
            .filter { it.physicalWidth == current.physicalWidth && it.physicalHeight == current.physicalHeight }
            .maxByOrNull { it.refreshRate } ?: return
        window.attributes = window.attributes.apply { preferredDisplayModeId = best.modeId }
    }

    /** Hardware keys go to the host first, so Ctrl+W, Alt+Tab or Esc never reach Android while streaming. */
    // androidx.core marks its override of the public Activity method as restricted; overriding it is supported.
    @SuppressLint("RestrictedApi")
    override fun dispatchKeyEvent(event: KeyEvent): Boolean {
        if (vm.session.value != null && !vm.hudOpen.value && vm.mapper.onKeyEvent(event)) return true
        return super.dispatchKeyEvent(event)
    }

    override fun onKeyShortcut(keyCode: Int, event: KeyEvent): Boolean =
        vm.session.value != null && !vm.hudOpen.value || super.onKeyShortcut(keyCode, event)

    override fun onWindowFocusChanged(hasFocus: Boolean) {
        super.onWindowFocusChanged(hasFocus)
        // Android only lets the focused app read the clipboard, so sync on focus.
        if (hasFocus && vm.session.value != null) {
            vm.pushClipboard(onlyIfChanged = true)
            inputView?.let { if (it.captureEnabled) it.requestPointerCapture() }
        }
    }
}
