package com.desklink.client.ui

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.heightIn
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.ProvideTextStyle
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.dp

/** Blue Ink's gradient on the main action, top to bottom (docs/design.md, Colour). */
private val ActionGradient = Brush.verticalGradient(listOf(Color(0xFF3B79C9), Color(0xFF306EC0)))

private val ButtonPadding = PaddingValues(horizontal = 20.dp, vertical = 8.dp)

/** A screen's one main action: Blue Ink with white text, 18 dp corners, 44 dp tall at least. */
@Composable
fun PrimaryButton(onClick: () -> Unit, modifier: Modifier = Modifier, enabled: Boolean = true, content: @Composable RowScope.() -> Unit) {
    val c = DeskTheme.colors
    Button(
        onClick = onClick,
        enabled = enabled,
        shape = DeskShape,
        colors = ButtonDefaults.buttonColors(
            containerColor = Color.Transparent, contentColor = Color.White,
            disabledContainerColor = c.line, disabledContentColor = c.muted,
        ),
        contentPadding = ButtonPadding,
        modifier = (if (enabled) modifier.background(ActionGradient, DeskShape) else modifier).heightIn(min = 44.dp),
    ) { ProvideTextStyle(MaterialTheme.typography.labelLarge) { content() } }
}

/** A secondary action: a card with a 1 dp line and the text in the link colour, as Cancel. */
@Composable
fun QuietButton(onClick: () -> Unit, modifier: Modifier = Modifier, enabled: Boolean = true, content: @Composable RowScope.() -> Unit) {
    val c = DeskTheme.colors
    OutlinedButton(
        onClick = onClick,
        enabled = enabled,
        shape = DeskShape,
        border = BorderStroke(1.dp, c.line),
        colors = ButtonDefaults.outlinedButtonColors(containerColor = c.card, contentColor = c.link, disabledContentColor = c.muted),
        contentPadding = ButtonPadding,
        modifier = modifier.heightIn(min = 44.dp),
    ) { ProvideTextStyle(MaterialTheme.typography.labelLarge) { content() } }
}

/** What cannot be undone, in a dialog that names the consequence: red with white text. */
@Composable
fun DangerButton(onClick: () -> Unit, modifier: Modifier = Modifier, content: @Composable RowScope.() -> Unit) {
    Button(
        onClick = onClick,
        shape = DeskShape,
        colors = ButtonDefaults.buttonColors(containerColor = DeskTheme.colors.danger, contentColor = Color.White),
        contentPadding = ButtonPadding,
        modifier = modifier.heightIn(min = 44.dp),
    ) { ProvideTextStyle(MaterialTheme.typography.labelLarge) { content() } }
}
