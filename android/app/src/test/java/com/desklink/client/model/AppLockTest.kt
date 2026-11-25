package com.desklink.client.model

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class AppLockTest {
    @Test
    fun connectAsksWhenTheSettingIsOn() =
        assertTrue(AppLock.asks(enabled = true, deviceSecure = true, trigger = AppLockTrigger.CONNECT))

    @Test
    fun connectGoesAheadWhenTheSettingIsOff() =
        assertFalse(AppLock.asks(enabled = false, deviceSecure = true, trigger = AppLockTrigger.CONNECT))

    @Test
    fun aReconnectInsideARunningSessionDoesNotAskAgain() =
        assertFalse(AppLock.asks(enabled = true, deviceSecure = true, trigger = AppLockTrigger.RECONNECT))

    @Test
    fun turningTheSettingOffAsks() =
        assertTrue(AppLock.asks(enabled = true, deviceSecure = true, trigger = AppLockTrigger.TURN_OFF))

    @Test
    fun withoutAScreenLockNothingAsks() {
        for (trigger in AppLockTrigger.entries) {
            assertFalse(trigger.name, AppLock.asks(enabled = true, deviceSecure = false, trigger = trigger))
        }
    }
}
