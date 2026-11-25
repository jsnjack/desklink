package com.desklink.client.net

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class AddressChoiceTest {
    @Test
    fun theFirstAnswerWinsWhileUdpWorkedEverywhere() {
        val choice = AddressChoice<String>(emptySet())
        assertEquals("lan", choice.answered("192.168.1.10", "lan"))
    }

    @Test
    fun anAddressWhereUdpFailedWaitsForTheOthers() {
        // The LAN address answers HTTPS first, but UDP failed there; the VPN one carries both.
        val choice = AddressChoice<String>(setOf("192.168.1.10"))
        assertNull(choice.answered("192.168.1.10", "lan"))
        assertEquals("vpn", choice.answered("10.0.0.5", "vpn"))
    }

    @Test
    fun anAddressWhereUdpFailedIsUsedWhenNoOtherAnswers() {
        val choice = AddressChoice<String>(setOf("192.168.1.10", "10.0.0.5"))
        assertNull(choice.answered("192.168.1.10", "lan"))
        assertNull(choice.answered("10.0.0.5", "vpn"))
        assertEquals("lan", choice.last())
    }

    @Test
    fun nothingAnsweredLeavesNoChoice() {
        assertNull(AddressChoice<String>(setOf("192.168.1.10")).last())
    }
}
