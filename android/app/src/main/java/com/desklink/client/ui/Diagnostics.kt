package com.desklink.client.ui

import android.content.ActivityNotFoundException
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Process
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.res.colorResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import com.desklink.client.R
import com.desklink.client.net.ProbeOutcome
import com.desklink.client.net.ProbeRow
import com.desklink.client.net.UdpResult
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.async
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeoutOrNull
import java.io.IOException
import java.time.ZonedDateTime
import java.time.format.DateTimeFormatter
import java.time.temporal.ChronoUnit

private const val LOG_TAG = "DeskLink"

/** The newest lines of this process's log that logcat reads, before the report keeps only the DeskLink ones that fit. */
private const val LOG_LINES = 2000
private const val LOGCAT_TIMEOUT_MS = 5_000L

/**
 * Collects the app's recent DeskLink log lines and opens Android's share
 * sheet with them as plain text. [onError] gets a message when the log
 * cannot be read or nothing can take the text.
 */
suspend fun shareLog(context: Context, onError: (String) -> Unit) {
    val log = try {
        readOwnLog()
    } catch (e: IOException) {
        return onError(context.getString(R.string.msg_log_failed, e.message ?: e.toString()))
    }
    val text = withContext(Dispatchers.Default) { LogReport.build(logHeader(context), log) }
    val send = Intent(Intent.ACTION_SEND)
        .setType("text/plain")
        .putExtra(Intent.EXTRA_SUBJECT, context.getString(R.string.log_subject))
        .putExtra(Intent.EXTRA_TEXT, text)
    try {
        context.startActivity(Intent.createChooser(send, context.getString(R.string.action_share_log)))
    } catch (e: ActivityNotFoundException) {
        onError(context.getString(R.string.msg_log_failed, e.message ?: e.toString()))
    }
}

/**
 * This process's recent lines under the DeskLink tag. An app may read its
 * own log without any permission. Only the DeskLink tag is read, because the
 * app's own lines are known to hold no token, key or clipboard text, while
 * the framework's lines in the same process are not.
 */
private suspend fun readOwnLog(): String = withContext(Dispatchers.IO) {
    val command = listOf("logcat", "-d", "-t", LOG_LINES.toString(), "--pid=${Process.myPid()}", "-v", "threadtime", "-s", "$LOG_TAG:V")
    val logcat = ProcessBuilder(command).redirectErrorStream(true).start()
    try {
        // Read while logcat writes: waiting first would block it on a full pipe.
        val output = async { runCatching { logcat.inputStream.bufferedReader().use { it.readText() } } }
        val result = withTimeoutOrNull(LOGCAT_TIMEOUT_MS) { output.await() }
            ?: throw IOException("logcat did not finish within ${LOGCAT_TIMEOUT_MS / 1000} s")
        result.getOrElse { throw IOException("reading logcat failed: ${it.message}", it) }
    } finally {
        // Ends the read above too, when logcat hangs.
        logcat.destroy()
    }
}

/** The report's first lines: app version, Android version and device model, and when it was taken. */
private fun logHeader(context: Context): List<String> {
    val version = runCatching {
        val pm = context.packageManager
        val info = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            pm.getPackageInfo(context.packageName, PackageManager.PackageInfoFlags.of(0))
        } else {
            @Suppress("DEPRECATION")
            pm.getPackageInfo(context.packageName, 0)
        }
        "${info.versionName} (${info.longVersionCode})"
    }.getOrDefault("unknown")
    return listOf(
        "DeskLink $version",
        "Android ${Build.VERSION.RELEASE} (API ${Build.VERSION.SDK_INT}), ${Build.MANUFACTURER} ${Build.MODEL}",
        "Taken ${ZonedDateTime.now().truncatedTo(ChronoUnit.SECONDS).format(DateTimeFormatter.ISO_OFFSET_DATE_TIME)}",
    )
}

/** The connection test's result for one host, or a spinner while its addresses are asked. */
@Composable
fun ConnectionTestDialog(test: ConnectionTest, onDismiss: () -> Unit) {
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(stringResource(R.string.test_title, test.hostName)) },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
                val rows = test.rows
                when {
                    rows == null -> Row(verticalAlignment = Alignment.CenterVertically) {
                        CircularProgressIndicator(Modifier.size(24.dp))
                        Spacer(Modifier.width(12.dp))
                        Text(stringResource(R.string.test_running))
                    }
                    rows.isEmpty() -> Text(stringResource(R.string.test_no_address))
                    else -> rows.forEach { ProbeRowLine(it) }
                }
                Text(stringResource(R.string.test_https_only, test.port), style = MaterialTheme.typography.bodySmall)
            }
        },
        confirmButton = { TextButton(onClick = onDismiss) { Text(stringResource(R.string.action_close)) } },
    )
}

@Composable
private fun ProbeRowLine(row: ProbeRow) {
    val error = MaterialTheme.colorScheme.error
    val (color, text) = when (val o = row.outcome) {
        is ProbeOutcome.Answered -> {
            val https = stringResource(R.string.test_answered, o.ms, o.hostname, o.version)
            val udp = when (val u = o.udp) {
                is UdpResult.Answered -> stringResource(R.string.test_udp_ok, u.ms)
                UdpResult.NoAnswer -> stringResource(R.string.test_udp_blocked)
                UdpResult.Unsupported -> stringResource(R.string.test_udp_unsupported)
                null -> null
            }
            // An address whose UDP is blocked cannot stream, though HTTPS answers.
            (if (o.udp == UdpResult.NoAnswer) error else colorResource(R.color.health_online)) to listOfNotNull(https, udp).joinToString("\n")
        }
        ProbeOutcome.Refused -> error to stringResource(R.string.test_refused)
        ProbeOutcome.TimedOut -> error to stringResource(R.string.test_timed_out)
        is ProbeOutcome.Failed -> error to stringResource(R.string.test_failed, o.message)
    }
    Row {
        Box(Modifier.padding(top = 6.dp).size(10.dp).clip(CircleShape).background(color))
        Spacer(Modifier.width(12.dp))
        Column {
            Text(
                if (row.fastest) stringResource(R.string.test_address_fastest, row.address) else row.address,
                style = MaterialTheme.typography.titleSmall,
            )
            Text(text, style = MaterialTheme.typography.bodySmall)
        }
    }
}
