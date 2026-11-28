package com.desklink.client.ui

import androidx.annotation.StringRes
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
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
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Surface
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
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import com.desklink.client.R
import com.desklink.client.model.ControlProtocol
import com.desklink.client.model.HostProfile
import com.desklink.client.model.HostSettings
import com.desklink.client.model.NearbyHost
import com.desklink.client.model.ScreenMode
import com.desklink.client.model.StreamPreset

/** A saved host, its last health check and its settings. */
data class HostItem(val profile: HostProfile, val health: HostHealth?, val settings: HostSettings = HostSettings())

/**
 * What the main page shows: the saved [hosts], the unpaired hosts found
 * [nearby], whether a pairing runs and whether the network is [metered].
 */
data class HostsUiState(
    val hosts: List<HostItem>,
    val nearby: List<NearbyHost> = emptyList(),
    val pairing: Boolean = false,
    val metered: Boolean = false,
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

    /** Opens Pair Your Computer, with the steps, Scan code and the computers found nearby. */
    fun addComputer()

    /** Checks whether [host] answers. */
    fun check(host: HostProfile)

    /** Tests every address of [host]. */
    fun test(host: HostProfile)

    /** Asks whether to forget [host]. */
    fun forget(host: HostProfile)

    /** Opens the tablet's settings page. */
    fun openSettings()

    /** Opens [host]'s settings page. */
    fun openHostSettings(host: HostProfile)

    /** Chooses [host]'s stream preset. */
    fun setPreset(host: HostProfile, preset: StreamPreset)

    /** Chooses what [host]'s sessions show, a [ScreenMode] value. */
    fun setScreenMode(host: HostProfile, mode: String)

    /** Chooses whether the tablet plays [host]'s sound. */
    fun setPlayAudio(host: HostProfile, play: Boolean)
}

/** The widest the page's content grows. */
private val PAGE_MAX_WIDTH = 1120.dp

/** How narrow a host's card may get before the grid has one column fewer. */
private val CARD_MIN_WIDTH = 320.dp

/** The space between cards. */
private val GAP = 12.dp

/**
 * The main page, for picking a computer and connecting: a card per saved
 * host with its status, what its next session asks for and Connect, then
 * the unpaired hosts found on the network. A host's less common actions and
 * its settings page sit in its card's menu, and the tablet's own settings
 * on their page behind the top bar's gear. Without hosts it shows how to
 * pair the first.
 */
@Composable
fun HostsScreen(state: HostsUiState, actions: HostsActions) {
    var details by remember { mutableStateOf<HostItem?>(null) }
    var choices by remember { mutableStateOf<HostItem?>(null) }
    Surface(Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.surface) {
        Column(Modifier.fillMaxSize().safeDrawingPadding()) {
            TopBar(state, actions)
            run {
                Box(Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(horizontal = 16.dp), contentAlignment = Alignment.TopCenter) {
                    Column(Modifier.widthIn(max = PAGE_MAX_WIDTH).fillMaxWidth().padding(bottom = 24.dp), verticalArrangement = Arrangement.spacedBy(GAP)) {
                        if (state.metered) MeteredNotice()
                        HostGrid(state, actions, onDetails = { details = it }, onChoices = { choices = it })
                        NearbyList(state, actions)
                    }
                }
            }
        }
    }
    // The dialogs follow the host's newest health and settings while they are open.
    details?.let { item ->
        val current = state.hosts.firstOrNull { it.profile.id == item.profile.id } ?: item
        HostDetails(current) { details = null }
    }
    choices?.let { item ->
        val current = state.hosts.firstOrNull { it.profile.id == item.profile.id } ?: item
        NextSessionDialog(current, actions) { choices = null }
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
            IconButton(onClick = actions::addComputer) {
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
 * What [item]'s next session asks for in one line, "Auto · Own screen ·
 * Sound on"; a tap opens the choices ([NextSessionDialog]).
 */
@Composable
private fun NextSessionSummary(item: HostItem, onClick: () -> Unit) {
    val s = item.settings
    val summary = stringResource(
        R.string.host_summary,
        stringResource(s.preset.label),
        stringResource(
            when (s.screen) {
                ScreenMode.OWN -> R.string.screen_own
                ScreenMode.MIRROR -> R.string.screen_mirror
                else -> R.string.summary_screen_auto
            },
        ),
        stringResource(if (s.sound) R.string.summary_sound_on else R.string.summary_sound_off),
    )
    OutlinedButton(onClick = onClick, contentPadding = PaddingValues(start = 16.dp, end = 8.dp)) {
        Text(summary, style = MaterialTheme.typography.labelLarge, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
        Icon(painterResource(R.drawable.ic_arrow_drop_down), contentDescription = null)
    }
}

/**
 * Quality, Screen and Sound for [item]'s next session, each saved for that
 * host as it is chosen; the menus' entries explain themselves. The host's
 * other settings are on its settings page.
 */
@Composable
private fun NextSessionDialog(item: HostItem, actions: HostsActions, onDismiss: () -> Unit) {
    val p = item.profile
    val s = item.settings
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(stringResource(R.string.next_session_title, p.name)) },
        text = {
            Column {
                MenuRow(
                    stringResource(R.string.label_quality), null, presetChoices(), s.preset, { actions.setPreset(p, it) },
                    inset = 0.dp,
                )
                MenuRow(
                    stringResource(R.string.label_screen), null, screenChoices(), s.screen, { actions.setScreenMode(p, it) },
                    inset = 0.dp,
                )
                SwitchRow(stringResource(R.string.label_sound), null, s.sound, { actions.setPlayAudio(p, it) }, inset = 0.dp)
            }
        },
        confirmButton = { TextButton(onClick = onDismiss) { Text(stringResource(R.string.action_close)) } },
        dismissButton = {
            TextButton(onClick = {
                onDismiss()
                actions.openHostSettings(p)
            }) { Text(stringResource(R.string.action_more_settings)) }
        },
    )
}

/**
 * The saved hosts as cards, as many to a row as fit at [CARD_MIN_WIDTH] but
 * no more than there are hosts, so two hosts share the width; a single host
 * still takes half of it rather than a card as wide as the page.
 */
@Composable
private fun HostGrid(state: HostsUiState, actions: HostsActions, onDetails: (HostItem) -> Unit, onChoices: (HostItem) -> Unit) {
    BoxWithConstraints(Modifier.fillMaxWidth()) {
        val fit = ((maxWidth + GAP) / (CARD_MIN_WIDTH + GAP)).toInt().coerceAtLeast(1)
        val columns = minOf(fit, maxOf(state.hosts.size, 2))
        Column(verticalArrangement = Arrangement.spacedBy(GAP)) {
            for (row in state.hosts.chunked(columns)) {
                Row(horizontalArrangement = Arrangement.spacedBy(GAP)) {
                    for (item in row) HostCard(item, state.pairing, actions, { onDetails(item) }, { onChoices(item) }, Modifier.weight(1f))
                    // A short last row keeps its cards as wide as the others.
                    repeat(columns - row.size) { Spacer(Modifier.weight(1f)) }
                }
            }
        }
    }
}

/**
 * A saved host: its name and status, what its next session asks for, then
 * Connect (or Pair before it is paired). Connect wakes a host that does not
 * answer. Checking, testing, waking, pairing again, its settings, the
 * details and Forget are in the card's menu.
 */
@Composable
private fun HostCard(
    item: HostItem,
    pairing: Boolean,
    actions: HostsActions,
    onDetails: () -> Unit,
    onChoices: () -> Unit,
    modifier: Modifier = Modifier,
) {
    val p = item.profile
    Surface(modifier, shape = DeskShape, color = DeskTheme.colors.card) {
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
            Spacer(Modifier.height(12.dp))
            Box(Modifier.padding(end = 12.dp)) { NextSessionSummary(item, onChoices) }
            Spacer(Modifier.height(12.dp))
            val big = Modifier.fillMaxWidth().padding(end = 12.dp).heightIn(min = 48.dp)
            if (p.paired) {
                PrimaryButton(onClick = { actions.connect(p) }, modifier = big) { Text(stringResource(R.string.action_connect)) }
            } else {
                PrimaryButton(onClick = { actions.pair(p) }, enabled = !pairing, modifier = big) { Text(stringResource(R.string.action_pair)) }
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
            MenuEntry(R.string.action_settings) { pick { actions.openHostSettings(p) } }
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

@Composable
private fun StatusDot(health: HostHealth?) {
    Box(Modifier.size(8.dp).background(statusColor(health), CircleShape))
}

/** GNOME's status colours: online green, waking yellow, unreachable red, grey while unknown. */
@Composable
private fun statusColor(health: HostHealth?): Color = when (health) {
    is HostHealth.Online -> DeskTheme.colors.online
    is HostHealth.Offline -> DeskTheme.colors.unreachable
    HostHealth.Waking -> DeskTheme.colors.waking
    HostHealth.Checking, null -> DeskTheme.colors.asleep
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
