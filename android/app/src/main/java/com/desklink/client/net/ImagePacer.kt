package com.desklink.client.net

/**
 * Sends the parts of a clipboard image one at a time, each only once
 * OkHttp's outgoing queue is empty, so at most one part is queued ahead of a
 * message sent meanwhile. Text to type goes over the same WebSocket and the
 * keys typed after it wait for it at most [StreamSession]'s write cap: queued
 * behind a whole image, the text would arrive seconds after those keys.
 */
class ImagePacer(
    /** The bytes OkHttp has queued on the connection. */
    private val queued: () -> Long,
    /** Whether the connection the image goes over is still the session's. */
    private val open: () -> Boolean,
    private val sleep: (Long) -> Unit = Thread::sleep,
    private val now: () -> Long = { System.nanoTime() / 1_000_000 },
) {
    /**
     * Sends [parts] in order with [send], waiting before each until the queue
     * is empty. Returns false, leaving the rest unsent, once the connection
     * is no longer [open] or a part has waited [PART_WAIT_MS]: the host drops
     * an image whose parts stop coming when the next one starts.
     */
    fun <T> send(parts: List<T>, send: (T) -> Unit): Boolean {
        for (part in parts) {
            val deadline = now() + PART_WAIT_MS
            while (queued() > 0) {
                if (!open() || now() >= deadline) return false
                sleep(POLL_MS)
            }
            if (!open()) return false
            send(part)
        }
        return true
    }

    companion object {
        /** How often the queue is checked while a part waits. */
        const val POLL_MS = 5L

        /** The longest one part waits for the queue before the image is given up. */
        const val PART_WAIT_MS = 30_000L
    }
}
