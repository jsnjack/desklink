package com.desklink.client.net

import org.junit.Assert.assertEquals
import org.junit.Test
import java.io.IOException
import java.net.ConnectException
import java.net.NoRouteToHostException
import java.net.SocketTimeoutException
import java.security.cert.CertificateException
import javax.net.ssl.SSLHandshakeException

class ProbeRowsTest {
    /** How OkHttp reports a failed connect: its own exception around the socket's. */
    private fun okHttpWrapped(cause: Throwable) = ConnectException("Failed to connect to /192.168.178.11:9443").apply { initCause(cause) }

    private fun answered(ms: Long) = ProbeOutcome.Answered(ms, "desk", "0.28.0")

    @Test
    fun aRefusedConnectionReadsAsRefused() = assertEquals(
        ProbeOutcome.Refused,
        ProbeOutcome.of(okHttpWrapped(ConnectException("isConnected failed: ECONNREFUSED (Connection refused)"))),
    )

    @Test
    fun aConnectTimeoutReadsAsTimedOut() = assertEquals(
        ProbeOutcome.TimedOut,
        ProbeOutcome.of(okHttpWrapped(SocketTimeoutException("failed to connect to /192.168.178.11 (port 9443) after 3000ms"))),
    )

    @Test
    fun androidsTimedOutConnectReadsAsTimedOut() = assertEquals(
        ProbeOutcome.TimedOut,
        ProbeOutcome.of(okHttpWrapped(ConnectException("isConnected failed: ETIMEDOUT (Connection timed out)"))),
    )

    @Test
    fun aCertificateMismatchShowsTheCertificateMessage() {
        val mismatch = CertificateException("Certificate ab12 does not match the paired host.")
        val tls = SSLHandshakeException("java.security.cert.CertificateException: Certificate ab12 does not match the paired host.")
            .apply { initCause(mismatch) }
        assertEquals(ProbeOutcome.Failed("Certificate ab12 does not match the paired host."), ProbeOutcome.of(tls))
    }

    @Test
    fun anUnreachableAddressShowsTheSocketsReason() = assertEquals(
        ProbeOutcome.Failed("Host unreachable"),
        ProbeOutcome.of(okHttpWrapped(NoRouteToHostException("Host unreachable"))),
    )

    @Test
    fun anHttpErrorKeepsItsMessage() = assertEquals(
        ProbeOutcome.Failed("The host answered with HTTP 500."),
        ProbeOutcome.of(IOException("The host answered with HTTP 500.")),
    )

    @Test
    fun rowsKeepTheOrderConnectTriesTheAddresses() {
        val rows = probeRows(listOf("10.0.0.5" to ProbeOutcome.TimedOut, "192.168.178.11" to answered(12), "10.8.0.1" to ProbeOutcome.Refused))
        assertEquals(listOf("10.0.0.5", "192.168.178.11", "10.8.0.1"), rows.map { it.address })
        assertEquals(listOf(ProbeOutcome.TimedOut, answered(12), ProbeOutcome.Refused), rows.map { it.outcome })
    }

    @Test
    fun theQuickestAnswerIsMarkedFastest() {
        val rows = probeRows(listOf("10.8.0.1" to answered(40), "192.168.178.11" to answered(12), "10.0.0.5" to ProbeOutcome.TimedOut))
        assertEquals(listOf(false, true, false), rows.map { it.fastest })
    }

    @Test
    fun ofTwoEquallyQuickAnswersTheEarlierIsMarked() {
        val rows = probeRows(listOf("192.168.178.11" to answered(12), "10.8.0.1" to answered(12)))
        assertEquals(listOf(true, false), rows.map { it.fastest })
    }

    @Test
    fun noAddressIsFastestWhenNoneAnswered() {
        val rows = probeRows(listOf("192.168.178.11" to ProbeOutcome.Refused, "10.8.0.1" to ProbeOutcome.Failed("Host unreachable")))
        assertEquals(listOf(false, false), rows.map { it.fastest })
    }
}
