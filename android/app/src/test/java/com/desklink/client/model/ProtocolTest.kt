package com.desklink.client.model

import com.desklink.client.crypto.PacketCipher
import com.desklink.client.crypto.SessionCrypto
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

private fun seq(n: Int, start: Int) = ByteArray(n) { (start + it).toByte() }

private fun unhex(s: String) = ByteArray(s.length / 2) { s.substring(2 * it, 2 * it + 2).toInt(16).toByte() }

/** Datagrams produced by host/internal/net/packet_test.go. */
class ProtocolTest {
    private val keys = SessionCrypto.deriveSessionKeys(seq(32, 0xA0), 0x01020304, seq(16, 0x30))
    private val videoDatagram = unhex("444c01010102030400000007000902039d3f934b64d90a4f8f306b8387320d754c027b29287f9f8652654cb535fcaa7369")
    private val audioDatagram = unhex("444c0105010203040000000801020000eee798241dac103901c344001b5d32eb21aafd25ebe2f5c8b43652eb")
    private val inputDatagram = unhex("444c0102010203040000000100000000a8d7d153404684a969382be34dddec2de4f7f39bd5d7fa30d6b4d12c")

    @Test
    fun decryptsHostVideoFragment() {
        val (h, pt) = PacketCipher(keys.hostToClient).open(videoDatagram, videoDatagram.size)!!
        assertEquals(Wire.TYPE_VIDEO, h.type)
        assertEquals(0x01020304, h.sessionId)
        assertEquals(7, h.seq)
        val f = Wire.parseVideo(h, pt)!!
        assertEquals(9, f.frameId)
        assertEquals(0x0102, f.index)
        assertEquals(0x0203, f.count)
        assertTrue(f.keyframe)
        assertEquals(0x0102030405060708L, f.captureMicros)
        assertEquals("hello", String(f.data, f.offset, f.length))
    }

    @Test
    fun decryptsHostAudioPacket() {
        val (h, pt) = PacketCipher(keys.hostToClient).open(audioDatagram, audioDatagram.size)!!
        assertEquals(Wire.TYPE_AUDIO, h.type)
        assertEquals(8, h.seq)
        val a = Wire.parseAudio(h, pt)!!
        assertEquals(0x0102, a.counter)
        assertEquals(0x0102030405060708L, a.captureMicros)
        assertEquals("opus", String(a.data))
        assertNull(Wire.parseAudio(h, ByteArray(Wire.AUDIO_PREFIX)))
    }

    @Test
    fun encryptsInputExactlyLikeTheHost() {
        val payload = Wire.encodeInput(listOf(InputEvent.move(-5, 300)))
        val dg = PacketCipher(keys.clientToHost).seal(Header(Wire.TYPE_INPUT, 0x01020304, 1), payload)
        assertArrayEquals(inputDatagram, dg)
    }

    @Test
    fun rejectsTamperedDatagram() {
        val bad = videoDatagram.copyOf().also { it[10] = (it[10].toInt() xor 1).toByte() }
        assertNull(PacketCipher(keys.hostToClient).open(bad, bad.size))
    }

    @Test
    fun rejectsDatagramForTheOtherDirection() {
        assertNull(PacketCipher(keys.clientToHost).open(videoDatagram, videoDatagram.size))
    }

    @Test
    fun headerRoundTrip() {
        val h = Header(Wire.TYPE_TELEMETRY, -2, 42, 65535, 3, 9)
        val buf = ByteArray(Wire.HEADER_SIZE)
        h.write(buf)
        assertEquals(h, Header.parse(buf, buf.size))
    }

    @Test
    fun headerRejectsBadMagic() {
        val buf = ByteArray(Wire.HEADER_SIZE)
        Header(Wire.TYPE_VIDEO, 1, 1).write(buf)
        buf[0] = 0
        assertNull(Header.parse(buf, buf.size))
    }

    @Test
    fun inputEventsRoundTrip() {
        val events = listOf(InputEvent.key(29, true), InputEvent.scroll(-120, 60), InputEvent.pen(InputKind.PEN_TOUCH, 65535, 0))
        assertEquals(events, Wire.decodeInput(Wire.encodeInput(events)))
    }

    @Test
    fun telemetryLayout() {
        assertArrayEquals(byteArrayOf(0x02, 0x00, 0x00, 0xFA.toByte(), 1), Wire.encodeTelemetry(512, 250, true))
    }

    @Test
    fun sealedDatagramsStayWithinTheLimit() {
        val dg = PacketCipher(keys.clientToHost).seal(Header(Wire.TYPE_INPUT, 1, 1), ByteArray(Wire.MAX_PLAINTEXT))
        assertEquals(Wire.MAX_DATAGRAM, dg.size)
    }

    @Test
    fun autoHelloCarriesTheStartLevel() {
        val start = LevelInfo(1920, 1200, 60)
        val json = ProtocolJson.encodeToString(Hello.serializer(), Hello("av1", 2560, 1600, 120, 18, auto = true, start = start))
        assertTrue(json.contains("\"auto\":true") && json.contains("\"start\":{\"width\":1920,\"height\":1200,\"fps\":60}"))
        val manual = ProtocolJson.encodeToString(Hello.serializer(), Hello("hevc", 1920, 1200, 60, 20))
        assertTrue("manual hello omits start", !manual.contains("start"))
    }

    @Test
    fun sessionInfoReadsTheLadder() {
        val info = ProtocolJson.decodeFromString(
            SessionInfo.serializer(),
            """{"session_id":4000000000,"udp_port":9444,"codec":"av1","width":1920,"height":1200,"fps":60,"qp":22,
               "auto":true,"level":2,"levels":[{"width":2560,"height":1600,"fps":120},{"width":2560,"height":1600,"fps":60},
               {"width":1920,"height":1200,"fps":60}]}""",
        )
        assertEquals(4_000_000_000L, info.sessionId)
        assertTrue(info.auto)
        assertEquals(LevelInfo(1920, 1200, 60), info.levels[info.level])
    }

    @Test
    fun pairingQrParses() {
        val qr = ProtocolJson.decodeFromString(
            PairingQr.serializer(),
            """{"ip":"192.168.178.11","port":9443,"pin":"123456","fingerprint":"ab12"}""",
        )
        assertEquals(PairingQr("192.168.178.11", 9443, "123456", "ab12"), qr)
    }
}
