package com.desklink.client.model

import org.junit.Assert.assertEquals
import org.junit.Test

class OwnScaleTest {
    @Test
    fun stepsByAQuarterWithinTheRange() {
        assertEquals(1.75, OwnScale.step(2.0, -1), 0.0)
        assertEquals(2.0, OwnScale.step(2.0, 1), 0.0)
        assertEquals(1.0, OwnScale.step(1.0, -1), 0.0)
        // A scale between steps moves to the step next to its nearest one.
        assertEquals(1.25, OwnScale.step(1.6, -1), 0.0)
    }

    @Test
    fun onlyTheWholeScalesAreMarkedSharp() {
        assertEquals(listOf(true, false, false, false, true), listOf(1.0, 1.25, 1.5, 1.75, 2.0).map(OwnScale::isSharp))
        // Mutter may round a requested scale a little.
        assertEquals(true, OwnScale.isSharp(1.9999))
    }

    @Test
    fun helloAndSessionInfoCarryTheScale() {
        val hello = ProtocolJson.encodeToString(Hello.serializer(), Hello("av1", 2560, 1600, 120, 18, scale = 1.5))
        assert(hello.contains("\"scale\":1.5")) { hello }
        val info = ProtocolJson.decodeFromString(
            SessionInfo.serializer(),
            """{"session_id":1,"udp_port":9444,"codec":"av1","width":2560,"height":1600,"fps":120,"qp":18,"screen":"own","scale":1.75}""",
        )
        assertEquals(1.75, info.scale, 0.0)
    }

    @Test
    fun primaryTravelsAsTheHostNamesIt() {
        assertEquals("""{"primary":false}""", ProtocolJson.encodeToString(PrimaryChange.serializer(), PrimaryChange(false)))
        val own = """{"session_id":1,"udp_port":9444,"codec":"av1","width":2560,"height":1600,"fps":120,"qp":18,"screen":"own","scale":2"""
        assertEquals(true, ProtocolJson.decodeFromString(SessionInfo.serializer(), "$own,\"primary\":true}").primary)
        assertEquals(false, ProtocolJson.decodeFromString(SessionInfo.serializer(), "$own,\"primary\":false}").primary)
        // A host from before the switch leaves the field out: no switch, rather than one that does nothing.
        assertEquals(null, ProtocolJson.decodeFromString(SessionInfo.serializer(), "$own}").primary)
        // A 0.27.0 host has the switch but leaves primary out while it is false, as here for an own
        // screen beside the desk, so the app hides the switch: the host must be updated with the app.
        val host0270 = """{"session_id":7,"udp_port":9444,"host_nonce":"AAECAwQFBgcICQoLDA0ODw==","codec":"av1",""" +
            """"width":2560,"height":1600,"fps":120,"qp":18,"display":"own 2560x1600 screen at 200%","auto":true,""" +
            """"level":0,"levels":[{"width":2560,"height":1600,"fps":120}],"audio":true,"screen":"own","scale":2}"""
        assertEquals(null, ProtocolJson.decodeFromString(SessionInfo.serializer(), host0270).primary)
    }

    @Test
    fun theDeskSwitchTravelsAsTheHostNamesIt() {
        assertEquals("""{"off":true}""", ProtocolJson.encodeToString(DeskChange.serializer(), DeskChange(true)))
        val own = """{"session_id":1,"udp_port":9444,"codec":"av1","width":2560,"height":1600,"fps":120,"qp":18,"screen":"own","scale":2"""
        assertEquals(true, ProtocolJson.decodeFromString(SessionInfo.serializer(), "$own,\"desk_off\":true}").deskOff)
        assertEquals(false, ProtocolJson.decodeFromString(SessionInfo.serializer(), "$own,\"desk_off\":false}").deskOff)
        // A desk the session found locked, and older hosts: no switch.
        assertEquals(null, ProtocolJson.decodeFromString(SessionInfo.serializer(), "$own}").deskOff)
    }

    @Test
    fun keepAwakeTravelsAsTheHostNamesIt() {
        val hello = ProtocolJson.encodeToString(Hello.serializer(), Hello("av1", 2560, 1600, 120, 18, keepAwake = KeepAwake.UNTIL_RETURN))
        assert(hello.contains("\"keep_awake\":-1")) { hello }
        val change = ProtocolJson.encodeToString(KeepAwakeChange.serializer(), KeepAwakeChange(KeepAwake.FOUR_HOURS))
        assertEquals("""{"minutes":240}""", change)
        val info = ProtocolJson.decodeFromString(
            SessionInfo.serializer(),
            """{"session_id":1,"udp_port":9444,"codec":"av1","width":2560,"height":1600,"fps":120,"qp":18,"keep_awake":60}""",
        )
        assertEquals(KeepAwake.ONE_HOUR, info.keepAwake)
    }
}
