package com.desklink.client.input

import com.desklink.client.model.TypeText
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class TextRouteTest {
    @Test
    fun plainAsciiStaysKeyTapsEvenWhenTheHostTypesText() {
        assertFalse(TextRoute.asText(TextEdit(0, "Hello, world!\n"), typesText = true, latched = false))
    }

    @Test
    fun anAccentedLetterGoesAsTextToAHostThatTypesIt() {
        assertTrue(TextRoute.asText(TextEdit(0, "é"), typesText = true, latched = false))
    }

    @Test
    fun aMixedWordGoesWholeAsText() {
        // Splitting "café" into taps and text would let the parts arrive out of order.
        assertTrue(TextRoute.asText(TextEdit(0, "café "), typesText = true, latched = false))
    }

    @Test
    fun emojiAndOtherAlphabetsGoAsText() {
        for (text in listOf("😀", "привет", "日本語", "Grüße")) {
            assertTrue(text, TextRoute.asText(TextEdit(0, text), typesText = true, latched = false))
        }
    }

    @Test
    fun anOlderHostGetsKeyTapsAsBefore() {
        assertFalse(TextRoute.asText(TextEdit(0, "é"), typesText = false, latched = false))
    }

    @Test
    fun aLatchedModifierKeepsKeyTapsForItsShortcut() {
        assertFalse(TextRoute.asText(TextEdit(0, "é"), typesText = true, latched = true))
    }

    @Test
    fun anAsciiControlCharacterWithoutAKeyGoesAsText() {
        // A carriage return has no key of its own; the host presses Return.
        assertFalse(TextRoute.typesAsKeys("a\rb"))
    }

    @Test
    fun deletionsTravelInsideTheTextTheyPrecede() {
        assertEquals("\b\bé", TextRoute.text(TextEdit(2, "é")))
    }

    @Test
    fun longTextIsCutIntoMessagesTheHostAccepts() {
        val chunks = TextRoute.chunks("ä".repeat(TypeText.MAX_CHARS) + "end")
        assertEquals(listOf("ä".repeat(TypeText.MAX_CHARS), "end"), chunks)
    }

    @Test
    fun chunksCountCodePointsAndNeverSplitASurrogatePair() {
        assertEquals(listOf("a😀", "b😀", "c"), TextRoute.chunks("a😀b😀c", max = 2))
    }

    @Test
    fun noTextSendsNoMessage() {
        assertEquals(emptyList<String>(), TextRoute.chunks(""))
    }
}

class ComposingTextTest {
    @Test
    fun aGrowingWordSendsOnlyItsNewCharacters() {
        val c = ComposingText()
        assertEquals(TextEdit(0, "ca"), c.replace("ca"))
        assertEquals(TextEdit(0, "f"), c.replace("caf"))
    }

    @Test
    fun aCorrectionDeletesBackToWhereTheWordsDiffer() {
        val c = ComposingText()
        c.replace("cafe")
        assertEquals(TextEdit(1, "é"), c.replace("café"))
    }

    @Test
    fun anEmojiCountsAsOneCharacterToDelete() {
        val c = ComposingText()
        c.replace("a😀")
        // The two emoji share their first UTF-16 unit; the whole emoji is replaced.
        assertEquals(TextEdit(1, "😃"), c.replace("a😃"))
    }

    @Test
    fun aCommitReplacesTheComposingTextAndEndsIt() {
        val c = ComposingText()
        c.replace("helo")
        assertEquals(TextEdit(1, "lo "), c.commit("hello "))
        assertEquals(TextEdit(0, "x"), c.replace("x"))
    }

    @Test
    fun finishingKeepsWhatWasTyped() {
        val c = ComposingText()
        c.replace("word")
        c.finish()
        assertEquals(TextEdit(0, "next"), c.replace("next"))
    }
}
