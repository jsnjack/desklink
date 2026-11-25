package com.desklink.client.ui

/** Turns the app's logcat output into the text "Share logs" hands to the share sheet. */
object LogReport {
    /**
     * The most the report holds, in UTF-8 bytes. The share sheet passes the
     * text through a Binder transaction, which fails beyond about 1 MB for the
     * whole intent, so a tenth of that leaves room.
     */
    const val MAX_BYTES = 100 * 1024

    // The longest note about left-out lines, so the note never pushes the report over its limit.
    private const val NOTE_RESERVE = 64

    // A bearer token, and the Keystore-wrapped peer key that a JSON parse error
    // quotes from the saved hosts ("saved hosts are unreadable").
    private val secrets = listOf(
        Regex("""(Bearer\s+)\S+""", RegexOption.IGNORE_CASE),
        Regex("""("wrappedKey"\s*:\s*")[^"]*"""),
    )

    /**
     * The [header] lines, a blank line, then the lines of [log], newest last,
     * in at most [maxBytes]. When the log does not fit, the oldest lines are
     * left out and a note says how many. Logcat's "beginning of" buffer
     * markers are dropped. The app logs no token, pairing key or clipboard
     * text; a bearer token or wrapped key that a line might still quote is
     * masked all the same, since the text leaves the device.
     */
    fun build(header: List<String>, log: String, maxBytes: Int = MAX_BYTES): String {
        val head = header.joinToString("\n", postfix = "\n\n")
        val lines = log.lines()
            .filter { it.isNotBlank() && !it.startsWith("--------- beginning of") }
            .map { line -> secrets.fold(line) { text, secret -> secret.replace(text, "\$1[removed]") } }
        val budget = maxBytes - head.utf8Size() - NOTE_RESERVE
        var used = 0
        var kept = 0
        for (line in lines.asReversed()) {
            val size = line.utf8Size() + 1
            if (used + size > budget) break
            used += size
            kept++
        }
        val dropped = lines.size - kept
        return buildString {
            append(head)
            when {
                lines.isEmpty() -> append("(no DeskLink log lines)\n")
                dropped > 0 -> append("(").append(dropped).append(" earlier lines left out)\n")
            }
            lines.takeLast(kept).forEach { append(it).append('\n') }
        }
    }

    private fun String.utf8Size(): Int = toByteArray(Charsets.UTF_8).size
}
