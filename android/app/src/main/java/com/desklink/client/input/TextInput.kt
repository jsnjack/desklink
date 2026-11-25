package com.desklink.client.input

import com.desklink.client.model.TypeText

/** One soft-keyboard edit: delete [delete] characters before the cursor, then insert [text]. */
data class TextEdit(val delete: Int, val text: String)

/**
 * The soft keyboard's composing text: a word it builds and may change while
 * it is typed. The host has no composing region of its own, so each change
 * becomes an edit of backspaces and new characters. Deletions count code
 * points, so an emoji is one character, as the desktop's Backspace sees it.
 */
class ComposingText {
    private var current = ""

    /** The edit that turns the composing text into [text], which becomes the composing text. */
    fun replace(text: String): TextEdit {
        // commonPrefixWith never ends inside a surrogate pair.
        val keep = current.commonPrefixWith(text).length
        val edit = TextEdit(current.codePointCount(keep, current.length), text.substring(keep))
        current = text
        return edit
    }

    /** The edit that commits [text] in place of the composing text, which then ends. */
    fun commit(text: String): TextEdit = replace(text).also { current = "" }

    /** Ends the composing text, keeping what it typed. */
    fun finish() {
        current = ""
    }
}

/**
 * How soft-keyboard text reaches the host. Plain ASCII goes as key taps on a
 * US layout, as it always has: taps travel with the other keys over UDP. Any
 * other text goes, when the host types text, as a `type` message that the
 * host types as keysyms, so it comes out right on any host layout that has
 * the characters; older hosts get the taps, and characters without a key are
 * lost there.
 */
object TextRoute {
    /** Whether every character of [text] is plain ASCII with a key on a US layout. */
    fun typesAsKeys(text: String): Boolean = text.all { it.code < 0x80 && LinuxKeyCodes.forChar(it) != null }

    /**
     * Whether [edit] goes as one `type` message rather than key taps: only to
     * a host that [typesText], and never while a modifier is [latched], whose
     * shortcut needs a key.
     */
    fun asText(edit: TextEdit, typesText: Boolean, latched: Boolean): Boolean =
        typesText && !latched && !typesAsKeys(edit.text)

    /**
     * The text of the `type` message for [edit]: a backspace character for each
     * deletion, which the host presses as BackSpace, then the text. Keeping the
     * deletions in the message stops them from overtaking it or falling behind
     * it, as separate key taps over UDP could.
     */
    fun text(edit: TextEdit): String = "\b".repeat(edit.delete) + edit.text

    /** [text] cut into messages of at most [max] code points, never inside a surrogate pair. */
    fun chunks(text: String, max: Int = TypeText.MAX_CHARS): List<String> {
        val out = ArrayList<String>()
        var start = 0
        while (start < text.length) {
            val end = if (text.codePointCount(start, text.length) <= max) text.length else text.offsetByCodePoints(start, max)
            out += text.substring(start, end)
            start = end
        }
        return out
    }
}
