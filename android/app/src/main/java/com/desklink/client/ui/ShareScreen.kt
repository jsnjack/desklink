package com.desklink.client.ui

import android.text.format.Formatter
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.selection.selectableGroup
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.RadioButton
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.desklink.client.R
import com.desklink.client.model.HostProfile
import com.desklink.client.model.percentOf

/** The share target's files and the state of their upload. */
internal data class ShareUiState(
    val hosts: List<HostProfile>,
    val items: List<ShareItem>,
    val loading: Boolean = false,
    val target: HostProfile? = null,
    val reaching: Boolean = false,
    val error: String? = null,
)

/** Shows the files and destination before handing the transfer to the view model. */
@Composable
fun ShareScreen(vm: ShareViewModel, onClose: () -> Unit) {
    val items by vm.items.collectAsStateWithLifecycle()
    val loading by vm.loading.collectAsStateWithLifecycle()
    val target by vm.target.collectAsStateWithLifecycle()
    val reaching by vm.reaching.collectAsStateWithLifecycle()
    val error by vm.error.collectAsStateWithLifecycle()
    ShareSheet(ShareUiState(vm.hosts, items, loading, target, reaching, error), vm::send, vm::cancel, onClose)
}

/** Lets the user pick a paired computer, then shows each file's progress and result. */
@Composable
internal fun ShareSheet(
    state: ShareUiState,
    onSend: (HostProfile) -> Unit,
    onCancel: () -> Unit,
    onClose: () -> Unit,
) {
    var selectedId by rememberSaveable { mutableStateOf(state.hosts.firstOrNull()?.id) }
    val selected = state.hosts.firstOrNull { it.id == selectedId }
    val hasPending = state.items.any { it.status !is ShareStatus.Saved }
    val canChoose = !state.loading && hasPending && state.target == null
    BoxWithConstraints(
        Modifier.fillMaxSize().background(DeskTheme.colors.line).safeDrawingPadding().padding(16.dp),
        contentAlignment = Alignment.BottomCenter,
    ) {
        Surface(
            modifier = Modifier.widthIn(max = SETTINGS_MAX_WIDTH).fillMaxWidth().heightIn(max = maxHeight),
            shape = DeskShape, color = DeskTheme.colors.card, contentColor = DeskTheme.colors.text,
        ) {
            Column(Modifier.padding(CARD_PADDING), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                    Text(
                        stringResource(R.string.share_title), Modifier.weight(1f),
                        style = MaterialTheme.typography.titleLarge,
                    )
                    if (state.target == null) {
                        IconButton(onClick = onClose) {
                            Icon(painterResource(R.drawable.ic_hud_close), stringResource(R.string.action_close))
                        }
                    }
                }
                LazyColumn(Modifier.weight(1f, fill = false), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                    items(state.items, key = { it.id }) { ShareRow(it) }
                    if (state.error != null) item { ErrorBanner(state.error, null) }
                    when {
                        state.loading -> item { Text(stringResource(R.string.share_loading)) }
                        state.items.isEmpty() -> item { Text(stringResource(R.string.share_nothing)) }
                        state.hosts.isEmpty() -> item { Text(stringResource(R.string.share_no_hosts)) }
                        canChoose -> item { ShareDestinations(state.hosts, selectedId) { selectedId = it } }
                    }
                }
                when {
                    state.target != null -> {
                        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                            CircularProgressIndicator(Modifier.size(20.dp), strokeWidth = 2.dp)
                            Text(
                                stringResource(if (state.reaching) R.string.share_reaching else R.string.share_sending, state.target.name),
                                Modifier.weight(1f),
                            )
                            QuietButton(onClick = onCancel) { Text(stringResource(R.string.action_cancel)) }
                        }
                    }
                    selected != null && canChoose -> PrimaryButton(onClick = { onSend(selected) }, modifier = Modifier.fillMaxWidth()) {
                        Text(stringResource(R.string.share_send_to, selected.name))
                    }
                }
            }
        }
    }
}

@Composable
private fun ShareDestinations(hosts: List<HostProfile>, selectedId: String?, onSelect: (String) -> Unit) {
    HorizontalDivider(color = DeskTheme.colors.line)
    Gap(8.dp)
    Text(stringResource(R.string.share_destination), style = MaterialTheme.typography.labelMedium, color = DeskTheme.colors.muted)
    Column(Modifier.selectableGroup()) {
        hosts.forEach { host ->
            val selected = host.id == selectedId
            Row(
                Modifier.fillMaxWidth().heightIn(min = 48.dp).selectable(selected, role = Role.RadioButton, onClick = { onSelect(host.id) }),
                horizontalArrangement = Arrangement.spacedBy(12.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                RadioButton(selected = selected, onClick = null)
                Text(host.name, Modifier.weight(1f), style = MaterialTheme.typography.bodyMedium)
                if (selected) {
                    Text(stringResource(R.string.share_downloads), style = MaterialTheme.typography.bodySmall, color = DeskTheme.colors.muted)
                }
            }
        }
    }
}

@Composable
private fun ShareRow(item: ShareItem) {
    val context = LocalContext.current
    val size = { bytes: Long -> Formatter.formatShortFileSize(context, bytes) }
    val status = item.status
    val fraction = (status as? ShareStatus.Sending)?.let { percentOf(it.sent, item.size) }
    Column(verticalArrangement = Arrangement.spacedBy(4.dp)) {
        Text(item.name, style = MaterialTheme.typography.titleSmall)
        val line = when (status) {
            ShareStatus.Waiting -> if (item.size >= 0) size(item.size) else stringResource(R.string.share_status_waiting)
            is ShareStatus.Sending -> fraction?.let { stringResource(R.string.share_status_sending_of, size(status.sent), size(item.size), it) }
                ?: stringResource(R.string.share_status_sending, size(status.sent))
            is ShareStatus.Saved -> stringResource(R.string.share_status_saved, status.name, status.hostName)
            is ShareStatus.Failed -> stringResource(R.string.share_status_failed, status.message)
            ShareStatus.Cancelled -> stringResource(R.string.share_status_cancelled)
        }
        Text(
            line,
            style = MaterialTheme.typography.bodySmall,
            color = if (status is ShareStatus.Failed) MaterialTheme.colorScheme.error else DeskTheme.colors.muted,
        )
        if (status is ShareStatus.Sending) {
            if (fraction != null) {
                LinearProgressIndicator(progress = { fraction / 100f }, modifier = Modifier.fillMaxWidth())
            } else {
                LinearProgressIndicator(modifier = Modifier.fillMaxWidth())
            }
        }
    }
}
