package com.desklink.client.ui

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.layout.wrapContentWidth
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import com.desklink.client.R
import com.desklink.client.model.NearbyHost

/** The widest the pairing page's steps grow. */
private val PAIR_MAX_WIDTH = 760.dp

/**
 * Pair Your Computer: the first start, and where Add computer leads. Three
 * numbered steps with the commands to run in terminal blocks and Scan code,
 * the unpaired computers found on this network, and Add by address for a
 * computer that is neither found nor scannable. [onBack] is null on the
 * first start, when there is nowhere to go back to; the gear then opens the
 * tablet's settings.
 */
@Composable
fun PairScreen(
    nearby: List<NearbyHost>,
    pairing: Boolean,
    first: Boolean,
    actions: HostsActions,
    onBack: (() -> Unit)?,
) {
    Surface(Modifier.fillMaxSize(), color = DeskTheme.colors.page, contentColor = DeskTheme.colors.text) {
        Column(
            // Surface hands its size down as the minimum; wrapContentWidth lets the cap apply.
            Modifier.safeDrawingPadding().verticalScroll(rememberScrollState()).padding(horizontal = 32.dp)
                .wrapContentWidth(Alignment.Start).widthIn(max = PAIR_MAX_WIDTH).fillMaxWidth().padding(bottom = 32.dp),
        ) {
            Row(Modifier.fillMaxWidth().heightIn(min = TITLE_AIR), verticalAlignment = Alignment.CenterVertically) {
                onBack?.let { BackLink(stringResource(R.string.app_name), it) }
                Spacer(Modifier.weight(1f))
                if (first) {
                    IconButton(onClick = actions::openSettings) {
                        Icon(painterResource(R.drawable.ic_settings), contentDescription = stringResource(R.string.action_settings))
                    }
                }
            }
            PageTitle(stringResource(R.string.pair_title), if (first) stringResource(R.string.empty_title) else null)
            Gap(24.dp)
            Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
                StepCard(1, stringResource(R.string.pair_step_install)) {
                    Text(stringResource(R.string.pair_step_install_where), style = MaterialTheme.typography.bodySmall, color = DeskTheme.colors.muted)
                    TerminalBlock(stringResource(R.string.pair_command_install))
                }
                StepCard(2, stringResource(R.string.pair_step_code)) {
                    TerminalBlock(stringResource(R.string.pair_command_pair))
                    Text(stringResource(R.string.pair_step_code_explain), style = MaterialTheme.typography.bodySmall, color = DeskTheme.colors.muted)
                }
                StepCard(3, stringResource(R.string.pair_step_scan)) {
                    Text(stringResource(R.string.pair_step_scan_explain), style = MaterialTheme.typography.bodySmall, color = DeskTheme.colors.muted)
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        PrimaryButton(onClick = actions::scanQr, enabled = !pairing) { Text(stringResource(R.string.action_scan_code)) }
                        if (pairing) {
                            Spacer(Modifier.width(12.dp))
                            CircularProgressIndicator(Modifier.size(20.dp), strokeWidth = 2.dp)
                        }
                    }
                }
            }
            if (nearby.isNotEmpty()) {
                GroupName(stringResource(R.string.nearby_found))
                CardGroup(nearby.map { n -> { NearbyRow(n, pairing) { actions.pairNearby(n) } } })
            }
            Gap(16.dp)
            TextButton(onClick = actions::addHost) {
                Text(stringResource(R.string.action_add_by_address), style = MaterialTheme.typography.labelLarge, color = DeskTheme.colors.link)
            }
            Text(
                stringResource(R.string.pair_by_address_explain), style = MaterialTheme.typography.bodySmall, color = DeskTheme.colors.muted,
                modifier = Modifier.padding(start = 12.dp),
            )
        }
    }
}

/**
 * An unpaired computer found on this network: a grey dot, its name and
 * address, and Pair, which still scans the code, since the announcement
 * proves nothing.
 */
@Composable
fun NearbyRow(n: NearbyHost, pairing: Boolean, onPair: () -> Unit) {
    Row(
        Modifier.fillMaxWidth().clickable(enabled = !pairing, onClick = onPair).heightIn(min = 64.dp).padding(horizontal = 16.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Dot(DeskTheme.colors.asleep)
        Spacer(Modifier.width(14.dp))
        Column(Modifier.weight(1f)) {
            Text(n.name, style = MaterialTheme.typography.titleSmall, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Text(stringResource(R.string.nearby_subtitle, n.ip), style = MaterialTheme.typography.bodySmall, color = DeskTheme.colors.muted)
        }
        QuietButton(onClick = onPair, enabled = !pairing) { Text(stringResource(R.string.action_pair)) }
    }
}
