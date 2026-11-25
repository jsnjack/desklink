package com.desklink.client.ui

import com.desklink.client.model.HostProfile
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class HostChoiceTest {
    private val office = HostProfile("a", "Office", "10.0.0.5")
    private val home = HostProfile("b", "Home", "192.168.178.20")

    @Test
    fun selectsTheHostChosenLast() {
        assertEquals(home, selectedHost(listOf(office, home), "b"))
    }

    @Test
    fun selectsTheFirstHostBeforeAChoice() {
        assertEquals(office, selectedHost(listOf(office, home), null))
    }

    @Test
    fun selectsTheFirstHostWhenTheChosenOneWasForgotten() {
        assertEquals(office, selectedHost(listOf(office, home), "gone"))
    }

    @Test
    fun selectsNothingWithoutHosts() {
        assertNull(selectedHost(emptyList(), "a"))
    }

    @Test
    fun showsThePortOnlyWhenItIsNotTheDefault() {
        assertEquals("10.0.0.5", shownAddress(office))
        assertEquals("10.0.0.5:9000", shownAddress(office.copy(port = 9000)))
    }

    @Test
    fun bracketsAnIpv6AddressBeforeItsPort() {
        assertEquals("[fd00::5]:9000", shownAddress(office.copy(ip = "fd00::5", port = 9000)))
        assertEquals("fd00::5", shownAddress(office.copy(ip = "fd00::5")))
    }

    @Test
    fun namesTheComputerOnlyWhenItsNameDiffers() {
        assertNull(reportedName("Office", "office"))
        assertEquals("cm", reportedName("Office", "cm"))
        assertNull(reportedName("Office", ""))
    }

    @Test
    fun offersWakeOnlyForAnOfflineHostWithAKnownMac() {
        val withMac = office.copy(mac = "aa:bb:cc:dd:ee:ff")
        assertTrue(offersWake(withMac, HostHealth.Offline("no answer")))
        assertFalse(offersWake(withMac, HostHealth.Online("cm", "0.50.2", 4)))
        assertFalse(offersWake(withMac, HostHealth.Checking))
        assertFalse(offersWake(withMac, null))
        assertFalse(offersWake(office, HostHealth.Offline("no answer")))
    }
}
