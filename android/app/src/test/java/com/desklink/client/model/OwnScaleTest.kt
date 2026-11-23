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
}
