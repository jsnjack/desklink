package com.desklink.client.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalInspectionMode
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import com.desklink.client.R

/** The air above a page's big title (docs/design.md, Spacing). */
val TITLE_AIR = 48.dp

/** How far cards pad their content. */
val CARD_PADDING = 20.dp

/**
 * A page's big title in Display, with [state] under it in muted text: the
 * title says the state, not a decoration ("1 of 2 computers online").
 */
@Composable
fun PageTitle(title: String, state: String?, modifier: Modifier = Modifier) {
    Column(modifier, verticalArrangement = Arrangement.spacedBy(4.dp)) {
        Text(title, style = MaterialTheme.typography.displaySmall, maxLines = 1, overflow = TextOverflow.Ellipsis)
        state?.let { Text(it, style = MaterialTheme.typography.bodyMedium, color = DeskTheme.colors.muted) }
    }
}

/** "‹ DeskLink": the way back from a page, as a link above its title. */
@Composable
fun BackLink(label: String, onBack: () -> Unit, modifier: Modifier = Modifier) {
    TextButton(onClick = onBack, modifier = modifier, contentPadding = PaddingValues(horizontal = 4.dp)) {
        Icon(painterResource(R.drawable.ic_arrow_back), contentDescription = null, tint = DeskTheme.colors.link, modifier = Modifier.size(18.dp))
        Spacer(Modifier.width(4.dp))
        Text(label, style = MaterialTheme.typography.labelLarge, color = DeskTheme.colors.link)
    }
}

/**
 * Settings that belong together on one white card, with a 1 dp line
 * between rows; null rows are left out, and nothing shows without rows.
 */
@Composable
fun CardGroup(rows: List<(@Composable () -> Unit)?>, modifier: Modifier = Modifier) {
    val shown = rows.filterNotNull()
    if (shown.isEmpty()) return
    Column(modifier.fillMaxWidth().clip(DeskShape).background(DeskTheme.colors.card)) {
        shown.forEachIndexed { i, row ->
            if (i > 0) HorizontalDivider(Modifier.padding(horizontal = 16.dp), thickness = 1.dp, color = DeskTheme.colors.line)
            row()
        }
    }
}

/** A group's name above its [CardGroup]. */
@Composable
fun GroupName(text: String, modifier: Modifier = Modifier) {
    Text(
        text, style = MaterialTheme.typography.labelMedium, color = DeskTheme.colors.link,
        modifier = modifier.padding(start = 16.dp, top = 16.dp, bottom = 6.dp),
    )
}

/** A command or code as typed, in a terminal block: the one dark surface. */
@Composable
fun TerminalBlock(text: String, modifier: Modifier = Modifier) {
    SelectionContainer(modifier) {
        Text(
            text, style = MonoStyle, color = Color(0xFFF6F5F4),
            modifier = Modifier.clip(RoundedCornerShape(8.dp)).background(DeskTheme.colors.terminal).padding(horizontal = 12.dp, vertical = 8.dp),
        )
    }
}

/**
 * An error where it happened: what failed in [title], with exact names and
 * counts, and what to check in [body].
 */
@Composable
fun ErrorBanner(title: String, body: String?, modifier: Modifier = Modifier) {
    Column(
        modifier.fillMaxWidth().clip(DeskShape).background(MaterialTheme.colorScheme.errorContainer).padding(horizontal = 16.dp, vertical = 12.dp),
        verticalArrangement = Arrangement.spacedBy(4.dp),
    ) {
        Text(title, style = MaterialTheme.typography.labelLarge, color = MaterialTheme.colorScheme.onErrorContainer)
        body?.let { Text(it, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onErrorContainer) }
    }
}

/** Shows activity onscreen and keeps previews on a single frame. */
@Composable
internal fun BusyIndicator(modifier: Modifier, strokeWidth: Dp, color: Color = MaterialTheme.colorScheme.primary) {
    if (LocalInspectionMode.current) {
        CircularProgressIndicator(progress = { 0.7f }, modifier = modifier, strokeWidth = strokeWidth, color = color)
    } else {
        CircularProgressIndicator(modifier, strokeWidth = strokeWidth, color = color)
    }
}

/** A note that is not an error, such as the metered network's cost, on Blue Tint. */
@Composable
fun NoteBanner(text: String, modifier: Modifier = Modifier) {
    Text(
        text, style = MaterialTheme.typography.bodySmall, color = DeskTheme.colors.text,
        modifier = modifier.fillMaxWidth().clip(DeskShape).background(DeskTheme.colors.blueTint).padding(horizontal = 16.dp, vertical = 12.dp),
    )
}

/** A status dot: colour never carries the meaning alone, so words always sit beside it. */
@Composable
fun Dot(color: Color, size: Dp = 10.dp) {
    Box(Modifier.size(size).background(color, CircleShape))
}

/** A numbered step's number on Blue Tint. */
@Composable
fun StepNumber(n: Int) {
    Box(Modifier.size(28.dp).background(DeskTheme.colors.blueTint, CircleShape), contentAlignment = Alignment.Center) {
        Text(n.toString(), style = MaterialTheme.typography.labelMedium, color = DeskTheme.colors.link)
    }
}

/** One numbered step on a card: what to do in [title], then its [content]. */
@Composable
fun StepCard(n: Int, title: String, modifier: Modifier = Modifier, content: @Composable () -> Unit = {}) {
    Row(modifier.fillMaxWidth().clip(DeskShape).background(DeskTheme.colors.card).padding(CARD_PADDING)) {
        StepNumber(n)
        Spacer(Modifier.width(16.dp))
        Column(Modifier.weight(1f).padding(top = 4.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Text(title, style = MaterialTheme.typography.titleSmall)
            content()
        }
    }
}

/** An empty height in the 4-point grid, for the space between groups. */
@Composable
fun Gap(height: Dp = 12.dp) = Spacer(Modifier.height(height))
