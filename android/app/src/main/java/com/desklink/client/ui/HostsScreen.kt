package com.desklink.client.ui

import androidx.annotation.StringRes
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
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
import androidx.compose.foundation.layout.wrapContentWidth
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
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
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
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
 * [nearby], whether a pairing runs, whether the network is [metered], and
 * the host whose details show beside the list ([selected], else the first).
 */
data class HostsUiState(
    val hosts: List<HostItem>,
    val nearby: List<NearbyHost> = emptyList(),
    val pairing: Boolean = false,
    val metered: Boolean = false,
    val selected: String? = null,
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

    /** Shows [host]'s details beside the list. */
    fun select(host: HostProfile)

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

    /** Chooses whether [host]'s sessions draw still parts as exact pixels. */
    fun setExactText(host: HostProfile, on: Boolean)

    /** Chooses whether [host]'s Second Desk turns the desk's monitors off. */
    fun setDeskOff(host: HostProfile, off: Boolean)
}

/** The list's width beside the details. */
private val LIST_WIDTH = 320.dp

/** The widest the details grow. */
private val DETAIL_MAX_WIDTH = 720.dp

/** A list row's height (docs/design.md, Spacing). */
private val LIST_ROW = 64.dp

/**
 * The main page, for picking a computer and connecting. In landscape the
 * list of computers sits on the left under the big title, which says how
 * many are online, and the selected one on the right: its status, its
 * settings for the next session and one Connect at the bottom, with its
 * less common actions behind ⋯. In portrait the list alone fills the page,
 * each computer with its one action, and a computer opens on its own page.
 */
@Composable
fun HostsScreen(state: HostsUiState, actions: HostsActions) {
    var details by remember { mutableStateOf<HostItem?>(null) }
    Surface(Modifier.fillMaxSize(), color = DeskTheme.colors.page, contentColor = DeskTheme.colors.text) {
        BoxWithConstraints(Modifier.fillMaxSize().safeDrawingPadding()) {
            if (maxWidth > maxHeight && maxWidth >= 720.dp) {
                TwoPanes(state, actions) { details = it }
            } else {
                ListPage(state, actions, maxHeight)
            }
        }
    }
    // The dialog follows the host's newest health while it is open.
    details?.let { item ->
        val current = state.hosts.firstOrNull { it.profile.id == item.profile.id } ?: item
        HostDetails(current) { details = null }
    }
}

/** "1 of 2 computers online": the big title's state line. */
@Composable
private fun onlineLine(state: HostsUiState): String {
    val online = state.hosts.count { it.health is HostHealth.Online }
    return pluralStringResource(R.plurals.computers_online, state.hosts.size, online, state.hosts.size)
}

@Composable
private fun TwoPanes(state: HostsUiState, actions: HostsActions, onDetails: (HostItem) -> Unit) {
    val chosen = state.hosts.firstOrNull { it.profile.id == state.selected } ?: state.hosts.first()
    Row(Modifier.fillMaxSize().padding(start = 32.dp, end = 32.dp)) {
        Column(Modifier.width(LIST_WIDTH).fillMaxHeight()) {
            Spacer(Modifier.height(TITLE_AIR))
            PageTitle(stringResource(R.string.app_name), onlineLine(state), Modifier.padding(start = 16.dp))
            Gap(24.dp)
            Column(Modifier.weight(1f).verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(4.dp)) {
                for (item in state.hosts) {
                    HostRow(item, selected = item.profile.id == chosen.profile.id) { actions.select(item.profile) }
                }
                if (state.nearby.isNotEmpty()) {
                    GroupName(stringResource(R.string.nearby_title))
                    for (n in state.nearby) {
                        ListRow(DeskTheme.colors.asleep, n.name, stringResource(R.string.status_not_paired_short), selected = false) {
                            if (!state.pairing) actions.pairNearby(n)
                        }
                    }
                }
            }
            BottomLinks(state, actions)
        }
        Spacer(Modifier.width(32.dp))
        Details(chosen, state, actions, onDetails, Modifier.weight(1f).fillMaxHeight())
    }
}

/** Add computer, with a spinner while a pairing runs, and the tablet's settings. */
@Composable
private fun BottomLinks(state: HostsUiState, actions: HostsActions) {
    Row(Modifier.fillMaxWidth().padding(vertical = 12.dp), verticalAlignment = Alignment.CenterVertically) {
        TextButton(onClick = actions::addComputer, contentPadding = PaddingValues(horizontal = 16.dp)) {
            Icon(painterResource(R.drawable.ic_add), contentDescription = null, tint = DeskTheme.colors.link, modifier = Modifier.size(18.dp))
            Spacer(Modifier.width(8.dp))
            Text(stringResource(R.string.action_add_computer), style = MaterialTheme.typography.labelLarge, color = DeskTheme.colors.link)
        }
        if (state.pairing) CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.dp)
        Spacer(Modifier.weight(1f))
        IconButton(onClick = actions::openSettings) {
            Icon(painterResource(R.drawable.ic_settings), contentDescription = stringResource(R.string.action_settings), tint = DeskTheme.colors.muted)
        }
    }
}

/** A saved host in the list: its dot, name and short status; the selected one on Blue Tint. */
@Composable
private fun HostRow(item: HostItem, selected: Boolean, onClick: () -> Unit) {
    ListRow(statusColor(item), item.profile.name, shortStatus(item), selected, onClick)
}

@Composable
private fun ListRow(dot: Color, name: String, status: String, selected: Boolean, onClick: () -> Unit) {
    Row(
        Modifier.fillMaxWidth().clip(DeskShape).background(if (selected) DeskTheme.colors.blueTint else Color.Transparent)
            .clickable(onClick = onClick).heightIn(min = LIST_ROW).padding(horizontal = 16.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Dot(dot)
        Spacer(Modifier.width(14.dp))
        Column(Modifier.weight(1f)) {
            Text(name, style = MaterialTheme.typography.titleSmall, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Text(status, style = MaterialTheme.typography.bodySmall, color = DeskTheme.colors.muted, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
    }
}

/**
 * The selected host: its name and status, a banner when it did not answer,
 * its next session's settings in two groups, and its one main action at
 * the bottom with ⋯ beside it.
 */
@Composable
private fun Details(item: HostItem, state: HostsUiState, actions: HostsActions, onDetails: (HostItem) -> Unit, modifier: Modifier) {
    val p = item.profile
    val s = item.settings
    Column(modifier.wrapContentWidth(Alignment.Start).widthIn(max = DETAIL_MAX_WIDTH)) {
        Column(Modifier.weight(1f).verticalScroll(rememberScrollState())) {
            // The name's first line sits level with the big title's.
            Spacer(Modifier.height(TITLE_AIR + 6.dp))
            Text(p.name, style = MaterialTheme.typography.titleLarge, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Gap(4.dp)
            Row(verticalAlignment = Alignment.CenterVertically) {
                Dot(statusColor(item), 8.dp)
                Spacer(Modifier.width(8.dp))
                Text(shortStatus(item), style = MaterialTheme.typography.bodySmall, color = DeskTheme.colors.muted)
            }
            updateAdvice(item)?.let {
                Gap(4.dp)
                Text(it, style = MaterialTheme.typography.bodySmall, color = DeskTheme.colors.warningText)
            }
            Gap(20.dp)
            if (state.metered) {
                MeteredNotice()
                Gap(12.dp)
            }
            unreachable(item)?.let { h ->
                OfflineBanner(p.name, h)
                Gap(12.dp)
            }
            CardGroup(
                listOf(
                    {
                        SettingRow(stringResource(R.string.label_screen), null, minHeight = LIST_ROW) {
                            ChoiceSegments(screenSegments, s.screen, { actions.setScreenMode(p, it) })
                        }
                    },
                    {
                        MenuRow(
                            stringResource(R.string.label_quality), stringResource(presetShort(s.preset)), presetChoices(), s.preset,
                            { actions.setPreset(p, it) }, minHeight = LIST_ROW,
                        )
                    },
                ),
            )
            Gap(12.dp)
            CardGroup(
                listOf(
                    { SwitchRow(stringResource(R.string.label_sound), null, s.sound, { actions.setPlayAudio(p, it) }, minHeight = LIST_ROW) },
                    {
                        SwitchRow(
                            stringResource(R.string.label_exact_text),
                            if (s.sharperColours) null else stringResource(R.string.exact_text_needs_sharper),
                            s.exactText && s.sharperColours, { actions.setExactText(p, it) }, enabled = s.sharperColours, minHeight = LIST_ROW,
                        )
                    },
                    {
                        SwitchRow(
                            stringResource(R.string.label_desk_off),
                            stringResource(if (s.screen == ScreenMode.MIRROR) R.string.desk_off_needs_second else R.string.desk_off_short),
                            s.deskOff, { actions.setDeskOff(p, it) }, enabled = s.screen != ScreenMode.MIRROR, minHeight = LIST_ROW,
                        )
                    },
                ),
            )
            TextButton(onClick = { actions.openHostSettings(p) }, contentPadding = PaddingValues(horizontal = 16.dp)) {
                Text(stringResource(R.string.action_all_settings), style = MaterialTheme.typography.labelLarge, color = DeskTheme.colors.link)
            }
            Gap(16.dp)
        }
        Row(Modifier.fillMaxWidth().padding(top = 8.dp, bottom = 24.dp), verticalAlignment = Alignment.CenterVertically) {
            MainAction(item, state.pairing, actions, Modifier.weight(1f))
            Spacer(Modifier.width(12.dp))
            HostMenu(item, state.pairing, actions) { onDetails(item) }
        }
    }
}

/** Auto, Second Desk and Mirror as segments. */
private val screenSegments = listOf(
    ScreenMode.AUTO to R.string.screen_auto,
    ScreenMode.OWN to R.string.screen_own,
    ScreenMode.MIRROR to R.string.screen_mirror,
)

/**
 * The page's one main action: Pair before the host is paired, Try again
 * after it did not answer and cannot be woken, else Connect, which wakes a
 * host that does not answer.
 */
@Composable
private fun MainAction(item: HostItem, pairing: Boolean, actions: HostsActions, modifier: Modifier) {
    val p = item.profile
    when {
        !p.paired -> PrimaryButton(onClick = { actions.pair(p) }, enabled = !pairing, modifier = modifier) {
            Text(stringResource(R.string.action_pair))
        }
        unreachable(item) != null -> PrimaryButton(onClick = { actions.connect(p) }, modifier = modifier) {
            Text(stringResource(R.string.action_try_again))
        }
        else -> PrimaryButton(onClick = { actions.connect(p) }, modifier = modifier) { Text(stringResource(R.string.action_connect)) }
    }
}

/**
 * Portrait: the big title in the top third, then each host on a card with
 * its one action (Connect, Wake while it sleeps, Pair before it is paired);
 * a tap on a card opens the host's own page.
 */
@Composable
private fun ListPage(state: HostsUiState, actions: HostsActions, height: Dp) {
    Column(Modifier.fillMaxSize().padding(horizontal = 24.dp)) {
        Column(Modifier.weight(1f).verticalScroll(rememberScrollState())) {
            Spacer(Modifier.height(maxOf(TITLE_AIR, height * 0.2f)))
            PageTitle(stringResource(R.string.app_name), onlineLine(state))
            Gap(24.dp)
            if (state.metered) {
                MeteredNotice()
                Gap(12.dp)
            }
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                for (item in state.hosts) HostCard(item, state.pairing, actions)
            }
            if (state.nearby.isNotEmpty()) {
                GroupName(stringResource(R.string.nearby_title))
                Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    for (n in state.nearby) {
                        CardRow(DeskTheme.colors.asleep, n.name, stringResource(R.string.status_not_paired_short), onClick = { actions.pairNearby(n) }) {
                            QuietButton(onClick = { actions.pairNearby(n) }, enabled = !state.pairing) { Text(stringResource(R.string.action_pair)) }
                        }
                    }
                }
            }
        }
        BottomLinks(state, actions)
    }
}

@Composable
private fun HostCard(item: HostItem, pairing: Boolean, actions: HostsActions) {
    val p = item.profile
    CardRow(statusColor(item), p.name, shortStatus(item), onClick = { actions.openHostSettings(p) }) {
        when {
            !p.paired -> QuietButton(onClick = { actions.pair(p) }, enabled = !pairing) { Text(stringResource(R.string.action_pair)) }
            asleep(item) ->
                QuietButton(onClick = { actions.wake(p) }) { Text(stringResource(R.string.action_wake)) }
            else -> PrimaryButton(onClick = { actions.connect(p) }) { Text(stringResource(R.string.action_connect)) }
        }
    }
}

@Composable
private fun CardRow(dot: Color, name: String, status: String, onClick: () -> Unit, action: @Composable () -> Unit) {
    Row(
        Modifier.fillMaxWidth().clip(DeskShape).background(DeskTheme.colors.card).clickable(onClick = onClick)
            .heightIn(min = LIST_ROW + 8.dp).padding(horizontal = 16.dp, vertical = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Dot(dot)
        Spacer(Modifier.width(14.dp))
        Column(Modifier.weight(1f)) {
            Text(name, style = MaterialTheme.typography.titleSmall, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Text(status, style = MaterialTheme.typography.bodySmall, color = DeskTheme.colors.muted, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
        Spacer(Modifier.width(12.dp))
        action()
    }
}

/** One line on what a stream costs while the network is metered; a tap shows the measurements. */
@Composable
private fun MeteredNotice() {
    var open by remember { mutableStateOf(false) }
    NoteBanner(stringResource(R.string.metered_notice), Modifier.clickable { open = true })
    if (open) {
        AlertDialog(
            onDismissRequest = { open = false },
            title = { Text(stringResource(R.string.metered_title)) },
            text = { Text(stringResource(R.string.metered_banner)) },
            confirmButton = { QuietButton(onClick = { open = false }) { Text(stringResource(R.string.action_close)) } },
        )
    }
}

/** A host's less common actions behind ⋯; Forget last, apart, in red, and it asks first. */
@Composable
private fun HostMenu(item: HostItem, pairing: Boolean, actions: HostsActions, onDetails: () -> Unit) {
    val p = item.profile
    var open by remember { mutableStateOf(false) }
    val pick = { action: () -> Unit ->
        open = false
        action()
    }
    Box {
        QuietButton(onClick = { open = true }, modifier = Modifier.width(56.dp)) {
            Icon(painterResource(R.drawable.ic_more_horiz), contentDescription = stringResource(R.string.action_more), modifier = Modifier.size(20.dp))
        }
        DropdownMenu(
            expanded = open, onDismissRequest = { open = false }, containerColor = DeskTheme.colors.card,
            shape = DeskShape, border = BorderStroke(1.dp, DeskTheme.colors.line), shadowElevation = 0.dp, tonalElevation = 0.dp,
        ) {
            MenuEntry(R.string.action_check) { pick { actions.check(p) } }
            MenuEntry(R.string.action_test_connection) { pick { actions.test(p) } }
            if (offersWake(p, item.health)) MenuEntry(R.string.action_wake) { pick { actions.wake(p) } }
            if (p.paired) MenuEntry(R.string.action_repair, enabled = !pairing) { pick { actions.pair(p) } }
            MenuEntry(R.string.action_settings) { pick { actions.openHostSettings(p) } }
            MenuEntry(R.string.action_details) { pick(onDetails) }
            HorizontalDivider(color = DeskTheme.colors.line)
            DropdownMenuItem(
                text = { Text(stringResource(R.string.action_forget), color = DeskTheme.colors.dangerText) },
                onClick = { pick { actions.forget(p) } },
            )
        }
    }
}

@Composable
private fun MenuEntry(@StringRes label: Int, enabled: Boolean = true, onClick: () -> Unit) {
    DropdownMenuItem(text = { Text(stringResource(label)) }, enabled = enabled, onClick = onClick)
}

/** What the page leaves out: the full status, the version advice, the address and the pairing. */
@Composable
private fun HostDetails(item: HostItem, onDismiss: () -> Unit) {
    val p = item.profile
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(p.name) },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Text(fullStatus(item), style = MaterialTheme.typography.bodyMedium)
                updateAdvice(item)?.let { Text(it, style = MaterialTheme.typography.bodyMedium, color = DeskTheme.colors.warningText) }
                Text(
                    if (p.paired) stringResource(R.string.host_paired, shownAddress(p), p.fingerprint.orEmpty().take(16))
                    else stringResource(R.string.host_not_paired, shownAddress(p)),
                    style = MaterialTheme.typography.bodyMedium, color = DeskTheme.colors.muted,
                )
            }
        },
        confirmButton = { QuietButton(onClick = onDismiss) { Text(stringResource(R.string.action_close)) } },
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

/** Whether [item] did not answer and Connect can wake it: it reads as asleep, not failed. */
private fun asleep(item: HostItem): Boolean {
    val h = item.health as? HostHealth.Offline ?: return false
    return h.why == HostHealth.Why.NO_ANSWER && offersWake(item.profile, h)
}

/**
 * A paired host's failure the page explains: it refused, did not wake, or
 * did not answer and Connect cannot wake it; null otherwise, as for a host
 * that only sleeps.
 */
private fun unreachable(item: HostItem): HostHealth.Offline? {
    val h = item.health as? HostHealth.Offline ?: return null
    return h.takeIf { item.profile.paired && !asleep(item) }
}

/** What failed, with the computer's name and the count, and what to check. */
@Composable
private fun OfflineBanner(name: String, h: HostHealth.Offline) {
    when (h.why) {
        HostHealth.Why.REFUSED -> ErrorBanner(
            stringResource(R.string.err_refused_title, name),
            stringResource(R.string.err_refused_body, name, h.address.orEmpty()),
        )
        HostHealth.Why.NOT_WOKEN -> ErrorBanner(stringResource(R.string.err_not_woken_title, name), stringResource(R.string.err_not_woken_body))
        HostHealth.Why.NO_ANSWER -> ErrorBanner(
            stringResource(R.string.err_did_not_answer, name),
            pluralStringResource(R.plurals.err_did_not_answer_body, h.tried, h.tried),
        )
    }
}

/**
 * GNOME's status colours: online green, waking yellow, asleep grey for a
 * host Connect can wake, unreachable red for one it cannot, grey while
 * unknown.
 */
@Composable
private fun statusColor(item: HostItem): Color = when (item.health) {
    is HostHealth.Online -> DeskTheme.colors.online
    is HostHealth.Offline -> if (asleep(item)) DeskTheme.colors.asleep else DeskTheme.colors.unreachable
    HostHealth.Waking -> DeskTheme.colors.waking
    HostHealth.Checking, null -> DeskTheme.colors.asleep
}

/** The list's status: "Online · 4 ms", "Asleep · Connect wakes it", and whether the host is paired. */
@Composable
private fun shortStatus(item: HostItem): String {
    val status = when (val h = item.health) {
        is HostHealth.Online -> stringResource(R.string.status_online_short, h.ms)
        is HostHealth.Offline -> stringResource(
            when {
                asleep(item) -> R.string.status_offline_wakes
                h.why == HostHealth.Why.REFUSED -> R.string.status_refused
                h.why == HostHealth.Why.NOT_WOKEN -> R.string.status_not_woken
                else -> R.string.status_offline_short
            },
        )
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
