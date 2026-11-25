package com.desklink.client.model

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test
import java.io.ByteArrayInputStream

class SharedFilesTest {
    @Test
    fun aSharedNameKeepsOnlyItsLastPathElement() {
        assertEquals("report.pdf", SharedNames.clean("report.pdf", "pdf"))
        assertEquals("evil.sh", SharedNames.clean("../../.ssh/evil.sh", null))
        assertEquals("photo.jpg", SharedNames.clean("C:\\Users\\me\\photo.jpg", null))
        assertEquals("Holiday photo.jpg", SharedNames.clean("  Holiday photo.jpg  ", null))
    }

    @Test
    fun aSharedItemWithoutANameGetsOneFromItsType() {
        assertEquals("file.png", SharedNames.clean(null, "png"))
        assertEquals("file.pdf", SharedNames.clean("   ", ".pdf"))
        assertEquals("file", SharedNames.clean("dir/", null))
        assertEquals("file", SharedNames.clean("", ""))
    }

    @Test
    fun sharedTextIsNamedAfterItsSubject() {
        assertEquals("Meeting notes.txt", SharedNames.forText("Meeting notes"))
        assertEquals("todo.txt", SharedNames.forText("todo.txt"))
        assertEquals("b.txt", SharedNames.forText("a/b"))
        assertEquals(SharedNames.TEXT_FALLBACK, SharedNames.forText(null))
        assertEquals(SharedNames.TEXT_FALLBACK, SharedNames.forText("  "))
        assertEquals(SharedNames.MAX_SUBJECT + ".txt".length, SharedNames.forText("x".repeat(500)).length)
    }

    @Test
    fun progressIsAWholePercentOnlyWhenTheSizeIsKnown() {
        assertEquals(0, percentOf(0, 200))
        assertEquals(50, percentOf(100, 200))
        assertEquals(100, percentOf(300, 200))
        assertNull(percentOf(100, -1))
        assertNull(percentOf(0, 0))
    }

    @Test
    fun readAtMostStopsAtTheLimitOrTheEnd() {
        val data = ByteArray(200_000) { it.toByte() }
        assertArrayEquals(data.copyOf(100_001), readAtMost(ByteArrayInputStream(data), 100_001))
        assertArrayEquals(data, readAtMost(ByteArrayInputStream(data), 1_000_000))
    }
}
