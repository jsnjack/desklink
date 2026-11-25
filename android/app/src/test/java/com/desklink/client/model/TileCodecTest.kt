package com.desklink.client.model

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

private fun unhex(s: String) = ByteArray(s.length / 2) { s.substring(2 * it, 2 * it + 2).toInt(16).toByte() }

/**
 * The pattern of the shared tile vectors at stream pixel [x], [y], as RGB;
 * host/internal/net/tiles_test.go has the same function.
 */
private fun vectorPixel(x: Int, y: Int): Triple<Int, Int, Int> = when {
    y < 2 -> Triple(28, 28, 28)
    (x + y) % 7 == 0 -> Triple(98, 160, 234)
    x % 5 == 0 -> Triple((30 + x) and 0xff, (40 + y) and 0xff, 200)
    x % 3 == 0 -> Triple((x * 9) and 0xff, (y * 13) and 0xff, (x xor y) and 0xff)
    else -> Triple(36, 31, 49)
}

/** Messages produced by host/internal/net/tiles_test.go. */
class TileCodecTest {
    private val dataVector = unhex(
        "02000046000600020000000500010042000000060000024c789c24914b6a1b4d14857ffd799007210e07eca444e49465" +
            "a7a47e2471bbac063fc017843cf0443168e24943c6f606b4022f2378055a800545469768035e8106996aec43b8d1ac69" +
            "be3edf575df7b7bfb9bbbdcf26535eba33f0c7cf3fbc71e760932b2f83d80bf0268cc0a6508fc49b28f0bc94737b96b3" +
            "fbdbc7c75fec646a3b896dd75a8f0cdc53247e77afd9cb14f661992912efdc86679529e76e0b890fceb1ced473e53e20" +
            "f1c4180f4aa61cbacf486cdc9e07a7ae87c40b5be2cc55e038532edc3e92e72453b0e58e917895a9076b77ca4e6eb276" +
            "f8cfb39b2b07e199d58457ece5ea791dde5a90311eac72e53c6c5a4d687b7015de5b4a6eb27ed80125570e4340fa77b2" +
            "51aee034f42d2837df2c1c80e35cb908dfe0b90c879c18d30a47b69f588713768ab5ac5b2807f17fab896f3c781d5f58" +
            "4a61b2bbb8095685721edfd9c9ea42c155dcb61afbf9ecc73d500ae5307e826713bb1c19338d5f6c3c711623382e948b" +
            "587b70192b4e0c68c553785e5903eb78cc4e69beb63c07bba572204fccd72b15bc969716549aef4eb6c0aa54ce65637d" +
            "ad0ff291b5612be9ac957dd905a5540ec57bb0911d8e0c98ca57785e94a69cc92138b6c9c4a51c7062404b4e6c12bc2a" +
            "1589b51cfd1d00965c1fd2",
    )
    private val frameVector = unhex("01000a000640010203040102030405060708000200030102")
    private val frameAllVector = unhex("0101004600060000000700000000000000090000")

    @Test
    fun decodesTheHostsTileFrame() {
        val f = TileWire.parseFrame(frameVector)!!
        assertEquals(TileGrid(2560, 1600), f.grid)
        assertEquals(0x01020304L, f.seq)
        assertEquals(0x0102030405060708L, f.captureMicros)
        assertFalse(f.all)
        assertArrayEquals(intArrayOf(3, 0x0102), f.changed)
        val all = TileWire.parseFrame(frameAllVector)!!
        assertTrue(all.all)
        assertEquals(7L, all.seq)
        assertEquals(9L, all.captureMicros)
        assertEquals(0, all.changed.size)
    }

    @Test
    fun decodesTheHostsTilesToTheirExactPixels() {
        val d = TileWire.parseData(dataVector)!!
        assertEquals(TileGrid(70, 6), d.grid)
        assertEquals(listOf(1 to 5L, 0 to 6L), d.tiles.map { it.slot to it.seq })
        val cut = d.tiles[0]
        assertEquals(listOf(64, 0, 6, 6), listOf(cut.x, cut.y, cut.w, cut.h))
        for (t in d.tiles) {
            for (y in 0 until t.h) {
                for (x in 0 until t.w) {
                    val (r, g, b) = vectorPixel(t.x + x, t.y + y)
                    val i = (y * t.w + x) * 4
                    val got = listOf(t.rgba[i], t.rgba[i + 1], t.rgba[i + 2], t.rgba[i + 3]).map { it.toInt() and 0xff }
                    assertEquals("tile ${t.slot} pixel ($x,$y)", listOf(r, g, b, 255), got)
                }
            }
        }
    }

    @Test
    fun refusesMalformedMessages() {
        assertNull("a tile_frame cut inside its list", TileWire.parseFrame(frameVector.copyOf(frameVector.size - 1)))
        assertNull("a tile_data cut inside its stream", TileWire.parseData(dataVector.copyOf(dataVector.size - 40)))
        val outside = dataVector.copyOf()
        outside[TileWire.DATA_HEADER + 4] = 0x7f
        assertNull("a tile outside the grid", TileWire.parseData(outside))
        val longer = dataVector.copyOf()
        longer[TileWire.DATA_HEADER + 7] = (longer[TileWire.DATA_HEADER + 7] + 1).toByte()
        assertNull("records that do not match the stream", TileWire.parseData(longer))
        assertNull("the wrong kind", TileWire.parseData(frameVector))
    }

    @Test
    fun decodesQoiRunsAndRefusesOpsOfAnotherSize() {
        // QOI_OP_RGB 1,2,3 then a run of three more pixels.
        val ops = byteArrayOf(0xfe.toByte(), 1, 2, 3, (0xc0 or 2).toByte())
        val out = ByteArray(16)
        assertTrue(TileWire.decodeQoi(ops, 0, ops.size, 4, out))
        assertArrayEquals(byteArrayOf(1, 2, 3, -1, 1, 2, 3, -1, 1, 2, 3, -1, 1, 2, 3, -1), out)
        assertFalse("too few pixels expected", TileWire.decodeQoi(ops, 0, ops.size, 3, out))
        assertFalse("too many pixels expected", TileWire.decodeQoi(ops, 0, ops.size, 5, ByteArray(20)))
        assertFalse("a literal cut short", TileWire.decodeQoi(ops, 0, 3, 1, out))
    }

    @Test
    fun cutsTheGridAtTheStreamsEdge() {
        val g = TileGrid(1920, 1200)
        assertEquals(30, g.cols)
        assertEquals(19, g.rows)
        assertEquals(1152, g.y(30 * 18))
        assertNotNull(g)
    }
}
