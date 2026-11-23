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
 * hands each key to [KeyCapture]; only while the stream is on screen does that
 * take it for the desktop, otherwise the key goes on to Android untouched.
 * It reads no window content and gets no accessibility events.
 */
class KeyCaptureService : AccessibilityService() {
    override fun onKeyEvent(event: KeyEvent): Boolean = KeyCapture.route(event)

    override fun onAccessibilityEvent(event: AccessibilityEvent?) = Unit

    override fun onInterrupt() = Unit
}

/** Where [KeyCapture] sends keys: the activity showing the stream. */
interface KeySink {
    /** Whether a key pressed now goes to the desktop. */
    fun wants(event: KeyEvent): Boolean

    /** Sends [event] to the desktop; false when the desktop does not take that key. */
    fun send(event: KeyEvent): Boolean
}

/**
 * Routes keys from [KeyCaptureService] to the session that is on screen, if
 * any. A key's release goes where its press went, so Android never keeps a
 * key down whose release DeskLink took, even when the activity is gone by
 * then. The service calls it on the main thread.
 */
object KeyCapture {
    /** The activity while it is resumed, or null. */
    @Volatile
    var sink: KeySink? = null

    private val split = KeySplit()

    /** Takes a key for the desktop and returns true, or returns false to leave it to Android. */
    fun route(event: KeyEvent): Boolean {
        val s = sink
        return split.route(keyIdentity(event), splitAction(event), { s?.wants(event) == true }) { s?.send(event) == true }
    }

    /** Whether the user has switched the service on in the accessibility settings. */
    fun enabled(context: Context): Boolean {
        val me = ComponentName(context, KeyCaptureService::class.java)
        val am = context.getSystemService(AccessibilityManager::class.java) ?: return false
        return am.getEnabledAccessibilityServiceList(AccessibilityServiceInfo.FEEDBACK_ALL_MASK)
            .any { it.resolveInfo.serviceInfo.let { s -> ComponentName(s.packageName, s.name) == me } }
    }
}
