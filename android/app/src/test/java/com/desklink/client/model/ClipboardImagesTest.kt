package com.desklink.client.model

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

/** The same cases as host/internal/net/clipimage_test.go. */
class ClipboardImagesTest {
    private val part = ClipboardImages.PART_BYTES

    /** A PNG signature followed by size-8 more bytes, as testPNG in Go. */
    private fun testPng(size: Int): ByteArray {
        val signature = byteArrayOf(0x89.toByte(), 'P'.code.toByte(), 'N'.code.toByte(), 'G'.code.toByte(), 0x0D, 0x0A, 0x1A, 0x0A)
        return ByteArray(size) { i -> if (i < signature.size) signature[i] else (i * 7).toByte() }
    }

    @Test
    fun imagesAreSplitInto512KiBParts() {
        val png = testPng(2 * part + 1)
        val parts = ClipboardImages.parts(png)
        assertEquals(listOf(part, part, 1), parts.map { B64.decode(it.image!!).size })
        assertEquals(listOf(0, 1, 2), parts.map { it.part })
        parts.forEach {
            assertEquals(3, it.parts)
            assertEquals("", it.text)
        }
        assertArrayEquals(png, parts.map { B64.decode(it.image!!) }.reduce { a, b -> a + b })
    }

    private class Case(val name: String, val limit: Int, val feed: List<ClipboardText>, val want: ByteArray?, val dropped: Int)

    @Test
    fun theAssemblerJoinsPartsInOrderAndDropsEverythingElse() {
        val small = testPng(100)
        val three = testPng(2 * part + 100)
        val other = testPng(part + 5)
        val notPng = ByteArray(20) { 1 }
        val threeParts = ClipboardImages.parts(three)
        val limit = ClipboardImages.DEFAULT_LIMIT
        val cases = listOf(
            Case("a one-part image comes back at once", limit, ClipboardImages.parts(small), small, 0),
            Case("a three-part image comes back after its last part", limit, threeParts, three, 0),
            Case("a new first part starts the image over", limit, threeParts.take(2) + ClipboardImages.parts(other), other, 0),
            Case("a skipped part drops the image", limit, listOf(threeParts[0], threeParts[2]), null, 1),
            Case("parts after a dropped image wait for the next first part", limit, listOf(threeParts[0], threeParts[2], threeParts[1]), null, 2),
            Case("a part without its first part is dropped", limit, threeParts.drop(1), null, 2),
            Case(
                "a changed part count drops the image", limit,
                listOf(threeParts[0], ClipboardText(image = B64.encode(three.copyOfRange(part, 2 * part)), part = 1, parts = 2)), null, 1,
            ),
            Case("more parts than the limit allows drop the image at once", part, ClipboardImages.parts(other).take(1), null, 1),
            Case("more bytes than the limit drop the image", part + 1, ClipboardImages.parts(other), null, 1),
            Case("an image that is not a PNG is dropped", limit, ClipboardImages.parts(notPng), null, 1),
        )
        for (c in cases) {
            val a = ImageAssembler(c.limit)
            var got: ByteArray? = null
            var dropped = 0
            c.feed.forEachIndexed { i, p ->
                got = null
                when (val r = a.add(p)) {
                    is ImagePart.Done -> {
                        assertEquals("${c.name}: image came back at part $i", c.feed.size - 1, i)
                        got = r.png
                    }
                    is ImagePart.Dropped -> dropped++
                    ImagePart.Pending -> Unit
                }
            }
            if (c.want == null) assertNull(c.name, got) else assertArrayEquals(c.name, c.want, got)
            assertEquals(c.name, c.dropped, dropped)
        }
    }

    @Test
    fun aPartThatIsNotBase64DropsTheImage() {
        val a = ImageAssembler(ClipboardImages.DEFAULT_LIMIT)
        assertEquals(ImagePart.Dropped("part 0 is not base64"), a.add(ClipboardText(image = "not base64!", part = 0, parts = 1)))
    }
}
