package com.desklink.client.ui

import android.app.KeyguardManager
import android.content.Context
import android.hardware.biometrics.BiometricManager.Authenticators
import android.hardware.biometrics.BiometricPrompt
import android.os.CancellationSignal
import com.desklink.client.model.AppLock
import com.desklink.client.model.AppLockTrigger

/** Whether the device has a screen lock (PIN, pattern or password) for the prompt to fall back on. */
fun deviceSecure(context: Context): Boolean =
    context.getSystemService(KeyguardManager::class.java)?.isDeviceSecure == true

/**
 * Runs [action] once the app lock lets [trigger] go ahead: at once when
 * [AppLock.asks] says no check is needed, otherwise after the user confirms in
 * Android's own prompt with a strong biometric (a fingerprint) or the
 * device's PIN, pattern or password. [title] heads the prompt. [onError] gets
 * the prompt's error message; cancelling the prompt runs neither.
 */
fun withAppLock(
    context: Context,
    enabled: Boolean,
    trigger: AppLockTrigger,
    title: String,
    onError: (String) -> Unit,
    action: () -> Unit,
) {
    if (!AppLock.asks(enabled, deviceSecure(context), trigger)) return action()
    val callback = object : BiometricPrompt.AuthenticationCallback() {
        override fun onAuthenticationSucceeded(result: BiometricPrompt.AuthenticationResult) = action()

        override fun onAuthenticationError(errorCode: Int, errString: CharSequence) {
            // The user closed the prompt, or Android took it away; neither needs a message.
            if (errorCode == BiometricPrompt.BIOMETRIC_ERROR_USER_CANCELED || errorCode == BiometricPrompt.BIOMETRIC_ERROR_CANCELED) return
            onError(errString.toString())
        }
    }
    try {
        // With the device credential allowed, Android offers the PIN itself, so
        // the prompt must not have a cancel button of its own.
        BiometricPrompt.Builder(context)
            .setTitle(title)
            .setAllowedAuthenticators(Authenticators.BIOMETRIC_STRONG or Authenticators.DEVICE_CREDENTIAL)
            .build()
            .authenticate(CancellationSignal(), context.mainExecutor, callback)
    } catch (e: RuntimeException) {
        onError(e.message ?: e.toString())
    }
}
