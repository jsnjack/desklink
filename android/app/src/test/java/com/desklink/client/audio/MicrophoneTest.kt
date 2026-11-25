package com.desklink.client.audio

import com.desklink.client.audio.MicPolicy.Step
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class MicrophoneTest {
    @Test
    fun captureRunsOnlyWhileTheUserTheHostTheForegroundAndThePermissionAllowIt() {
        assertTrue(MicPolicy.capture(wanted = true, hostOn = true, foreground = true, permitted = true))
        assertFalse("switch off", MicPolicy.capture(wanted = false, hostOn = true, foreground = true, permitted = true))
        assertFalse("host has not made its microphone yet", MicPolicy.capture(wanted = true, hostOn = false, foreground = true, permitted = true))
        assertFalse("host without a microphone", MicPolicy.capture(wanted = true, hostOn = null, foreground = true, permitted = true))
        assertFalse("app in the background", MicPolicy.capture(wanted = true, hostOn = true, foreground = false, permitted = true))
        assertFalse("permission revoked", MicPolicy.capture(wanted = true, hostOn = true, foreground = true, permitted = false))
    }

    @Test
    fun aConnectionAsksForTheMicrophoneOnlyWhenSavedOnAndStillPermitted() {
        assertTrue(MicPolicy.ask(wanted = true, permitted = true))
        assertFalse(MicPolicy.ask(wanted = true, permitted = false))
        assertFalse(MicPolicy.ask(wanted = false, permitted = true))
    }

    @Test
    fun turningTheSwitchOnAsksForThePermissionFirst() {
        assertEquals(Step.ASK_PERMISSION, MicPolicy.toggle(on = true, permitted = false))
        assertEquals(Step.TURN_ON, MicPolicy.answered(granted = true))
        assertEquals(Step.EXPLAIN_REFUSAL, MicPolicy.answered(granted = false))
        assertEquals(Step.TURN_ON, MicPolicy.toggle(on = true, permitted = true))
        assertEquals(Step.TURN_OFF, MicPolicy.toggle(on = false, permitted = false))
    }

    @Test
    fun onlyOpusPacketsGoToTheDesk() {
        val packet = byteArrayOf((31 shl 3).toByte(), 1, 2, 3)
        assertTrue(MicFrames.isPacket(config = false, frame = packet))
        assertFalse("flagged codec config", MicFrames.isPacket(config = true, frame = packet))
        assertFalse("the identification header", MicFrames.isPacket(false, "OpusHead\u0001\u0001".toByteArray(Charsets.US_ASCII)))
        assertFalse("Android's header marker", MicFrames.isPacket(false, "AOPUSHDR\u0013".toByteArray(Charsets.US_ASCII)))
        assertFalse("empty", MicFrames.isPacket(false, ByteArray(0)))
        assertFalse("too large for one datagram", MicFrames.isPacket(false, ByteArray(MicFrames.MAX_FRAME + 1)))
        assertTrue("the largest that fits", MicFrames.isPacket(false, ByteArray(MicFrames.MAX_FRAME)))
    }
}
