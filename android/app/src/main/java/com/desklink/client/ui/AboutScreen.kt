package com.desklink.client.ui

import android.content.Context
import android.content.Intent
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.requiredSize
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.core.net.toUri
import com.desklink.client.R

/** Where the source lives, which About links to. */
const val SOURCE_URL = "https://github.com/jsnjack/desklink"

/** The bundled fonts' licence texts, in the APK's assets. */
private val FONT_LICENCES = listOf("licences/montserrat-OFL.txt", "licences/noto-OFL.txt")

/**
 * About DeskLink, from the tablet's settings: the icon, the version and
 * the licence, a few plain sentences on why it is free software, then the
 * source code, Share logs and the fonts' licences.
 */
@Composable
fun AboutScreen(version: String, onShareLogs: () -> Unit, onBack: () -> Unit) {
    val context = LocalContext.current
    var licences by remember { mutableStateOf(false) }
    SettingsPage(stringResource(R.string.about_title_short), onBack, back = stringResource(R.string.action_settings)) {
        Row(Modifier.padding(start = 4.dp, top = 20.dp, bottom = 16.dp), verticalAlignment = Alignment.CenterVertically) {
            AppIcon(52.dp)
            Spacer(Modifier.width(16.dp))
            Column {
                Text(
                    if (version.isEmpty()) stringResource(R.string.app_name) else stringResource(R.string.about_name_version, version),
                    style = MaterialTheme.typography.titleMedium,
                )
                Text(stringResource(R.string.about_licence), style = MaterialTheme.typography.bodySmall, color = DeskTheme.colors.muted)
            }
        }
        Text(
            stringResource(R.string.about_free), style = MaterialTheme.typography.bodyMedium,
            modifier = Modifier.fillMaxWidth().clip(DeskShape).background(DeskTheme.colors.card).padding(CARD_PADDING),
        )
        Gap(12.dp)
        CardGroup(
            listOf(
                { LinkRow(stringResource(R.string.about_source), SOURCE_URL.removePrefix("https://")) { openUrl(context, SOURCE_URL) } },
                { LinkRow(stringResource(R.string.action_share_log), stringResource(R.string.share_log_explain), onShareLogs) },
                { LinkRow(stringResource(R.string.about_fonts), stringResource(R.string.about_fonts_explain)) { licences = true } },
            ),
        )
    }
    if (licences) FontLicences { licences = false }
}

/** The launcher icon as drawn: the adaptive icon's two layers, cropped to their safe zone. */
@Composable
fun AppIcon(size: Dp) {
    // The layers are 108 dp canvases whose middle 66 dp the launcher shows.
    val canvas = size * 108f / 66f
    Box(Modifier.size(size).clip(RoundedCornerShape(size * 0.28f)), contentAlignment = Alignment.Center) {
        Image(painterResource(R.drawable.ic_launcher_background), null, Modifier.requiredSize(canvas), contentScale = ContentScale.FillBounds)
        Image(painterResource(R.drawable.ic_launcher_foreground), null, Modifier.requiredSize(canvas), contentScale = ContentScale.FillBounds)
    }
}

/** The SIL Open Font Licence texts of Montserrat and Noto, which the licence asks to ship with the fonts. */
@Composable
private fun FontLicences(onDismiss: () -> Unit) {
    val context = LocalContext.current
    val text = remember { FONT_LICENCES.joinToString("\n\n\n") { name -> context.assets.open(name).bufferedReader().use { it.readText() } } }
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(stringResource(R.string.about_fonts)) },
        text = {
            Column(Modifier.heightIn(max = 480.dp).verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Text(text, style = MaterialTheme.typography.bodySmall)
            }
        },
        confirmButton = { QuietButton(onClick = onDismiss) { Text(stringResource(R.string.action_close)) } },
    )
}

private fun openUrl(context: Context, url: String) {
    runCatching { context.startActivity(Intent(Intent.ACTION_VIEW, url.toUri())) }
}
