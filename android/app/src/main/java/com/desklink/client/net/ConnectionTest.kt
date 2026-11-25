package com.desklink.client.net

import java.net.ConnectException
import java.net.SocketTimeoutException

/** What one address answered when the connection test asked it for /health. */
sealed interface ProbeOutcome {
    /** desklinkd answered after [ms] milliseconds, reporting its [hostname] and [version]. */
    data class Answered(val ms: Long, val hostname: String, val version: String) : ProbeOutcome

    /** The machine is on, but nothing listens on the port, so desklinkd is not running. */
    data object Refused : ProbeOutcome

    /** No answer in time: the machine is asleep, or not reachable from this network. */
    data object TimedOut : ProbeOutcome

    /** Any other failure, such as a certificate that does not match or an HTTP error. */
    data class Failed(val message: String) : ProbeOutcome

    companion object {
        /**
         * The outcome a failed /health call stands for. OkHttp and the TLS
         * layer wrap the exception that says what happened ("No route to
         * host", the pinned certificate's mismatch), so the causes are
         * searched, and the innermost message is the one shown.
         */
        fun of(e: Throwable): ProbeOutcome {
            val chain = causes(e)
            return when {
                isConnectionRefused(e) -> Refused
                chain.any { it is SocketTimeoutException || (it is ConnectException && "ETIMEDOUT" in it.message.orEmpty()) } -> TimedOut
                else -> Failed(chain.lastOrNull { !it.message.isNullOrBlank() }?.message ?: e.toString())
            }
        }

        private fun causes(e: Throwable): List<Throwable> = generateSequence(e) { t -> t.cause?.takeIf { it !== t } }.take(MAX_CAUSES).toList()

        private const val MAX_CAUSES = 16
    }
}

/**
 * One line of the connection test: the [address] and its [outcome].
 * [fastest] marks the address that answered first, which Connect would use.
 */
data class ProbeRow(val address: String, val outcome: ProbeOutcome, val fastest: Boolean)

/**
 * The connection test's rows for [results], an outcome per address in the
 * order Connect tries them, which they keep: the main address first. Only an
 * answering address can be the fastest, and of two equally fast ones the
 * earlier is marked.
 *
 * The test covers HTTPS only. A UDP test needs the host to answer a probe on
 * its media port; its result will go beside these rows.
 */
fun probeRows(results: List<Pair<String, ProbeOutcome>>): List<ProbeRow> {
    val fastest = results.withIndex()
        .filter { it.value.second is ProbeOutcome.Answered }
        .minByOrNull { (it.value.second as ProbeOutcome.Answered).ms }?.index
    return results.mapIndexed { i, (address, outcome) -> ProbeRow(address, outcome, i == fastest) }
}
