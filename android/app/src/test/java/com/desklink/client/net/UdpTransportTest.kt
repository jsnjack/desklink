package com.desklink.client.net

import com.desklink.client.crypto.PacketCipher
import com.desklink.client.crypto.SessionCrypto
import com.desklink.client.model.Header
import com.desklink.client.model.InputEvent
import com.desklink.client.model.InputKind
import com.desklink.client.model.TileFrame
import com.desklink.client.model.Wire
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import org.junit.After
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.InetAddress
import java.net.SocketAddress
import java.util.concurrent.CountDownLatch
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicLong

/** A [UdpTransport] against a fake host on the loopback interface. */
class UdpTransportTest {
    private val sid = 0x1234
    private val keys = SessionCrypto.SessionKeys(ByteArray(32) { 1 }, ByteArray(32) { 2 })
    private val host = DatagramSocket(0, InetAddress.getLoopbackAddress()).apply { soTimeout = 3000 }
    private val fromClient = PacketCipher(keys.clientToHost)
    private val toClient = PacketCipher(keys.hostToClient)
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
    private val ready = CountDownLatch(1)
    private val tilePackets = LinkedBlockingQueue<List<TileFrame>>()
    private val transport = UdpTransport(
        "127.0.0.1", host.localPort, sid.toLong(), keys, {}, scope, onReady = { ready.countDown() },
        tileSink = { tilePackets.add(it) },
    ).also { it.start() }

    @After
    fun tearDown() {
        transport.close()
        scope.cancel()
        host.close()
    }

    private class Received(val header: Header, val payload: ByteArray, val from: SocketAddress)

    private fun receive(): Received {
        val buf = ByteArray(2048)
        while (true) {
            val p = DatagramPacket(buf, buf.size)
            host.receive(p)
            val (h, pt) = fromClient.open(buf, p.length) ?: continue
            return Received(h, pt, p.socketAddress)
        }
    }

    private fun receiveInput(): List<InputEvent> {
        while (true) {
            val r = receive()
            if (r.header.type == Wire.TYPE_INPUT) return Wire.decodeInput(r.payload)
        }
    }

    private fun sendToClient(to: SocketAddress, header: Header, payload: ByteArray) {
        val dg = toClient.seal(header, payload)
        host.send(DatagramPacket(dg, dg.size, to))
    }

    // The keepalive timestamp of the next echo from the client.
    private fun receiveEcho(): Long {
        while (true) {
            val r = receive()
            if (r.header.type == Wire.TYPE_KEEPALIVE && r.header.fragIdx == Wire.KEEPALIVE_ECHO) {
                return Wire.parseKeepalive(r.payload)!!
            }
        }
    }

    @Test
    fun releasesFollowTheInputQueuedBeforeThemThenAnEmptySnapshot() {
        transport.sendInput(InputEvent.key(29, true), InputEvent.key(46, true))
        transport.releaseHeld()
        val pressed = ArrayList<InputEvent>()
        var afterRelease: List<InputEvent>? = null
        while (afterRelease == null) {
            val events = receiveInput()
            if (events.all { it.kind == InputKind.HELD }) {
                if (pressed.size == 4) afterRelease = events
                continue
            }
            pressed += events
        }
        assertEquals(
            listOf(InputEvent.key(29, true), InputEvent.key(46, true), InputEvent.key(46, false), InputEvent.key(29, false)),
            pressed,
        )
        assertEquals(listOf(InputEvent(InputKind.HELD, InputKind.HELD_NONE)), afterRelease)
    }

    /** The next input that is not a held snapshot, and when it arrived. */
    private fun receiveKeys(): Pair<List<InputEvent>, Long> {
        while (true) {
            val events = receiveInput()
            if (events.any { it.kind != InputKind.HELD }) return events to System.nanoTime()
        }
    }

    @Test
    fun textGoesOutBetweenTheKeysAroundItAndHoldsTheLaterOnesBack() {
        val before = listOf(InputEvent.key(14, true), InputEvent.key(14, false))
        val after = listOf(InputEvent.key(30, true), InputEvent.key(30, false))
        val textSentAt = AtomicLong()
        transport.sendInput(*before.toTypedArray())
        transport.sendInOrder { textSentAt.set(System.nanoTime()) }
        transport.sendInput(*after.toTypedArray())
        val (first, _) = receiveKeys()
        val (second, secondAt) = receiveKeys()
        assertEquals(before, first)
        assertEquals(after, second)
        val heldMs = (secondAt - textSentAt.get()) / 1_000_000
        assertTrue("the keys after the text left ${heldMs}ms after it", heldMs >= UdpTransport.TEXT_LEAD_MS)
    }

    @Test
    fun microphoneFramesDoNotWaitBehindText() {
        // Text to type holds the input sender until it is written, which can
        // take longer than the host's microphone buffer waits for a frame.
        val textWritten = CountDownLatch(1)
        transport.sendInOrder { textWritten.await(5, TimeUnit.SECONDS) }
        val sentAt = System.nanoTime()
        transport.sendMicrophone(42, byteArrayOf(1, 2, 3))
        while (true) {
            val r = receive()
            if (r.header.type != Wire.TYPE_MIC) continue
            val waitedMs = (System.nanoTime() - sentAt) / 1_000_000
            assertTrue("the microphone frame waited ${waitedMs}ms behind the text", waitedMs < 1000)
            assertEquals(1, textWritten.count)
            break
        }
        textWritten.countDown()
    }

    @Test
    fun aTilePacketFromTheHostReachesTheTileSink() {
        val client = receive().from
        // Frame 3 of a 128x64 stream changed tile 1; frame 2 changed nothing.
        val packet = byteArrayOf(1, 0, 0, -128, 0, 64, 0, 0, 0, 3, 0, 0, 0, 0, 0, 0, 0, 30, 0, 1, 0, 1) +
            byteArrayOf(1, 0, 0, -128, 0, 64, 0, 0, 0, 2, 0, 0, 0, 0, 0, 0, 0, 20, 0, 0)
        sendToClient(client, Header(Wire.TYPE_TILES, sid, 1, frameId = 3), packet)
        val records = tilePackets.poll(3, TimeUnit.SECONDS)!!
        assertEquals(listOf(3L, 2L), records.map { it.seq })
        assertEquals(listOf(30L, 20L), records.map { it.captureMicros })
        assertArrayEquals(intArrayOf(1), records[0].changed)
    }

    @Test
    fun onlyAnAuthenticatedHostDatagramMakesItReadyAndARepeatIsDropped() {
        // The client speaks first, so the host learns its address.
        val client = receive().from
        sendToClient(client, Header(Wire.TYPE_KEEPALIVE, sid + 1, 1), Wire.encodeKeepalive(1))
        host.send(DatagramPacket(ByteArray(64), 64, client))
        assertFalse(ready.await(300, TimeUnit.MILLISECONDS))
        assertFalse(transport.ready)

        val first = toClient.seal(Header(Wire.TYPE_KEEPALIVE, sid, 1), Wire.encodeKeepalive(111))
        host.send(DatagramPacket(first, first.size, client))
        assertTrue(ready.await(3, TimeUnit.SECONDS))
        assertTrue(transport.ready)
        assertEquals(111L, receiveEcho())

        // The same datagram again, then a new one: only the new one is echoed.
        host.send(DatagramPacket(first, first.size, client))
        sendToClient(client, Header(Wire.TYPE_KEEPALIVE, sid, 2), Wire.encodeKeepalive(222))
        assertEquals(222L, receiveEcho())
    }
}
