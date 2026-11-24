package com.desklink.client.net

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.IOException
import java.net.ConnectException
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.NoRouteToHostException
import java.net.ServerSocket
import java.net.Socket
import java.net.SocketTimeoutException

class ConnectionRefusedTest {
    /** How OkHttp reports a failed connect: its own exception around the socket's. */
    private fun okHttpWrapped(cause: Throwable) = ConnectException("Failed to connect to /192.168.178.11:9443").apply { initCause(cause) }

    @Test
    fun aPortNobodyListensOnIsRefused() {
        val port = ServerSocket(0).use { it.localPort }
        val e = runCatching {
            Socket().use { it.connect(InetSocketAddress(InetAddress.getLoopbackAddress(), port), 2_000) }
        }.exceptionOrNull() ?: error("connecting to a closed port succeeded")
        assertTrue(isConnectionRefused(okHttpWrapped(e)))
    }

    @Test
    fun androidsRefusalMessageIsRecognised() = assertTrue(
        isConnectionRefused(
            okHttpWrapped(
                ConnectException(
                    "failed to connect to /192.168.178.11 (port 9443) from /192.168.178.202 (port 57300) " +
                        "after 10000ms: isConnected failed: ECONNREFUSED (Connection refused)"
                )
            )
        )
    )

    @Test
    fun aRefusalSuppressedBehindAnotherRouteCounts() = assertTrue(
        isConnectionRefused(IOException("all routes failed").apply { addSuppressed(ConnectException("Connection refused")) })
    )

    @Test
    fun aMachineThatDoesNotAnswerIsNotRefusing() {
        assertFalse(isConnectionRefused(okHttpWrapped(SocketTimeoutException("failed to connect after 10000ms"))))
        assertFalse(isConnectionRefused(okHttpWrapped(NoRouteToHostException("Host unreachable"))))
        assertFalse(isConnectionRefused(okHttpWrapped(ConnectException("isConnected failed: ENETUNREACH (Network is unreachable)"))))
    }

    @Test
    fun anAnswerThatFailedLaterIsNotRefusing() =
        assertFalse(isConnectionRefused(IOException("The host answered with HTTP 500.")))
}
