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
    fun aNewPairingKeepsItsOwnEntry() {
        val merged = mergeSameMachine(listOf(host("home", "192.168.178.32", "fp"), host("office", "192.168.1.139", "fp")), keep = "office")
        assertEquals(listOf("office"), merged.map { it.id })
        assertEquals(listOf("192.168.1.139", "192.168.178.32"), merged[0].candidates())
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
