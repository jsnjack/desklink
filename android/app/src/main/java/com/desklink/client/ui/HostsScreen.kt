package com.desklink.client.ui

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
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.LocalContentColor
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.res.colorResource
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.res.stringResource
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
 * What the main page shows: the saved [hosts], the one whose details show
 * ([selectedId], null without hosts), the app-wide [settings], the unpaired
 * hosts found [nearby], whether a pairing runs, whether the network is
 * [metered], the choices made before a session and the app's [version].
 */
data class HostsUiState(
    val hosts: List<HostItem>,
    val selectedId: String?,
    val settings: SettingsUiState,
    val nearby: List<NearbyHost> = emptyList(),
    val pairing: Boolean = false,
    val metered: Boolean = false,
    val preset: StreamPreset = StreamPreset.AUTO,
    val screenMode: String = ScreenMode.AUTO,
    val playAudio: Boolean = true,
    val version: String = "",
)

/** What the main page's controls do, the settings' included. */
interface HostsActions : SettingsActions {
    /** Shows [host]'s details. */
    fun select(host: HostProfile)

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

    /** Shares the app's log. */
    fun shareLogs()

    /** Asks whether to forget [host]. */
    fun forget(host: HostProfile)

    /** Chooses the stream preset. */
    fun setPreset(preset: StreamPreset)

    /** Chooses what sessions show, a [ScreenMode] value. */
    fun setScreenMode(mode: String)

    /** Chooses whether the tablet plays the desktop's sound. */
    fun setPlayAudio(play: Boolean)
}

/** Below this width the host list and the chosen host's details stack in one column. */
private val TWO_PANE_WIDTH = 840.dp

/** The widest a single column of the main page grows. */
private val COLUMN_MAX_WIDTH = 720.dp

/**
 * The main page. On a wide window the saved hosts sit in a list on the left
 * and the chosen host's details beside them, every setting included; on a
 * narrow one the details follow the list in one column, so Connect stays one
 * tap away with the usual one or two hosts. Without hosts it shows how to
 * pair the first.
 */
@Composable
fun HostsScreen(state: HostsUiState, actions: HostsActions) {
    Surface(Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.surface) {
        Column(Modifier.fillMaxSize().safeDrawingPadding()) {
            TopBar(state.version)
            if (state.metered) MeteredBanner(Modifier.padding(start = 16.dp, end = 16.dp, bottom = 8.dp))
            BoxWithConstraints(Modifier.fillMaxSize()) {
                val selected = state.hosts.firstOrNull { it.profile.id == state.selectedId } ?: state.hosts.firstOrNull()
                when {
                    selected == null -> EmptyState(state, actions)
                    maxWidth >= TWO_PANE_WIDTH -> TwoPanes(state, selected, actions, listWidth = (maxWidth * 0.25f).coerceIn(280.dp, 360.dp))
                    else -> OneColumn(state, selected, actions)
                }
            }
        }
    }
}

/** The app's name, and its version small at the other end. */
@Composable
private fun TopBar(version: String) {
    Row(
        Modifier.fillMaxWidth().height(56.dp).padding(horizontal = 16.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(stringResource(R.string.app_name), style = MaterialTheme.typography.titleLarge, modifier = Modifier.weight(1f))
        if (version.isNotEmpty()) {
            Text(
                stringResource(R.string.about_version, version),
                style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }
    }
}

/** What a stream costs while the network is metered, from the host's measurements in docs/desktop.md. */
@Composable
private fun MeteredBanner(modifier: Modifier = Modifier) {
    Text(
        stringResource(R.string.metered_banner),
        style = MaterialTheme.typography.bodySmall,
        color = MaterialTheme.colorScheme.onTertiaryContainer,
        modifier = modifier.fillMaxWidth()
            .background(MaterialTheme.colorScheme.tertiaryContainer, RoundedCornerShape(8.dp))
            .padding(horizontal = 12.dp, vertical = 6.dp),
    )
}

@Composable
private fun TwoPanes(state: HostsUiState, selected: HostItem, actions: HostsActions, listWidth: Dp) {
    Row(Modifier.fillMaxSize().padding(horizontal = 16.dp)) {
        Column(Modifier.width(listWidth).fillMaxHeight().verticalScroll(rememberScrollState()).padding(bottom = 16.dp)) {
            HostList(state, selected.profile.id, actions)
            AddButtons(state.pairing, actions, Modifier.padding(top = 8.dp))
        }
        Spacer(Modifier.width(16.dp))
        Column(Modifier.weight(1f).fillMaxHeight().verticalScroll(rememberScrollState()).padding(bottom = 16.dp)) {
            HostDetail(selected, state, actions)
        }
    }
}

@Composable
private fun OneColumn(state: HostsUiState, selected: HostItem, actions: HostsActions) {
    Box(Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(horizontal = 16.dp), contentAlignment = Alignment.TopCenter) {
        Column(Modifier.widthIn(max = COLUMN_MAX_WIDTH).fillMaxWidth().padding(bottom = 16.dp)) {
            HostList(state, selected.profile.id, actions)
            AddButtons(state.pairing, actions, Modifier.padding(vertical = 8.dp))
            HostDetail(selected, state, actions)
        }
    }
}

/** The saved hosts, then the unpaired ones found on the network. */
@Composable
private fun HostList(state: HostsUiState, selectedId: String, actions: HostsActions) {
    Column(verticalArrangement = Arrangement.spacedBy(4.dp)) {
        for (item in state.hosts) {
            HostRow(item, selected = item.profile.id == selectedId) { actions.select(item.profile) }
        }
        NearbyList(state, actions)
    }
}

@Composable
private fun NearbyList(state: HostsUiState, actions: HostsActions) {
    if (state.nearby.isEmpty()) return
    Text(
        stringResource(R.string.nearby_title),
        style = MaterialTheme.typography.titleSmall,
        color = MaterialTheme.colorScheme.primary,
        modifier = Modifier.padding(start = 16.dp, top = 12.dp, bottom = 4.dp),
    )
    for (n in state.nearby) {
        Row(Modifier.fillMaxWidth().padding(start = 16.dp), verticalAlignment = Alignment.CenterVertically) {
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

@Composable
private fun HostRow(item: HostItem, selected: Boolean, onClick: () -> Unit) {
    Surface(
        selected = selected,
        onClick = onClick,
        shape = RoundedCornerShape(12.dp),
        color = if (selected) MaterialTheme.colorScheme.secondaryContainer else MaterialTheme.colorScheme.surfaceContainerLow,
        modifier = Modifier.fillMaxWidth(),
    ) {
        Column(Modifier.padding(horizontal = 16.dp, vertical = 10.dp)) {
            Text(item.profile.name, style = MaterialTheme.typography.titleSmall, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Row(verticalAlignment = Alignment.CenterVertically) {
                StatusDot(item.health)
                Spacer(Modifier.width(6.dp))
                Text(
                    shortStatus(item),
                    style = MaterialTheme.typography.bodySmall,
                    // The selected row's container sets its own content colour.
                    color = if (selected) LocalContentColor.current else MaterialTheme.colorScheme.onSurfaceVariant,
                    maxLines = 1, overflow = TextOverflow.Ellipsis,
                )
            }
        }
    }
}

/** Scan QR and Add host, below the list. */
@Composable
private fun AddButtons(pairing: Boolean, actions: HostsActions, modifier: Modifier = Modifier) {
    Row(modifier, horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
        OutlinedButton(onClick = actions::scanQr, enabled = !pairing, contentPadding = ButtonDefaults.ButtonWithIconContentPadding) {
            Icon(painterResource(R.drawable.ic_qr_scan), contentDescription = null, modifier = Modifier.size(18.dp))
            Spacer(Modifier.width(8.dp))
            Text(stringResource(R.string.action_scan_qr))
        }
        TextButton(onClick = actions::addHost, contentPadding = ButtonDefaults.TextButtonWithIconContentPadding) {
            Icon(painterResource(R.drawable.ic_add), contentDescription = null, modifier = Modifier.size(18.dp))
            Spacer(Modifier.width(8.dp))
            Text(stringResource(R.string.action_add_host))
        }
        if (pairing) CircularProgressIndicator(Modifier.size(20.dp), strokeWidth = 2.dp)
    }
}

/**
 * The chosen host: its status, Connect (with Wake while it does not answer,
 * or Pair before it is paired) and its less common actions as quiet text
 * buttons; then, under a line saying they apply to every computer, the
 * choices made before a session and every other setting.
 *
 * The host's actions sit with its status rather than at the end: they are
 * about this host, as the settings are not, and they stay in view when the
 * settings make the details scroll.
 */
@Composable
private fun HostDetail(item: HostItem, state: HostsUiState, actions: HostsActions) {
    Surface(Modifier.fillMaxWidth(), shape = RoundedCornerShape(16.dp), color = MaterialTheme.colorScheme.surfaceContainer) {
        Column(Modifier.padding(top = 16.dp, bottom = 12.dp)) {
            DetailHeader(item, state.pairing, actions, Modifier.padding(horizontal = 20.dp))
            HostActions(item.profile, state.pairing, actions)
            Column(Modifier.padding(horizontal = 20.dp)) {
                EveryComputer()
                SessionChoices(state, actions)
                SettingsPanel(state.settings, actions)
            }
        }
    }
}

/** Check, Test connection, Pair again, Share logs and Forget, in the error colour. */
@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun HostActions(p: HostProfile, pairing: Boolean, actions: HostsActions) {
    // The buttons' own padding lines their labels up with the text above.
    FlowRow(Modifier.fillMaxWidth().padding(horizontal = 8.dp)) {
        TextButton(onClick = { actions.check(p) }) { Text(stringResource(R.string.action_check)) }
        TextButton(onClick = { actions.test(p) }) { Text(stringResource(R.string.action_test_connection)) }
        if (p.paired) {
            TextButton(onClick = { actions.pair(p) }, enabled = !pairing) { Text(stringResource(R.string.action_repair)) }
        }
        TextButton(onClick = actions::shareLogs) { Text(stringResource(R.string.action_share_log)) }
        TextButton(
            onClick = { actions.forget(p) },
            colors = ButtonDefaults.textButtonColors(contentColor = MaterialTheme.colorScheme.error),
        ) { Text(stringResource(R.string.action_forget)) }
    }
}

/**
 * The line between the host and the settings, saying that the settings
 * below it are the same whichever host is chosen.
 */
@Composable
private fun EveryComputer() {
    Row(Modifier.fillMaxWidth().padding(top = 4.dp, bottom = 4.dp), verticalAlignment = Alignment.CenterVertically) {
        Text(
            stringResource(R.string.settings_every_computer),
            style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant,
        )
        Spacer(Modifier.width(12.dp))
        HorizontalDivider(Modifier.weight(1f))
    }
}

@Composable
private fun DetailHeader(item: HostItem, pairing: Boolean, actions: HostsActions, modifier: Modifier = Modifier) {
    val p = item.profile
    Row(modifier, verticalAlignment = Alignment.CenterVertically) {
        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
            Text(p.name, style = MaterialTheme.typography.titleLarge, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Row(verticalAlignment = Alignment.CenterVertically) {
                StatusDot(item.health)
                Spacer(Modifier.width(8.dp))
                Text(fullStatus(item), style = MaterialTheme.typography.bodyMedium)
            }
            (item.health as? HostHealth.Online)?.update?.let { update ->
                val advice = when (update) {
                    ControlProtocol.Update.HOST -> R.string.update_host
                    ControlProtocol.Update.APP -> R.string.update_app
                }
                Text(stringResource(advice, item.health.version), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.tertiary)
            }
            Text(
                if (p.paired) stringResource(R.string.host_paired, shownAddress(p), p.fingerprint.orEmpty().take(16))
                else stringResource(R.string.host_not_paired, shownAddress(p)),
                style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }
        Spacer(Modifier.width(16.dp))
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
            val big = Modifier.heightIn(min = 48.dp)
            val padding = PaddingValues(horizontal = 28.dp)
            when {
                !p.paired -> {
                    if (pairing) CircularProgressIndicator(Modifier.size(24.dp))
                    Button(onClick = { actions.pair(p) }, enabled = !pairing, modifier = big, contentPadding = padding) {
                        Text(stringResource(R.string.action_pair))
                    }
                }
                else -> {
                    if (offersWake(p, item.health)) {
                        OutlinedButton(onClick = { actions.wake(p) }, modifier = big) { Text(stringResource(R.string.action_wake)) }
                    }
                    Button(onClick = { actions.connect(p) }, modifier = big, contentPadding = padding) {
                        Text(stringResource(R.string.action_connect))
                    }
                }
            }
        }
    }
}

/** Quality, Screen and Sound: what the next session asks for. */
@Composable
private fun SessionChoices(state: HostsUiState, actions: HostsActions) {
    SettingRow(stringResource(R.string.label_quality), null, inset = 0.dp) {
        ChoiceSegments(StreamPreset.entries.map { it to it.label }, state.preset, actions::setPreset)
    }
    Explanation(stringResource(state.preset.explain))
    val screens = listOf(ScreenMode.AUTO to R.string.screen_auto, ScreenMode.OWN to R.string.screen_own, ScreenMode.MIRROR to R.string.screen_mirror)
    SettingRow(stringResource(R.string.label_screen), null, inset = 0.dp) {
        ChoiceSegments(screens, state.screenMode, actions::setScreenMode)
    }
    Explanation(stringResource(screenExplain(state.screenMode)))
    SwitchRow(stringResource(R.string.label_play_audio), null, state.playAudio, actions::setPlayAudio, inset = 0.dp)
}

@Composable
private fun Explanation(text: String) {
    Text(
        text, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
        modifier = Modifier.padding(bottom = 6.dp),
    )
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
        is HostHealth.Offline -> stringResource(R.string.status_offline_short)
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
