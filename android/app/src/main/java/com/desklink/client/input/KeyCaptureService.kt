package com.desklink.client.input

import android.accessibilityservice.AccessibilityService
import android.accessibilityservice.AccessibilityServiceInfo
import android.content.ComponentName
import android.content.Context
import android.view.KeyEvent
import android.view.accessibility.AccessibilityEvent
import android.view.accessibility.AccessibilityManager

/**
 * Receives hardware keyboard keys before Android's own shortcuts, which keep
 * Win, every Win+key combination and Alt+Tab from ever reaching an app. It
 * hands each key to [KeyCapture]; only while a session is on screen does that
 * take it for the desktop, otherwise the key goes on to Android untouched.
 * It reads no window content and gets no accessibility events.
 */
class KeyCaptureService : AccessibilityService() {
    override fun onKeyEvent(event: KeyEvent): Boolean = KeyCapture.route(event)

    override fun onAccessibilityEvent(event: AccessibilityEvent?) = Unit

    override fun onInterrupt() = Unit
}

/** Routes keys from [KeyCaptureService] to the session that is on screen, if any. */
object KeyCapture {
    /** Takes a key for the desktop and returns true, or returns false to leave it to Android. */
    @Volatile
    var sink: ((KeyEvent) -> Boolean)? = null

    fun route(event: KeyEvent): Boolean = sink?.invoke(event) ?: false

    /** Whether the user has switched the service on in the accessibility settings. */
    fun enabled(context: Context): Boolean {
        val me = ComponentName(context, KeyCaptureService::class.java)
        val am = context.getSystemService(AccessibilityManager::class.java) ?: return false
        return am.getEnabledAccessibilityServiceList(AccessibilityServiceInfo.FEEDBACK_ALL_MASK)
            .any { it.resolveInfo.serviceInfo.let { s -> ComponentName(s.packageName, s.name) == me } }
    }
}
