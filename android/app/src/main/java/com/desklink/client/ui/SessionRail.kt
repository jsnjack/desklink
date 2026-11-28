package com.desklink.client.ui

import androidx.annotation.DrawableRes
import androidx.annotation.StringRes
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.detectDragGestures
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.unit.IntSize
import androidx.compose.ui.unit.dp
import com.desklink.client.R
import kotlin.math.roundToInt

/**
 * Frosted white over the desktop (docs/design.md, Depth). The design asks
 * for 86 % with a blur; Compose cannot blur the decoder's SurfaceView
 * behind it, so the panels are a little more opaque instead.
 */
@Composable
fun frost(): Color = DeskTheme.colors.card.copy(alpha = 0.92f)

/** The pill's size on the edge (docs/design.md, A session). */
private val PILL_WIDTH = 20.dp
private val PILL_HEIGHT = 38.dp

/** How far beyond the pill a touch still takes it: 20 dp is too narrow for a finger. */
private val PILL_REACH = 24.dp

/**
 * The pill that opens the session's tools: light and frosted, 20 × 38 dp,
 * on a side edge. A tap opens the rail; a drag moves it, and when let go it
 * sticks to the nearer side edge at the height it was dropped, which is
 * remembered. Only the pill takes touches; the rest reaches the desktop.
 */
@Composable
fun SessionPill(saved: HudPill, onOpen: () -> Unit, onMoved: (HudPill) -> Unit) {
    BoxWithConstraints(Modifier.fillMaxSize()) {
        val maxW = constraints.maxWidth
        val maxH = constraints.maxHeight
        var size by remember { mutableStateOf(IntSize.Zero) }
        // While dragging, the pill follows the finger freely; dragX and dragY
        // are its offset from the saved place.
        var dragX by remember { mutableFloatStateOf(0f) }
        var dragY by remember { mutableFloatStateOf(0f) }
        val baseX = if (saved.left) 0 else maxW - size.width
        val baseY = saved.y * (maxH - size.height).coerceAtLeast(0)
        val open = stringResource(R.string.hud_open_tools)
        Box(
            Modifier
                .offset {
                    IntOffset(
                        (baseX + dragX).roundToInt().coerceIn(0, (maxW - size.width).coerceAtLeast(0)),
                        (baseY + dragY).roundToInt().coerceIn(0, (maxH - size.height).coerceAtLeast(0)),
                    )
                }
                .onSizeChanged { size = it }
                .semantics { contentDescription = open }
                .pointerInput(Unit) { detectTapGestures { onOpen() } }
                .pointerInput(maxW, maxH, saved) {
                    detectDragGestures(
                        onDrag = { change, delta ->
                            change.consume()
                            dragX += delta.x
                            dragY += delta.y
                        },
                        onDragEnd = {
                            val centreX = baseX + dragX + size.width / 2f
                            val travel = (maxH - size.height).coerceAtLeast(1)
                            val y = ((baseY + dragY) / travel).coerceIn(0f, 1f)
                            dragX = 0f
                            dragY = 0f
                            onMoved(HudPill(left = centreX < maxW / 2f, y = y))
                        },
                        onDragCancel = {
                            dragX = 0f
                            dragY = 0f
                        },
                    )
                }
                .padding(vertical = 8.dp)
                .width(PILL_WIDTH + PILL_REACH),
            contentAlignment = if (saved.left) Alignment.CenterStart else Alignment.CenterEnd,
        ) {
            val shape = if (saved.left) RoundedCornerShape(topEnd = 10.dp, bottomEnd = 10.dp)
            else RoundedCornerShape(topStart = 10.dp, bottomStart = 10.dp)
            Box(
                Modifier.size(PILL_WIDTH, PILL_HEIGHT).clip(shape).background(frost()).border(1.dp, DeskTheme.colors.line, shape),
                contentAlignment = Alignment.Center,
            ) {
                // The grip: two short lines.
                Column(verticalArrangement = Arrangement.spacedBy(3.dp)) {
                    repeat(2) { Box(Modifier.size(8.dp, 1.5.dp).background(DeskTheme.colors.muted, RoundedCornerShape(1.dp))) }
                }
            }
        }
    }
}

/**
 * The rail of tools that the pill turns into, along the same edge at the
 * same height: the grip, which closes it, Keyboard, Trackpad, Apps,
 * Overview, Print Screen, the clipboard both ways, Stats, the settings
 * sheet and, apart and in red, Disconnect. Keyboard, Trackpad and Stats
 * show on Blue Tint while on. A tap beside the rail closes it, and that tap
 * does not reach the desktop.
 */
@Composable
fun ToolRail(state: HudUiState, actions: HudActions, place: HudPill) {
    BoxWithConstraints(Modifier.fillMaxSize().pointerInput(Unit) { detectTapGestures { actions.close() } }) {
        val maxW = constraints.maxWidth
        val maxH = constraints.maxHeight
        var size by remember { mutableStateOf(IntSize.Zero) }
        val margin = with(LocalDensity.current) { 8.dp.roundToPx() }
        Column(
            Modifier
                .offset {
                    val x = if (place.left) margin else maxW - size.width - margin
                    // Centred on where the pill sits, kept on screen.
                    val pillY = place.y * maxH
                    IntOffset(x, (pillY - size.height / 2f).roundToInt().coerceIn(margin, (maxH - size.height - margin).coerceAtLeast(margin)))
                }
                .onSizeChanged { size = it }
                .clip(DeskShape).background(frost()).border(1.dp, DeskTheme.colors.line, DeskShape)
                // Touches on the rail stay with it.
                .pointerInput(Unit) { detectTapGestures { } }
                .padding(vertical = 6.dp, horizontal = 4.dp),
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            RailButton(R.drawable.ic_hud_grip, R.string.hud_close_tools, onClick = actions::close)
            RailButton(R.drawable.ic_hud_keyboard, R.string.hud_keyboard, on = state.keyboardShown, onClick = actions::toggleKeyboard)
            RailButton(R.drawable.ic_hud_mouse, R.string.hud_trackpad, on = state.trackpad, onClick = actions::toggleTrackpad)
            RailButton(R.drawable.ic_hud_apps, R.string.hud_apps, onClick = actions::apps)
            RailButton(R.drawable.ic_hud_overview, R.string.hud_overview, onClick = actions::overview)
            // Android keeps the keyboard's Print Screen for its own screenshot; this is that key for the desktop.
            RailButton(R.drawable.ic_hud_screenshot, R.string.hud_print_screen, onClick = actions::printScreen)
            RailButton(R.drawable.ic_hud_send, R.string.hud_send_clipboard, onClick = actions::sendClipboard)
            RailButton(R.drawable.ic_hud_get, R.string.hud_get_clipboard, onClick = actions::getClipboard)
            RailButton(R.drawable.ic_hud_stats, R.string.hud_stats, on = state.statsShown, onClick = actions::toggleStats)
            RailButton(R.drawable.ic_hud_settings, R.string.hud_settings, onClick = actions::openSettings)
            HorizontalDivider(Modifier.width(28.dp).padding(vertical = 4.dp), color = DeskTheme.colors.line)
            RailButton(R.drawable.ic_hud_disconnect, R.string.action_disconnect, tint = DeskTheme.colors.dangerText, onClick = actions::disconnect)
        }
    }
}

/** One tool: its icon, named for screen readers; a tool that switches something ([on] not null) sits on Blue Tint while on. */
@Composable
private fun RailButton(
    @DrawableRes icon: Int,
    @StringRes label: Int,
    on: Boolean? = null,
    tint: Color = DeskTheme.colors.text,
    onClick: () -> Unit,
) {
    val active = on == true
    val name = stringResource(label)
    Box(
        Modifier.size(48.dp).clip(RoundedCornerShape(14.dp))
            .background(if (active) DeskTheme.colors.blueTint else Color.Transparent)
            .clickable(onClickLabel = name, role = Role.Button, onClick = onClick)
            .semantics {
                contentDescription = name
                if (on != null) selected = on
            },
        contentAlignment = Alignment.Center,
    ) {
        Icon(painterResource(icon), contentDescription = null, tint = if (active) DeskTheme.colors.link else tint, modifier = Modifier.size(22.dp))
    }
}
