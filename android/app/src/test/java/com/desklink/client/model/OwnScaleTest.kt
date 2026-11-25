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
        assert(ProtocolJson.decodeFromString(SessionInfo.serializer(), "$own,\"primary\":true}").primary)
        // A host that leaves the field out (Go's omitempty) means not primary.
        assert(!ProtocolJson.decodeFromString(SessionInfo.serializer(), "$own}").primary)
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
