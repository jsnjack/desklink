package com.desklink.client.ui

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import com.desklink.client.R

/** A settings page's width: one readable column. */
internal val SETTINGS_MAX_WIDTH = 640.dp

/**
 * The tablet's settings on a page of their own, opened from the main page's
 * gear: Security and Input, the same for every computer, then About
 * DeskLink and Share logs. Each computer's settings are on its own page.
 * "‹ DeskLink", or the system back gesture, returns to the main page.
 */
@Composable
fun SettingsScreen(state: SettingsUiState, version: String, actions: SettingsActions, onShareLogs: () -> Unit, onAbout: () -> Unit, onBack: () -> Unit) {
    SettingsPage(stringResource(R.string.action_settings), onBack) {
        SettingsPanel(state, actions)
        GroupName(stringResource(R.string.settings_about))
        CardGroup(
            listOf(
                { LinkRow(stringResource(R.string.about_title), version.takeIf { it.isNotEmpty() }?.let { stringResource(R.string.about_version, it) }, onAbout) },
                { LinkRow(stringResource(R.string.action_share_log), stringResource(R.string.share_log_explain), onShareLogs) },
            ),
        )
    }
}

/**
 * A page of settings: "‹ DeskLink", the big [title] and the [content]'s
 * cards in one column at most [SETTINGS_MAX_WIDTH] wide.
 */
@Composable
fun SettingsPage(title: String, onBack: () -> Unit, back: String = stringResource(R.string.app_name), content: @Composable () -> Unit) {
    Surface(Modifier.fillMaxSize(), color = DeskTheme.colors.page, contentColor = DeskTheme.colors.text) {
        Box(
            Modifier.fillMaxSize().safeDrawingPadding().verticalScroll(rememberScrollState()).padding(horizontal = 24.dp),
            contentAlignment = Alignment.TopCenter,
        ) {
            Column(Modifier.widthIn(max = SETTINGS_MAX_WIDTH).fillMaxWidth().padding(bottom = 32.dp)) {
                Box(Modifier.heightIn(min = TITLE_AIR), contentAlignment = Alignment.CenterStart) { BackLink(back, onBack) }
                PageTitle(title, null, Modifier.padding(start = 4.dp))
                content()
            }
        }
    }
}

/** A row that opens something: its title, a line under it and ›, the whole row a tap target. */
@Composable
fun LinkRow(title: String, subtitle: String?, onClick: () -> Unit) {
    SettingRow(title, subtitle, Modifier.clickable(onClick = onClick), minHeight = 56.dp) {
        Text(stringResource(R.string.link_arrow), style = MaterialTheme.typography.titleMedium, color = DeskTheme.colors.link)
    }
}
