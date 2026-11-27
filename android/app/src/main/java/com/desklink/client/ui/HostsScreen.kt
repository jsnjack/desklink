package com.desklink.client.ui

import androidx.annotation.StringRes
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.toggleable
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LocalContentColor
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.MenuDefaults
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Surface
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.res.colorResource
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import com.desklink.client.R
import com.desklink.client.model.ControlProtocol
import com.desklink.client.model.HostProfile
import com.desklink.client.model.NearbyHost
import com.desklink.client.model.ScreenMode
import com.desklink.client.model.StreamPreset

/** A saved host and its last health check. */
data class HostItem(val profile: HostProfile, val health: HostHealth?)

/**
 * What the main page shows: the saved [hosts], the unpaired hosts found
 * [nearby], whether a pairing runs, whether the network is [metered], and
 * the choices the next session makes, the same for every host.
 */
data class HostsUiState(
    val hosts: List<HostItem>,
    val nearby: List<NearbyHost> = emptyList(),
    val pairing: Boolean = false,
    val metered: Boolean = false,
    val preset: StreamPreset = StreamPreset.AUTO,
    val screenMode: String = ScreenMode.AUTO,
    val playAudio: Boolean = true,
)

/** What the main page's controls do. */
interface HostsActions {
    /** Connects to [host], waking it first when it does not answer. */
    fun connect(host: HostProfile)

    /** Sends [host] one round of Wake-on-LAN packets. */
    fun wake(host: HostProfile)

    /** Scans a pairing QR code for [host]. */
    fun pair(host: HostProfile)

    /** Scans a pairing QR code for a host found on the network. */
    fun pairNearby(host: NearbyHost)

    /** Scans a pairing QR code for whichever host showed it. */
    fun scanQr()

    /** Asks for a host's address. */
    fun addHost()

    /** Checks whether [host] answers. */
    fun check(host: HostProfile)

    /** Tests every address of [host]. */
    fun test(host: HostProfile)

    /** Asks whether to forget [host]. */
    fun forget(host: HostProfile)

    /** Opens the settings page. */
    fun openSettings()

    /** Chooses the stream preset. */
    fun setPreset(preset: StreamPreset)

    /** Chooses what sessions show, a [ScreenMode] value. */
    fun setScreenMode(mode: String)

    /** Chooses whether the tablet plays the desktop's sound. */
    fun setPlayAudio(play: Boolean)
}

/** The widest the page's content grows. */
private val PAGE_MAX_WIDTH = 1120.dp

/** How narrow a host's card may get before the grid has one column fewer. */
private val CARD_MIN_WIDTH = 320.dp

/** The space between cards. */
private val GAP = 12.dp

/**
 * The main page, for picking a computer and connecting: the choices the
 * next session makes in one row, then a card per saved host with its status
 * and Connect, then the unpaired hosts found on the network. A host's less
 * common actions sit in its card's menu, and the app's settings on their own
 * page behind the top bar's gear. Without hosts it shows how to pair the
 * first.
 */
@Composable
fun HostsScreen(state: HostsUiState, actions: HostsActions) {
    var details by remember { mutableStateOf<HostItem?>(null) }
    Surface(Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.surface) {
        Column(Modifier.fillMaxSize().safeDrawingPadding()) {
            TopBar(state, actions)
            if (state.hosts.isEmpty()) {
                EmptyState(state, actions)
            } else {
                Box(Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(horizontal = 16.dp), contentAlignment = Alignment.TopCenter) {
                    Column(Modifier.widthIn(max = PAGE_MAX_WIDTH).fillMaxWidth().padding(bottom = 24.dp), verticalArrangement = Arrangement.spacedBy(GAP)) {
                        if (state.metered) MeteredNotice()
                        NextSession(state, actions)
                        HostGrid(state, actions) { details = it }
                        NearbyList(state, actions)
                    }
                }
            }
        }
    }
    details?.let { item ->
        // The dialog follows the host's newest health while it is open.
        val current = state.hosts.firstOrNull { it.profile.id == item.profile.id } ?: item
        HostDetails(current) { details = null }
    }
}

/** The app's name; Scan QR, Add host and Settings at the other end. */
@Composable
private fun TopBar(state: HostsUiState, actions: HostsActions) {
    Row(
        Modifier.fillMaxWidth().height(64.dp).padding(start = 16.dp, end = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(stringResource(R.string.app_name), style = MaterialTheme.typography.titleLarge, modifier = Modifier.weight(1f))
        if (state.pairing) CircularProgressIndicator(Modifier.size(20.dp).padding(end = 4.dp), strokeWidth = 2.dp)
        // Without hosts the page's own buttons pair the first one.
        if (state.hosts.isNotEmpty()) {
            IconButton(onClick = actions::scanQr, enabled = !state.pairing) {
                Icon(painterResource(R.drawable.ic_qr_scan), contentDescription = stringResource(R.string.action_scan_qr))
            }
            IconButton(onClick = actions::addHost) {
                Icon(painterResource(R.drawable.ic_add), contentDescription = stringResource(R.string.action_add_host))
            }
        }
        IconButton(onClick = actions::openSettings) {
            Icon(painterResource(R.drawable.ic_settings), contentDescription = stringResource(R.string.action_settings))
        }
    }
}

/** One line on what a stream costs while the network is metered; a tap shows the measurements. */
@Composable
private fun MeteredNotice() {
    var open by remember { mutableStateOf(false) }
    Surface(
        onClick = { open = true },
        shape = RoundedCornerShape(8.dp),
        color = MaterialTheme.colorScheme.tertiaryContainer,
        modifier = Modifier.fillMaxWidth(),
    ) {
        Text(
            stringResource(R.string.metered_notice), style = MaterialTheme.typography.bodyMedium,
            color = MaterialTheme.colorScheme.onTertiaryContainer,
            modifier = Modifier.padding(horizontal = 12.dp, vertical = 8.dp),
        )
    }
    if (open) {
        AlertDialog(
            onDismissRequest = { open = false },
            title = { Text(stringResource(R.string.metered_title)) },
            text = { Text(stringResource(R.string.metered_banner)) },
            confirmButton = { TextButton(onClick = { open = false }) { Text(stringResource(R.string.action_close)) } },
        )
    }
}

/**
 * Quality, Screen and Sound: what the next session asks for, whichever host
 * it connects to. Each choice is a menu whose entries explain themselves.
 */
@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun NextSession(state: HostsUiState, actions: HostsActions) {
    FlowRow(
        Modifier.fillMaxWidth(),
        horizontalArrangement = Arrangement.spacedBy(16.dp),
        verticalArrangement = Arrangement.spacedBy(4.dp),
    ) {
        ChoiceMenu(
            stringResource(R.string.label_quality),
            StreamPreset.entries.map { MenuChoice(it, stringResource(it.label), detail = stringResource(it.explain)) },
            state.preset, actions::setPreset,
        )
        ChoiceMenu(
            stringResource(R.string.label_screen),
            listOf(ScreenMode.AUTO to R.string.screen_auto, ScreenMode.OWN to R.string.screen_own, ScreenMode.MIRROR to R.string.screen_mirror)
                .map { (mode, label) -> MenuChoice(mode, stringResource(label), detail = stringResource(screenExplain(mode))) },
            state.screenMode, actions::setScreenMode,
        )
        Row(
            Modifier.align(Alignment.CenterVertically)
                .toggleable(value = state.playAudio, role = Role.Switch, onValueChange = actions::setPlayAudio),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text(stringResource(R.string.label_sound), style = MaterialTheme.typography.labelLarge)
            Spacer(Modifier.width(8.dp))
            Switch(checked = state.playAudio, onCheckedChange = null)
        }
    }
}

/**
 * "Label: choice ▾", opening a menu of [choices], each with its explanation;
 * the chosen one is filled, as in [MenuRow].
 */
@Composable
private fun <T> ChoiceMenu(label: String, choices: List<MenuChoice<T>>, selected: T, onSelect: (T) -> Unit) {
    var open by remember { mutableStateOf(false) }
    Box {
        OutlinedButton(onClick = { open = true }, contentPadding = PaddingValues(start = 16.dp, end = 8.dp)) {
            Text(
                stringResource(R.string.choice_value, label, choices.firstOrNull { it.value == selected }?.shown.orEmpty()),
                style = MaterialTheme.typography.labelLarge,
            )
            Icon(painterResource(R.drawable.ic_arrow_drop_down), contentDescription = null)
        }
        DropdownMenu(expanded = open, onDismissRequest = { open = false }, containerColor = MaterialTheme.colorScheme.surfaceContainerHighest) {
            for (c in choices) {
                val on = c.value == selected
                val text = if (on) MaterialTheme.colorScheme.onSecondaryContainer else MaterialTheme.colorScheme.onSurface
                DropdownMenuItem(
                    text = {
                        Column(Modifier.widthIn(max = 360.dp).padding(vertical = 4.dp)) {
                            Text(c.label, style = MaterialTheme.typography.bodyLarge)
                            c.detail?.let {
                                Text(it, style = MaterialTheme.typography.bodySmall, color = if (on) text else MaterialTheme.colorScheme.onSurfaceVariant)
                            }
                        }
                    },
                    onClick = {
                        open = false
                        if (!on) onSelect(c.value)
                    },
                    colors = MenuDefaults.itemColors(textColor = text),
                    modifier = if (on) Modifier.background(MaterialTheme.colorScheme.secondaryContainer) else Modifier,
                )
            }
        }
    }
}

/**
 * The saved hosts as cards, as many to a row as fit at [CARD_MIN_WIDTH] but
 * no more than there are hosts, so two hosts share the width; a single host
 * still takes half of it rather than a card as wide as the page.
 */
@Composable
private fun HostGrid(state: HostsUiState, actions: HostsActions, onDetails: (HostItem) -> Unit) {
    BoxWithConstraints(Modifier.fillMaxWidth()) {
        val fit = ((maxWidth + GAP) / (CARD_MIN_WIDTH + GAP)).toInt().coerceAtLeast(1)
        val columns = minOf(fit, maxOf(state.hosts.size, 2))
        Column(verticalArrangement = Arrangement.spacedBy(GAP)) {
            for (row in state.hosts.chunked(columns)) {
                Row(horizontalArrangement = Arrangement.spacedBy(GAP)) {
                    for (item in row) HostCard(item, state.pairing, actions, { onDetails(item) }, Modifier.weight(1f))
                    // A short last row keeps its cards as wide as the others.
                    repeat(columns - row.size) { Spacer(Modifier.weight(1f)) }
                }
            }
        }
    }
}

/**
 * A saved host: its name and status, then Connect (or Pair before it is
 * paired). Connect wakes a host that does not answer. Checking, testing,
 * waking, pairing again, the details and Forget are in the card's menu.
 */
@Composable
private fun HostCard(item: HostItem, pairing: Boolean, actions: HostsActions, onDetails: () -> Unit, modifier: Modifier = Modifier) {
    val p = item.profile
    Surface(modifier, shape = RoundedCornerShape(16.dp), color = MaterialTheme.colorScheme.surfaceContainer) {
        Column(Modifier.padding(start = 20.dp, end = 8.dp, top = 12.dp, bottom = 16.dp)) {
            Row(verticalAlignment = Alignment.Top) {
                Column(Modifier.weight(1f).padding(top = 4.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
                    Text(p.name, style = MaterialTheme.typography.titleLarge, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        StatusDot(item.health)
                        Spacer(Modifier.width(8.dp))
                        Text(
                            shortStatus(item), style = MaterialTheme.typography.bodyMedium,
                            color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 1, overflow = TextOverflow.Ellipsis,
                        )
                    }
                    updateAdvice(item)?.let {
                        Text(it, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.tertiary)
                    }
                }
                HostMenu(item, pairing, actions, onDetails)
            }
            Spacer(Modifier.height(16.dp))
            val big = Modifier.fillMaxWidth().padding(end = 12.dp).heightIn(min = 48.dp)
            if (p.paired) {
                Button(onClick = { actions.connect(p) }, modifier = big) { Text(stringResource(R.string.action_connect)) }
            } else {
                Button(onClick = { actions.pair(p) }, enabled = !pairing, modifier = big) { Text(stringResource(R.string.action_pair)) }
            }
        }
    }
}

/** A host's less common actions; Forget last, apart, in the error colour, and it asks first. */
@Composable
private fun HostMenu(item: HostItem, pairing: Boolean, actions: HostsActions, onDetails: () -> Unit) {
    val p = item.profile
    var open by remember { mutableStateOf(false) }
    val pick = { action: () -> Unit ->
        open = false
        action()
    }
    Box {
        IconButton(onClick = { open = true }) {
            Icon(painterResource(R.drawable.ic_more_vert), contentDescription = stringResource(R.string.action_more))
        }
        DropdownMenu(expanded = open, onDismissRequest = { open = false }) {
            MenuEntry(R.string.action_check) { pick { actions.check(p) } }
            MenuEntry(R.string.action_test_connection) { pick { actions.test(p) } }
            if (offersWake(p, item.health)) MenuEntry(R.string.action_wake) { pick { actions.wake(p) } }
            if (p.paired) MenuEntry(R.string.action_repair, enabled = !pairing) { pick { actions.pair(p) } }
            MenuEntry(R.string.action_details) { pick(onDetails) }
            HorizontalDivider()
            DropdownMenuItem(
                text = { Text(stringResource(R.string.action_forget), color = MaterialTheme.colorScheme.error) },
                onClick = { pick { actions.forget(p) } },
            )
        }
    }
}

@Composable
private fun MenuEntry(@StringRes label: Int, enabled: Boolean = true, onClick: () -> Unit) {
    DropdownMenuItem(text = { Text(stringResource(label)) }, enabled = enabled, onClick = onClick)
}

/** What the card leaves out: the full status, the version advice, the address and the pairing. */
@Composable
private fun HostDetails(item: HostItem, onDismiss: () -> Unit) {
    val p = item.profile
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(p.name) },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Text(fullStatus(item), style = MaterialTheme.typography.bodyMedium)
                updateAdvice(item)?.let { Text(it, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.tertiary) }
                Text(
                    if (p.paired) stringResource(R.string.host_paired, shownAddress(p), p.fingerprint.orEmpty().take(16))
                    else stringResource(R.string.host_not_paired, shownAddress(p)),
                    style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
        },
        confirmButton = { TextButton(onClick = onDismiss) { Text(stringResource(R.string.action_close)) } },
    )
}

/** Advice when the host's desklinkd and this app differ in version, or null. */
@Composable
private fun updateAdvice(item: HostItem): String? {
    val h = item.health as? HostHealth.Online ?: return null
    val advice = when (h.update ?: return null) {
        ControlProtocol.Update.HOST -> R.string.update_host
        ControlProtocol.Update.APP -> R.string.update_app
    }
    return stringResource(advice, h.version)
}

/** The unpaired hosts found on the network, each with Pair. */
@Composable
private fun NearbyList(state: HostsUiState, actions: HostsActions) {
    if (state.nearby.isEmpty()) return
    Column {
        Text(
            stringResource(R.string.nearby_title),
            style = MaterialTheme.typography.titleSmall,
            color = MaterialTheme.colorScheme.primary,
            modifier = Modifier.padding(start = 4.dp, top = 8.dp, bottom = 4.dp),
        )
        for (n in state.nearby) {
            Row(Modifier.fillMaxWidth().padding(start = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                Column(Modifier.weight(1f)) {
                    Text(n.name, style = MaterialTheme.typography.titleSmall, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    Text(
                        stringResource(R.string.nearby_subtitle, n.ip),
                        style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                }
                // The announcement proves nothing; pairing takes the code from desklinkd pair.
                TextButton(onClick = { actions.pairNearby(n) }, enabled = !state.pairing) { Text(stringResource(R.string.action_pair)) }
            }
        }
    }
}

private fun screenExplain(mode: String): Int = when (mode) {
    ScreenMode.OWN -> R.string.screen_own_explain
    ScreenMode.MIRROR -> R.string.screen_mirror_explain
    else -> R.string.screen_auto_explain
}

/** No saved host yet: how to pair the first, with Scan QR as the main action. */
@Composable
private fun EmptyState(state: HostsUiState, actions: HostsActions) {
    Box(Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(16.dp), contentAlignment = Alignment.Center) {
        Column(
            Modifier.widthIn(max = 480.dp).padding(top = 48.dp),
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            Icon(
                painterResource(R.drawable.ic_computer), contentDescription = null,
                tint = MaterialTheme.colorScheme.primary, modifier = Modifier.size(48.dp),
            )
            Text(stringResource(R.string.empty_title), style = MaterialTheme.typography.titleLarge)
            Text(
                stringResource(R.string.empty_explain), style = MaterialTheme.typography.bodyMedium,
                color = MaterialTheme.colorScheme.onSurfaceVariant, textAlign = TextAlign.Center,
            )
            Spacer(Modifier.height(8.dp))
            Button(onClick = actions::scanQr, enabled = !state.pairing, contentPadding = ButtonDefaults.ButtonWithIconContentPadding) {
                Icon(painterResource(R.drawable.ic_qr_scan), contentDescription = null, modifier = Modifier.size(18.dp))
                Spacer(Modifier.width(8.dp))
                Text(stringResource(R.string.action_scan_qr))
            }
            TextButton(onClick = actions::addHost) { Text(stringResource(R.string.action_add_by_address)) }
            if (state.pairing) CircularProgressIndicator(Modifier.size(20.dp), strokeWidth = 2.dp)
            Column(Modifier.fillMaxWidth()) { NearbyList(state, actions) }
        }
    }
}

@Composable
private fun StatusDot(health: HostHealth?) {
    Box(Modifier.size(8.dp).background(statusColor(health), CircleShape))
}

@Composable
private fun statusColor(health: HostHealth?): Color = when (health) {
    is HostHealth.Online -> colorResource(R.color.health_online)
    is HostHealth.Offline -> MaterialTheme.colorScheme.error
    HostHealth.Waking -> MaterialTheme.colorScheme.tertiary
    HostHealth.Checking, null -> MaterialTheme.colorScheme.outline
}

/** The list's status: "Online · 4 ms", "Offline", and whether the host is paired. */
@Composable
private fun shortStatus(item: HostItem): String {
    val status = when (val h = item.health) {
        is HostHealth.Online -> stringResource(R.string.status_online_short, h.ms)
        is HostHealth.Offline ->
            stringResource(if (offersWake(item.profile, h)) R.string.status_offline_wakes else R.string.status_offline_short)
        HostHealth.Waking -> stringResource(R.string.health_waking)
        HostHealth.Checking, null -> stringResource(R.string.health_checking)
    }
    return if (item.profile.paired) status else stringResource(R.string.status_not_paired, status)
}

/** The details' status: the version and answer time, or why the host did not answer. */
@Composable
private fun fullStatus(item: HostItem): String = when (val h = item.health) {
    is HostHealth.Online -> reportedName(item.profile.name, h.hostname)
        ?.let { stringResource(R.string.health_online_named_detail, it, h.version, h.ms) }
        ?: stringResource(R.string.health_online_detail, h.version, h.ms)
    is HostHealth.Offline -> stringResource(R.string.health_offline, h.reason)
    HostHealth.Waking -> stringResource(R.string.health_waking)
    HostHealth.Checking, null -> stringResource(R.string.health_checking)
}
