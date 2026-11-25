package com.desklink.client.model

import java.io.InputStream

/**
 * File names for what another app shares with DeskLink. The host cleans the
 * name it receives itself; the tablet still sends only a base name, so the
 * saved file is called what the user saw.
 */
object SharedNames {
    /** The name used when a shared item has none. */
    const val FALLBACK = "file"

    /** The name of shared text that comes without a subject. */
    const val TEXT_FALLBACK = "Shared text.txt"

    /** The longest name taken from a shared text's subject, before ".txt". */
    const val MAX_SUBJECT = 100

    /**
     * The name to send for a shared item called [display] (OpenableColumns'
     * display name, or the URI's last segment): its last path element,
     * trimmed. Without one it is [FALLBACK] with [extension] (from the MIME
     * type, without the dot), so the desk still knows what kind of file it is.
     */
    fun clean(display: String?, extension: String?): String {
        val base = baseName(display.orEmpty())
        if (base.isNotEmpty()) return base
        val ext = extension?.trim()?.trimStart('.').orEmpty()
        return if (ext.isEmpty()) FALLBACK else "$FALLBACK.$ext"
    }

    /** The name of a `.txt` file holding text shared with [subject] (EXTRA_SUBJECT). */
    fun forText(subject: String?): String {
        val base = baseName(subject.orEmpty().take(MAX_SUBJECT))
        if (base.isEmpty()) return TEXT_FALLBACK
        return if (base.endsWith(".txt", ignoreCase = true)) base else "$base.txt"
    }

    private fun baseName(name: String) = name.substringAfterLast('/').substringAfterLast('\\').trim()
}

/** How much of [total] bytes [sent] is, in whole percent; null when the size is unknown. */
fun percentOf(sent: Long, total: Long): Int? = if (total > 0) (sent.coerceAtMost(total) * 100 / total).toInt() else null

/** Reads [input] until it ends or [limit] bytes have been read, whichever comes first. */
fun readAtMost(input: InputStream, limit: Int): ByteArray {
    val out = java.io.ByteArrayOutputStream()
    val buf = ByteArray(64 * 1024)
    while (out.size() < limit) {
        val n = input.read(buf, 0, minOf(buf.size, limit - out.size()))
        if (n < 0) break
        out.write(buf, 0, n)
    }
    return out.toByteArray()
}
