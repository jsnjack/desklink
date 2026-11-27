package com.desklink.client.ui

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import com.desklink.client.R

/** A settings page's width: one readable column. */
internal val SETTINGS_MAX_WIDTH = 640.dp

/**
 * The tablet's settings on a page of their own, opened from the main page's
 * gear: Security and Input, the same for every computer, then About with
 * the app's [version] and Share logs. Each computer's settings are on its
 * own page, from its card. Back, or the system back gesture, returns to the
 * main page.
 */
@Composable
fun SettingsScreen(state: SettingsUiState, version: String, actions: SettingsActions, onShareLogs: () -> Unit, onBack: () -> Unit) {
    Surface(Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.surface) {
        Column(Modifier.fillMaxSize().safeDrawingPadding()) {
            Row(Modifier.fillMaxWidth().height(64.dp).padding(start = 4.dp, end = 16.dp), verticalAlignment = Alignment.CenterVertically) {
                IconButton(onClick = onBack) {
                    Icon(painterResource(R.drawable.ic_arrow_back), contentDescription = stringResource(R.string.action_back))
                }
                Text(stringResource(R.string.action_settings), style = MaterialTheme.typography.titleLarge)
            }
            Box(Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(horizontal = 16.dp), contentAlignment = Alignment.TopCenter) {
                Column(Modifier.widthIn(max = SETTINGS_MAX_WIDTH).fillMaxWidth().padding(bottom = 24.dp)) {
                    SettingsPanel(state, actions)
                    SectionHeader(R.string.settings_about, inset = 0.dp, top = 12.dp)
                    SettingRow(stringResource(R.string.app_name), version.takeIf { it.isNotEmpty() }?.let { stringResource(R.string.about_version, it) }, inset = 0.dp) {
                        OutlinedButton(onClick = onShareLogs) { Text(stringResource(R.string.action_share_log)) }
                    }
                }
            }
        }
    }
}
