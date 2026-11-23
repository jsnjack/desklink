package com.desklink.client.model

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class PairingCodeTest {
    private val fp = "ab".repeat(32)

    private fun code(fingerprint: String?) =
        """{"ip":"192.168.178.11","port":9443,"pin":"123456"""" +
            (fingerprint?.let { ""","fingerprint":"$it"""" } ?: "") + ""","addresses":["10.8.0.2"]}"""

    @Test
    fun aCodeFromDesklinkdPairIsValid() {
        val parsed = PairingCode.parse(code(fp.uppercase()))
        assertEquals(PairingCode.Valid(PairingQr("192.168.178.11", 9443, "123456", fp, listOf("10.8.0.2"))), parsed)
    }

    @Test
    fun aCodeWithoutAFingerprintIsRefused() {
        assertEquals(PairingCode.NoFingerprint, PairingCode.parse(code(null)))
        assertEquals(PairingCode.NoFingerprint, PairingCode.parse(code("")))
        assertEquals(PairingCode.NoFingerprint, PairingCode.parse(code("ab12")))
        assertEquals(PairingCode.NoFingerprint, PairingCode.parse(code("zz".repeat(32))))
    }

    @Test
    fun otherCodesAreNotPairingCodes() {
        assertEquals(PairingCode.Invalid, PairingCode.parse("https://example.com"))
        assertEquals(PairingCode.Invalid, PairingCode.parse("""{"ip":"0.0.0.0","port":9443,"pin":"1","fingerprint":"$fp"}"""))
        assertEquals(PairingCode.Invalid, PairingCode.parse("""{"ip":"10.0.0.1","port":0,"pin":"1","fingerprint":"$fp"}"""))
        assertEquals(PairingCode.Invalid, PairingCode.parse("""{"ip":"10.0.0.1","port":9443,"pin":" ","fingerprint":"$fp"}"""))
    }
}

class PairingPlanTest {
    private val fp = "ab".repeat(32)
    private val other = "cd".repeat(32)
    private val qr = PairingQr("192.168.178.11", 9443, "123456", fp, listOf("10.8.0.2"))
    private var ids = 0
    private fun newId() = "new${ids++}"

    @Test
    fun aHostFoundOnTheNetworkPairsWithTheCodesCertificateWhenItAnnouncedAnother() {
        val announced = NearbyHost("DeskLink on desk", "192.168.178.11", 9443, other)
        val plan = planPairing(emptyList(), qr, announced = announced, newId = ::newId)
        assertTrue(plan.isNew)
        assertEquals(fp, plan.fingerprint)
        assertTrue(plan.announcedDiffers)
        assertFalse(plan.savedDiffers)
        assertEquals("the announced name is not trusted either", "192.168.178.11", plan.profile.name)
        assertEquals(null, plan.profile.fingerprint)
        assertEquals(listOf("192.168.178.11", "10.8.0.2"), plan.profile.candidates())
    }

    @Test
    fun aHostThatAnnouncedTheCodesCertificateIsNotFlagged() {
        val plan = planPairing(emptyList(), qr, announced = NearbyHost("DeskLink on desk", "192.168.178.11", 9443, fp.uppercase()), newId = ::newId)
        assertFalse(plan.announcedDiffers)
        assertEquals("desk", plan.profile.name)
    }

    @Test
    fun theCodesCertificateFindsTheSameMachineOnAnyNetwork() {
        val home = HostProfile("home", "Desk", "192.168.1.5", fingerprint = fp.uppercase(), peerId = "p", wrappedKey = "k")
        val chosen = HostProfile("other", "Other", "10.0.0.9")
        val plan = planPairing(listOf(chosen, home), qr, chosenId = "other", newId = ::newId)
        assertEquals("home", plan.profile.id)
        assertFalse(plan.isNew)
        assertFalse(plan.savedDiffers)
        assertEquals("192.168.178.11", plan.profile.ip)
        assertEquals(listOf("192.168.178.11", "192.168.1.5", "10.8.0.2"), plan.profile.candidates())
    }

    @Test
    fun pairingAgainWithAnotherCertificateSaysSoAndUsesTheCodes() {
        val saved = HostProfile("desk", "Desk", "192.168.178.11", 9000, fingerprint = other, peerId = "p", wrappedKey = "k")
        val plan = planPairing(listOf(saved), qr, chosenId = "desk", newId = ::newId)
        assertEquals("desk", plan.profile.id)
        assertTrue(plan.savedDiffers)
        assertEquals(fp, plan.fingerprint)
        assertEquals(9443, plan.profile.port)
    }

    @Test
    fun aHostAddedByAddressIsPairedByTheCodeForItsAddress() {
        val manual = HostProfile("manual", "Office", "192.168.178.11")
        val plan = planPairing(listOf(manual), qr, newId = ::newId)
        assertEquals("manual", plan.profile.id)
        assertFalse(plan.isNew)
        assertFalse(plan.savedDiffers)
    }
}

class AddressTest {
    @Test
    fun addressesThatCannotReachAHostAreRefused() {
        for (a in listOf("", " ", "0.0.0.0", "0.1.2.3", "127.0.0.1", "224.0.0.251", "239.255.255.250", "255.255.255.255", "240.0.0.1", "300.1.1.1", "::", "::1", "ff02::fb", "[ff02::1]")) {
            assertFalse("'$a' must be refused", usableAddress(a))
        }
        for (a in listOf("192.168.178.11", "10.8.0.2", "100.64.1.2", "fe80::1%wlan0", "2001:db8::1", "desk.lan")) {
            assertTrue("'$a' must be kept", usableAddress(a))
        }
    }

    @Test
    fun aHostKeepsAtMostEightAddressesNewestFirst() {
        val old = (1..10).map { "10.0.0.$it" }
        val p = HostProfile("a", "a", "192.168.1.1", addresses = old)
        assertEquals(HostProfile.MAX_ADDRESSES, p.candidates().size)
        val reached = p.withAddresses("192.168.1.2", listOf("10.9.0.1", "224.0.0.1", "0.0.0.0"))
        assertEquals("192.168.1.2", reached.ip)
        assertEquals(listOf("192.168.1.1", "10.9.0.1") + old.take(5), reached.addresses)
        assertEquals(HostProfile.MAX_ADDRESSES, reached.candidates().size)
    }

    @Test
    fun storedUnusableAddressesAreNotProbed() {
        val p = HostProfile("a", "a", "192.168.1.1", addresses = listOf("255.255.255.255", "10.0.0.1", "239.1.1.1"))
        assertEquals(listOf("192.168.1.1", "10.0.0.1"), p.candidates())
    }
}
