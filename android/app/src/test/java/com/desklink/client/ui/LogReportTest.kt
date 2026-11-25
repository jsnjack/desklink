package com.desklink.client.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class LogReportTest {
    private val header = listOf("DeskLink 0.28.0 (28000)", "Android 16 (API 36), samsung SM-X710")

    private fun line(i: Int) = "11-25 10:00:00.000  4242  4243 D DeskLink: line $i"

    @Test
    fun aShortLogFollowsTheHeaderWhole() {
        val log = (1..3).joinToString("\n", transform = ::line)
        assertEquals(header.joinToString("\n") + "\n\n" + log + "\n", LogReport.build(header, log))
    }

    @Test
    fun aLongLogKeepsTheNewestLinesWithinTheLimit() {
        val log = (1..1000).joinToString("\n", transform = ::line)
        val report = LogReport.build(header, log, maxBytes = 4096)
        assertTrue(report.toByteArray().size <= 4096)
        assertTrue(report.endsWith(line(1000) + "\n"))
        assertFalse(line(1) + "\n" in report)
        val kept = report.lines().count { it.startsWith("11-25") }
        assertTrue("(${1000 - kept} earlier lines left out)" in report)
    }

    @Test
    fun theLimitCountsUtf8BytesNotCharacters() {
        // Each "€" is three bytes in UTF-8.
        val log = (1..200).joinToString("\n") { "€".repeat(20) + it }
        val report = LogReport.build(header, log, maxBytes = 2048)
        assertTrue(report.toByteArray(Charsets.UTF_8).size <= 2048)
        assertTrue(report.endsWith("€".repeat(20) + "200\n"))
    }

    @Test
    fun aBearerTokenIsMasked() {
        val report = LogReport.build(header, "D DeskLink: Authorization: Bearer abc.DEF-123_xyz sent")
        assertFalse("abc.DEF-123_xyz" in report)
        assertTrue("Authorization: Bearer [removed] sent" in report)
    }

    @Test
    fun aWrappedKeyQuotedByAParseErrorIsMasked() {
        val report = LogReport.build(
            header,
            """E DeskLink: JSON input: [{"id":"1","name":"Desk","wrappedKey":"q0VzZWNyZXQ=","mac":"aa:bb"}]""",
        )
        assertFalse("q0VzZWNyZXQ=" in report)
        assertTrue(""""wrappedKey":"[removed]","mac":"aa:bb"""" in report)
    }

    @Test
    fun logcatBufferMarkersAreLeftOut() {
        val report = LogReport.build(header, "--------- beginning of main\n" + line(1))
        assertFalse("beginning of" in report)
        assertTrue(line(1) in report)
    }

    @Test
    fun anEmptyLogSaysSo() =
        assertTrue("(no DeskLink log lines)" in LogReport.build(header, ""))
}
