package com.desklink.client.model

/** What may need the app lock's check before it goes ahead: see [AppLock.asks]. */
enum class AppLockTrigger {
    /** The user taps Connect on a host, which also wakes a host that does not answer. */
    CONNECT,

    /** A running session connects again by itself, or for the HUD's screen switch. */
    RECONNECT,

    /** The user turns the setting off. */
    TURN_OFF,
}

/**
 * The optional fingerprint or PIN check before connecting. Connecting unlocks
 * the desk without its password, so with the setting on, whoever holds the
 * unlocked tablet must still prove to Android that it is theirs.
 */
object AppLock {
    /**
     * Whether [trigger] asks for a fingerprint or the device's PIN first.
     * Connect asks, and so does turning the setting off, which would otherwise
     * undo it without a check. A running session's reconnects do not: the
     * Connect that started the session already asked. Without a screen lock
     * ([deviceSecure] false) Android has no credential to ask for, so the
     * setting does not apply.
     */
    fun asks(enabled: Boolean, deviceSecure: Boolean, trigger: AppLockTrigger): Boolean =
        enabled && deviceSecure && trigger != AppLockTrigger.RECONNECT
}
