package com.desklink.client.net

import org.junit.Assert.assertEquals
import org.junit.Test

class ForgetAnswerTest {
    @Test
    fun onlyOkConfirmsTheHostForgotTheDevice() = assertEquals(ForgetAnswer.REMOVED, ForgetAnswer.of(200))

    @Test
    fun anAuthenticationFailureLeavesTheRemovalUnconfirmed() =
        assertEquals(ForgetAnswer.UNCONFIRMED, ForgetAnswer.of(401))

    @Test
    fun aHostWithoutTheEndpointIsTooOld() {
        assertEquals(ForgetAnswer.UNSUPPORTED, ForgetAnswer.of(404))
        assertEquals(ForgetAnswer.UNSUPPORTED, ForgetAnswer.of(405))
    }

    @Test
    fun anyOtherStatusFails() {
        assertEquals(ForgetAnswer.FAILED, ForgetAnswer.of(500))
        assertEquals(ForgetAnswer.FAILED, ForgetAnswer.of(204))
    }
}
