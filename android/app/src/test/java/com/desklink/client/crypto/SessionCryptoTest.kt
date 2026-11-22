package com.desklink.client.crypto

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Test

private fun seq(n: Int, start: Int) = ByteArray(n) { (start + it).toByte() }

private fun hex(b: ByteArray) = SessionCrypto.hex(b)

private fun unhex(s: String) = ByteArray(s.length / 2) { s.substring(2 * it, 2 * it + 2).toInt(16).toByte() }

/** The same vectors are asserted in host/internal/crypto/session_test.go. */
class SessionCryptoTest {
    private val clientPub = byteArrayOf(4) + seq(64, 0x40)
    private val hostPub = byteArrayOf(4) + seq(64, 0x80)
    private val fingerprint = "ab" + hex(seq(31, 0))
    private val keys = SessionCrypto.derivePairingKeys(seq(32, 0x10), "123456", clientPub, hostPub, fingerprint)

    @Test
    fun pairingPeerKeyMatchesHost() =
        assertEquals("b2e174e2435c70e3a5dcb935c9de25e1d49f29da6d961e1fb336a5fe8f132cd6", hex(keys.peerKey))

    @Test
    fun clientProofMatchesHost() =
        assertEquals("6fd9aa9945c6784d2433920c4899807fafd3756b0e6c95d49c483848a754cd63", hex(keys.clientProof()))

    @Test
    fun hostProofMatchesHost() =
        assertEquals("b636fb05e935edbfa59992c09e01afb02c1ef5f56c0ec9189512ebd9552a14d3", hex(keys.hostProof()))

    @Test
    fun authTokenMatchesHost() = assertEquals(
        "0011223344556677.1800000000.bc6503e1012a494178f17a35eef51de0bbcfa9c70ab2b7f603211be7917cabf8",
        SessionCrypto.authToken("0011223344556677", seq(32, 0xA0), 1_800_000_000),
    )

    @Test
    fun sessionKeysMatchHost() {
        val sk = SessionCrypto.deriveSessionKeys(seq(32, 0xA0), 0x01020304, seq(16, 0x30))
        assertEquals("526722d5d437ce84e5fb7ef6514a801596ec1cbf109dea3778ab18cfac7dd503", hex(sk.hostToClient))
        assertEquals("4c57da5db0f9e19d6c83834886a53a30cff31ab99aac9b36264e07feb99647fc", hex(sk.clientToHost))
    }

    @Test
    fun sessionIdAboveSignedRangeIsFormattedUnsigned() {
        val a = SessionCrypto.deriveSessionKeys(seq(32, 1), 0xF0000000L, seq(16, 0))
        val b = SessionCrypto.deriveSessionKeys(seq(32, 1), 0xF0000000L.toInt().toLong(), seq(16, 0))
        assertArrayEquals(a.hostToClient, b.hostToClient)
    }

    @Test
    fun hkdfMatchesRfc5869TestCase1() {
        val okm = SessionCrypto.hkdf(
            ByteArray(22) { 0x0b },
            unhex("000102030405060708090a0b0c"),
            unhex("f0f1f2f3f4f5f6f7f8f9"),
            42,
        )
        assertEquals("3cb25f25faacd57a90434f64d0362f2a2d2d0a90cf1a5a4c5db02d56ecc4c5bf34007208d5b887185865", hex(okm))
    }

    @Test
    fun ecdhAgreesInBothDirections() {
        val a = EphemeralKey()
        val b = EphemeralKey()
        assertEquals(65, a.publicBytes().size)
        assertArrayEquals(a.agree(b.publicBytes()), b.agree(a.publicBytes()))
    }
}
