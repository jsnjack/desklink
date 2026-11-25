package com.desklink.client.ui

import android.text.format.Formatter
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.desklink.client.R
import com.desklink.client.model.percentOf

/**
 * The share screen: what is being sent, a "Send to" button per paired host
 * while nothing is on its way, and each file's progress and result.
 */
@Composable
fun ShareScreen(vm: ShareViewModel, onClose: () -> Unit) {
    val items by vm.items.collectAsStateWithLifecycle()
    val target by vm.target.collectAsStateWithLifecycle()
    val reaching by vm.reaching.collectAsStateWithLifecycle()
    val error by vm.error.collectAsStateWithLifecycle()
    val busy = target != null
    val unsaved = items.any { it.status !is ShareStatus.Saved }

    Surface(modifier = Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.background) {
        Column(
            modifier = Modifier.fillMaxSize().safeDrawingPadding().padding(24.dp),
            verticalArrangement = Arrangement.spacedBy(16.dp),
        ) {
            Text(stringResource(R.string.share_title), style = MaterialTheme.typography.headlineSmall)
            LazyColumn(modifier = Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                items(items, key = { it.id }) { ShareRow(it) }
            }
            error?.let { Text(it, color = MaterialTheme.colorScheme.error) }
            when {
                vm.hosts.isEmpty() -> Text(stringResource(R.string.share_no_hosts))
                items.isEmpty() -> Text(stringResource(R.string.share_nothing))
                busy -> Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                    CircularProgressIndicator(modifier = Modifier.size(20.dp), strokeWidth = 2.dp)
                    val name = target?.name.orEmpty()
                    Text(
                        stringResource(if (reaching) R.string.share_reaching else R.string.share_sending, name),
                        modifier = Modifier.weight(1f),
                    )
                    OutlinedButton(onClick = vm::cancel) { Text(stringResource(R.string.action_cancel)) }
                }
                unsaved -> vm.hosts.forEach { host ->
                    Button(onClick = { vm.send(host) }, modifier = Modifier.fillMaxWidth()) {
                        Text(stringResource(R.string.share_send_to, host.name))
                    }
                }
            }
            if (!busy) {
                TextButton(onClick = onClose, modifier = Modifier.align(Alignment.End)) { Text(stringResource(R.string.action_close)) }
            }
        }
    }
}

@Composable
private fun ShareRow(item: ShareItem) {
    val context = LocalContext.current
    val size = { bytes: Long -> Formatter.formatShortFileSize(context, bytes) }
    Column(verticalArrangement = Arrangement.spacedBy(4.dp)) {
        Text(item.name, style = MaterialTheme.typography.titleSmall)
        val line = when (val st = item.status) {
            ShareStatus.Waiting -> if (item.size >= 0) size(item.size) else stringResource(R.string.share_status_waiting)
            is ShareStatus.Sending -> percentOf(st.sent, item.size)
                ?.let { stringResource(R.string.share_status_sending_of, size(st.sent), size(item.size), it) }
                ?: stringResource(R.string.share_status_sending, size(st.sent))
            is ShareStatus.Saved -> stringResource(R.string.share_status_saved, st.name)
            is ShareStatus.Failed -> stringResource(R.string.share_status_failed, st.message)
            ShareStatus.Cancelled -> stringResource(R.string.share_status_cancelled)
        }
        Text(
            line,
            style = MaterialTheme.typography.bodySmall,
            color = if (item.status is ShareStatus.Failed) MaterialTheme.colorScheme.error else MaterialTheme.colorScheme.onSurfaceVariant,
        )
        (item.status as? ShareStatus.Sending)?.let { st ->
            val fraction = percentOf(st.sent, item.size)
            if (fraction != null) {
                LinearProgressIndicator(progress = { fraction / 100f }, modifier = Modifier.fillMaxWidth())
            } else {
                LinearProgressIndicator(modifier = Modifier.fillMaxWidth())
            }
        }
    }
}
