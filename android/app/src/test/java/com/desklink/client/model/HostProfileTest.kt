package com.desklink.client.model

import org.junit.Assert.assertEquals
import org.junit.Test

class HostProfileTest {
    private fun host(id: String, ip: String, fp: String?, vararg others: String) =
        HostProfile(id, id, ip, fingerprint = fp, addresses = others.toList())

    @Test
    fun candidatesStartWithTheLastAnsweringAddress() {
        val p = host("a", "192.168.178.32", "fp", "192.168.1.139", "192.168.178.32", "")
        assertEquals(listOf("192.168.178.32", "192.168.1.139"), p.candidates())
    }

    @Test
    fun reachingAnotherAddressKeepsTheOldOne() {
        val p = host("a", "192.168.178.32", "fp").withAddresses("192.168.1.139", listOf("192.168.1.7", "192.168.1.139"))
        assertEquals("192.168.1.139", p.ip)
        assertEquals(listOf("192.168.178.32", "192.168.1.7"), p.addresses)
    }

    @Test
    fun entriesOfOneMachineBecomeOneHost() {
        val home = host("home", "192.168.178.32", "fp")
        val desktop = host("desk", "192.168.178.11", "other")
        val office = host("office", "192.168.1.139", "fp")
        val unpaired = host("new", "10.0.0.5", null)
        val merged = mergeSameMachine(listOf(home, desktop, office, unpaired))
        assertEquals(listOf("home", "desk", "new"), merged.map { it.id })
        assertEquals(listOf("192.168.178.32", "192.168.1.139"), merged[0].candidates())
    }

    @Test
    fun aNewPairingJoinsTheOlderEntry() {
        val home = host("home", "192.168.178.32", "fp").copy(name = "Laptop", mac = "aa:bb:cc:dd:ee:ff")
        val office = host("office", "192.168.1.139", "FP").copy(peerId = "new", wrappedKey = "k2", mac = null)
        val merged = mergeSameMachine(listOf(home, office), fresh = "office")
        assertEquals(listOf("home"), merged.map { it.id })
        val kept = merged[0]
        assertEquals("Laptop", kept.name)
        assertEquals("new", kept.peerId)
        assertEquals("k2", kept.wrappedKey)
        assertEquals("aa:bb:cc:dd:ee:ff", kept.mac)
        assertEquals(listOf("192.168.1.139", "192.168.178.32"), kept.candidates())
    }

    @Test
    fun pairingCodeAndHealthCarryAddresses() {
        val qr = ProtocolJson.decodeFromString(
            PairingQr.serializer(),
            """{"ip":"192.168.1.7","port":9443,"pin":"123456","fingerprint":"ab","addresses":["192.168.1.139"]}""",
        )
        assertEquals(listOf("192.168.1.139"), qr.addresses)
        val h = ProtocolJson.decodeFromString(Health.serializer(), """{"hostname":"h","addresses":["192.168.1.7","192.168.1.139"]}""")
        assertEquals(2, h.addresses.size)
    }
}
