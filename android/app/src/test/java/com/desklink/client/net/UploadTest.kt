package com.desklink.client.net

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Test

class UploadTest {
    @Test
    fun theUploadUrlCarriesTheNameIntact() {
        for (name in listOf("report.pdf", "Holiday photo 1.jpg", "a&b=c.txt", "dir/evil.txt", "Grüße – 日本.png", "1+1.txt", "50%.txt")) {
            val url = uploadUrl("192.168.178.11", 9443, name)
            assertEquals(name, "https://192.168.178.11:9443/files", url.newBuilder().query(null).build().toString())
            assertEquals(name, name, url.queryParameter("name"))
            assertEquals(name, listOf("name"), url.queryParameterNames.toList())
        }
    }

    @Test
    fun separatorsAndSpacesArePercentEncoded() {
        val query = uploadUrl("10.0.0.2", 9443, "a b/c&d+e.txt").encodedQuery!!
        assertEquals("name=a%20b%2Fc%26d%2Be.txt", query)
        assertFalse(query.contains(' '))
    }

    @Test
    fun onlyOkMeansTheHostSavedTheFile() = assertEquals(UploadAnswer.SAVED, UploadAnswer.of(200))

    @Test
    fun eachRefusalHasItsOwnAnswer() {
        assertEquals(UploadAnswer.REJECTED, UploadAnswer.of(401))
        assertEquals(UploadAnswer.BUSY, UploadAnswer.of(409))
        assertEquals(UploadAnswer.TOO_LARGE, UploadAnswer.of(413))
        assertEquals(UploadAnswer.DISK_FULL, UploadAnswer.of(507))
    }

    @Test
    fun aHostWithoutTheEndpointIsTooOld() {
        assertEquals(UploadAnswer.UNSUPPORTED, UploadAnswer.of(404))
        assertEquals(UploadAnswer.UNSUPPORTED, UploadAnswer.of(405))
    }

    @Test
    fun anyOtherStatusFails() {
        assertEquals(UploadAnswer.FAILED, UploadAnswer.of(500))
        assertEquals(UploadAnswer.FAILED, UploadAnswer.of(204))
    }
}
