package com.desklink.client.model

import com.desklink.client.crypto.PacketCipher
import com.desklink.client.crypto.SessionCrypto
import com.desklink.client.net.HelloBuilder
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
    private val parityDatagram = unhex("444c01060102030400000009000900009420b5b9af4971b0523ba0f7ed9de44b987390c8662819b7d095cb")
    private val audioDatagram = unhex("444c0105010203040000000801020000eee798241dac103901c344001b5d32eb21aafd25ebe2f5c8b43652eb")
    private val micDatagram = unhex("444c0107010203040000000201020000f461a9239d74a8e20db0683c625f9626c439afb818ad0fc9663f07af")
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
    fun decryptsHostParityPacket() {
        val (h, pt) = PacketCipher(keys.hostToClient).open(parityDatagram, parityDatagram.size)!!
        assertEquals(Wire.TYPE_FEC, h.type)
        val p = Wire.parseParity(h, pt)!!
        assertEquals(9, p.frameId)
        assertEquals(1, p.group)
        assertEquals(2, p.stride)
        assertEquals(3, p.count)
        assertEquals(0x0102, p.lenXor)
        assertEquals("xor", String(p.data))
        assertEquals(listOf(1), p.members)
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
    fun sharpHelloMatchesTheHostsVector() {
        // The same string is decoded in host/internal/net/control_test.go (sharpHello).
        val hello = HelloBuilder("av1", StreamPreset.SHARP, audio = false, screen = ScreenMode.AUTO, scale = 0.0, keepAwake = KeepAwake.OFF).build(null)
        assertEquals(
            """{"codec":"av1","width":2560,"height":1600,"fps":120,"qp":12,"auto":false,"fixed":true,"audio":false,"screen":"auto","scale":0.0,"keep_awake":0,"lock_after":false,"clipboard_images":8388608,"mic":false,"tiles":false}""",
            ProtocolJson.encodeToString(Hello.serializer(), hello),
        )
    }

    @Test
    fun aLowLevelIsRememberedAsOneBelowTheTop() {
        val ladder = listOf(
            LevelInfo(2560, 1600, 120), LevelInfo(2560, 1600, 60), LevelInfo(2560, 1600, 30),
            LevelInfo(1920, 1200, 30), LevelInfo(1280, 800, 30),
        )
        fun info(level: Int, auto: Boolean = true) =
            SessionInfo(sessionId = 1, udpPort = 9444, codec = "av1", width = 2560, height = 1600, fps = 120, qp = 12, auto = auto, level = level, levels = ladder)
        assertEquals(ladder[0], levelToRemember(info(0)))
        assertEquals(ladder[1], levelToRemember(info(1)))
        assertEquals("the smallest level keeps the full size", ladder[1], levelToRemember(info(4)))
        assertEquals("manual quality remembers nothing", null, levelToRemember(info(0, auto = false)))
        assertEquals("a level off the ladder remembers nothing", null, levelToRemember(info(5)))
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

    /** The JSON of TestClipboardMessagesMatchSharedVectors in host/internal/net/clipimage_test.go. */
    @Test
    fun clipboardMessagesReadAsTheHostWritesThem() {
        val text = ProtocolJson.decodeFromString(ClipboardText.serializer(), """{"text":"hello"}""")
        assertEquals("hello", text.text)
        assertTrue(!text.isImage)
        val first = ProtocolJson.decodeFromString(ClipboardText.serializer(), """{"text":"","image":"iVBORw0KGgo=","parts":2}""")
        assertTrue(first.isImage)
        assertEquals(0, first.part)
        assertEquals(2, first.parts)
        assertArrayEquals(unhex("89504e470d0a1a0a"), B64.decode(first.image!!))
        val second = ProtocolJson.decodeFromString(ClipboardText.serializer(), """{"text":"","image":"AAAAAA==","part":1,"parts":2}""")
        assertEquals(1, second.part)
        assertArrayEquals(ByteArray(4), B64.decode(second.image!!))
    }

    @Test
    fun clipboardTextStillCarriesItsText() {
        val json = ProtocolJson.encodeToString(ClipboardText.serializer(), ClipboardText("hello"))
        assertTrue(json, json.contains("\"text\":\"hello\"") && !json.contains("image"))
    }

    @Test
    fun theHelloSaysHowLargeAClipboardImageTheTabletTakes() {
        val hello = ProtocolJson.encodeToString(Hello.serializer(), Hello("av1", 2560, 1600, 120, 18, clipboardImages = ClipboardImages.DEFAULT_LIMIT))
        assertTrue(hello, hello.contains("\"clipboard_images\":8388608"))
    }

    @Test
    fun theSessionSaysWhetherTheHostTakesClipboardImages() {
        val base = """"session_id":1,"udp_port":9444,"codec":"av1","width":1920,"height":1200,"fps":60,"qp":22"""
        assertEquals(8388608, ProtocolJson.decodeFromString(SessionInfo.serializer(), "{$base,\"clipboard_images\":8388608}").clipboardImages)
        // Hosts from before clipboard images leave it out: text only.
        assertEquals(0, ProtocolJson.decodeFromString(SessionInfo.serializer(), "{$base}").clipboardImages)
    }

    // host/internal/net/control_test.go (TestScreenGeometryOnTheWire) writes
    // the same JSON; a real session also has a ladder, left out here.
    @Test
    fun aMirroringSessionListsTheMonitorsAndTheOneItMirrors() {
        val info = ProtocolJson.decodeFromString(
            SessionInfo.serializer(),
            """{"session_id":0,"udp_port":0,"host_nonce":null,"codec":"","width":0,"height":0,"fps":0,"qp":0,"display":"",
               "auto":false,"level":0,"screen":"mirror","monitors":[
               {"connector":"DP-5","width":2560,"height":1440,"refresh":59.951,"primary":true},
               {"connector":"eDP-1","width":2560,"height":1600,"refresh":120}],"mirroring":"eDP-1"}""",
        )
        assertEquals(
            listOf(MonitorInfo("DP-5", 2560, 1440, 59.951, primary = true), MonitorInfo("eDP-1", 2560, 1600, 120.0)),
            info.monitors,
        )
        assertEquals("eDP-1", info.mirroring)
    }

    @Test
    fun anOwnScreenLeavesTheMonitorsOut() {
        val info = ProtocolJson.decodeFromString(
            SessionInfo.serializer(),
            """{"session_id":0,"udp_port":0,"codec":"","width":0,"height":0,"fps":0,"qp":0,"display":"","auto":false,"level":0,"screen":"own","scale":2}""",
        )
        assertTrue(info.monitors.isEmpty())
        assertEquals("", info.mirroring)
    }

    @Test
    fun helloNamesTheMonitorToMirrorOnlyWhenChosen() {
        val chosen = ProtocolJson.encodeToString(Hello.serializer(), Hello("av1", 1600, 2560, 120, 18, screen = ScreenMode.MIRROR, monitor = "DP-5"))
        assertTrue(chosen.contains("\"width\":1600,\"height\":2560") && chosen.contains("\"monitor\":\"DP-5\""))
        val unchosen = ProtocolJson.encodeToString(Hello.serializer(), Hello("av1", 2560, 1600, 120, 18))
        assertTrue("no monitor field without a choice", !unchosen.contains("monitor"))
    }

    @Test
    fun mirrorAndResizeMatchTheHost() {
        assertEquals("""{"connector":"DP-5"}""", ProtocolJson.encodeToString(MirrorChange.serializer(), MirrorChange("DP-5")))
        assertEquals("""{"width":1600,"height":2560}""", ProtocolJson.encodeToString(Resize.serializer(), Resize(1600, 2560)))
        assertEquals("mirror", MsgType.MIRROR)
        assertEquals("resize", MsgType.RESIZE)
    }

    @Test
    fun aHostThatTypesTextSaysSoAndOlderHostsDoNot() {
        val base = """"session_id":1,"udp_port":9444,"codec":"av1","width":1920,"height":1200,"fps":60,"qp":22"""
        assertTrue(ProtocolJson.decodeFromString(SessionInfo.serializer(), "{$base,\"keysyms\":true}").keysyms)
        assertEquals(false, ProtocolJson.decodeFromString(SessionInfo.serializer(), "{$base}").keysyms)
    }

    @Test
    fun textToTypeHasTheShapeTheHostReads() {
        // host/internal/net/control_test.go and client_test.go hold the same JSON.
        assertEquals("""{"text":"é😀"}""", ProtocolJson.encodeToString(TypeText.serializer(), TypeText("é😀")))
        assertEquals("""{"text":"\b\bé😀\n"}""", ProtocolJson.encodeToString(TypeText.serializer(), TypeText("\b\bé😀\n")))
        assertEquals("type", MsgType.TYPE)
    }

    @Test
    fun pairingQrParses() {
        val qr = ProtocolJson.decodeFromString(
            PairingQr.serializer(),
            """{"ip":"192.168.178.11","port":9443,"pin":"123456","fingerprint":"ab12"}""",
        )
        assertEquals(PairingQr("192.168.178.11", 9443, "123456", "ab12"), qr)
    }

    @Test
    fun aTakeoverErrorCarriesItsReasonAsTheHostNamesIt() {
        val takeover = ProtocolJson.decodeFromString(
            ErrorInfo.serializer(),
            """{"message":"Laptop connected to this computer and took over the session","reason":"replaced"}""",
        )
        assertEquals(ErrorReason.REPLACED, takeover.reason)
        // Other errors, and hosts from before the reason, leave it out.
        assertEquals("", ProtocolJson.decodeFromString(ErrorInfo.serializer(), """{"message":"scale 9.00 is outside 1–3"}""").reason)
    }

    // Shared with host/internal/net/probe_test.go.
    private val probeKey = ByteArray(32) { it.toByte() }
    private val probeNonce = ByteArray(16) { (0xA0 + it).toByte() }
    private val probeRequest = unhex(
        "444c01090000000000000000000000001061396534656337323765366636346531a0a1a2a3a4a5a6a7a8a9aaabacadaeaf" +
            "90416313342141a0b20df5cb8401b9c5b61122ce86e6ab0fda5b331f5b56d857",
    )
    private val probeReply = unhex(
        "444c0109000000000000000000000000a0a1a2a3a4a5a6a7a8a9aaabacadaeaf" +
            "ed6df4fede51c170bba9536948b78475d37eba7df6aea7615460c0db4ff7e9ce",
    )

    @Test
    fun aUdpProbeMatchesTheHostByteForByte() {
        assertArrayEquals(probeRequest, UdpProbe.request("a9e4ec727e6f64e1", probeKey, probeNonce))
        assertTrue(UdpProbe.checkReply(probeReply, probeReply.size, probeKey, probeNonce))
    }

    @Test
    fun aUdpProbeReplyForAnotherNonceOrKeyIsRejected() {
        assertEquals(false, UdpProbe.checkReply(probeReply, probeReply.size, ByteArray(32) { 9 }, probeNonce))
        assertEquals(false, UdpProbe.checkReply(probeReply, probeReply.size, probeKey, ByteArray(16)))
        val tampered = probeReply.copyOf().also { it[it.size - 1] = (it[it.size - 1].toInt() xor 1).toByte() }
        assertEquals(false, UdpProbe.checkReply(tampered, tampered.size, probeKey, probeNonce))
    }

    @Test
    fun theProtocolVersionSaysWhichSideToUpdate() {
        assertEquals(ControlProtocol.Update.HOST, ControlProtocol.advice(1))
        assertEquals(null, ControlProtocol.advice(ControlProtocol.VERSION))
        assertEquals(ControlProtocol.Update.APP, ControlProtocol.advice(ControlProtocol.VERSION + 1))
        // A host from before the field leaves it out, which reads as 1.
        val old = ProtocolJson.decodeFromString(Health.serializer(), """{"name":"desklinkd","version":"0.28.0","hostname":"h","time_ms":1}""")
        assertEquals(1, old.protocol)
        assertEquals(0, old.udpPort)
    }

    @Test
    fun lockAfterAndSleepTravelAsTheHostNamesThem() {
        val hello = ProtocolJson.encodeToString(Hello.serializer(), Hello("av1", 2560, 1600, 120, 18, keepAwake = KeepAwake.SUSPEND, lockAfter = true))
        assertTrue(hello, hello.contains("\"lock_after\":true") && hello.contains("\"keep_awake\":-2"))
        assertEquals("""{"lock":false}""", ProtocolJson.encodeToString(LockAfterChange.serializer(), LockAfterChange(false)))
    }

    @Test
    fun encryptsMicrophoneFramesExactlyLikeTheHostExpects() {
        val payload = Wire.encodeAudio(0x0102030405060708L, "opus".toByteArray())
        val dg = PacketCipher(keys.clientToHost).seal(Wire.micHeader(0x01020304, 2, 0x0102), payload)
        assertArrayEquals(micDatagram, dg)
    }

    @Test
    fun theMicrophoneCounterWrapsAtSixteenBits() {
        assertEquals(1, Wire.micHeader(1, 1, 0x10001).frameId)
    }

    @Test
    fun theMicrophoneTravelsAsTheHostNamesIt() {
        val hello = ProtocolJson.encodeToString(Hello.serializer(), Hello("av1", 2560, 1600, 120, 18, mic = true))
        assertTrue(hello, hello.contains("\"mic\":true"))
        assertEquals("""{"on":true}""", ProtocolJson.encodeToString(MicChange.serializer(), MicChange(true)))
        val base = """{"session_id":1,"udp_port":9444,"codec":"av1","width":1,"height":1,"fps":60,"qp":20"""
        // A host without the microphone leaves it out; one with it says false while it is off.
        assertNull(ProtocolJson.decodeFromString(SessionInfo.serializer(), "$base}").mic)
        assertEquals(false, ProtocolJson.decodeFromString(SessionInfo.serializer(), "$base,\"mic\":false}").mic)
    }

    /** control_test.go's TestTheFrameRateCapTravelsAsTheTabletSendsIt reads the same JSON. */
    @Test
    fun theFrameRateCapTravelsAsTheHostReadsIt() {
        assertEquals("max_fps", MsgType.MAX_FPS)
        assertEquals("""{"fps":60}""", ProtocolJson.encodeToString(MaxFps.serializer(), MaxFps(60)))
        // Lifting the cap sends 0 rather than leaving the field out.
        assertEquals("""{"fps":0}""", ProtocolJson.encodeToString(MaxFps.serializer(), MaxFps(0)))
    }
}
