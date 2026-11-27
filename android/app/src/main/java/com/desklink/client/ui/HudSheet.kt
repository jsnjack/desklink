package com.desklink.client.ui

import androidx.annotation.DrawableRes
import androidx.annotation.StringRes
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.PlainTooltip
import androidx.compose.material3.Slider
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TooltipAnchorPosition
import androidx.compose.material3.TooltipBox
import androidx.compose.material3.TooltipDefaults
import androidx.compose.material3.rememberTooltipState
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.res.colorResource
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import com.desklink.client.R
import com.desklink.client.model.ScreenMode
import com.desklink.client.model.StreamPreset
import com.desklink.client.video.HardwareDecoder
import kotlin.math.roundToInt

/** What the HUD sheet's controls do. */
interface HudActions {
    /** Closes the sheet. */
    fun close()

    /** Shows or hides the soft keyboard and closes the sheet. */
    fun toggleKeyboard()

    /** Switches the touchscreen between tap to click and trackpad, and closes the sheet. */
    fun toggleTrackpad()

    /** Opens GNOME's app grid (Win+A) and closes the sheet. */
    fun apps()

    /** Opens GNOME's overview (Win) and closes the sheet. */
    fun overview()

    /** Sends the Android clipboard to the desktop. */
    fun sendClipboard()

    /** Asks the desktop for its clipboard. */
    fun getClipboard()

    /** Shows or hides the stats overlay. */
    fun toggleStats()

    /** Presses Print Screen on the desktop and closes the sheet. */
    fun printScreen()

    /** Switches the session to [mode], a [ScreenMode] value. */
    fun setScreen(mode: String)

    /** Chooses the stream preset; the stream changes at once. */
    fun setPreset(preset: StreamPreset)

    /** Chooses the codec, a [HardwareDecoder] codec name; the stream changes at once. */
    fun setCodec(codec: String)

    /** Turns Sharper colours on or off at once. */
    fun setSharperColours(on: Boolean)

    /** Turns Exact text when still on or off. */
    fun setExactText(on: Boolean)

    /** Starts or stops playing the desktop's sound. */
    fun setSound(on: Boolean)

    /** Sets the touchscreen trackpad's speed. */
    fun setTrackpadSpeed(speed: Float)

    /** Mirrors the monitor on [connector]. */
    fun chooseMonitor(connector: String)

    /** Makes the own screen's scale one step smaller (-1) or larger (+1). */
    fun changeScale(direction: Int)

    /** Makes the own screen GNOME's primary monitor for the session, or gives it back. */
    fun setPrimary(on: Boolean)

    /** Turns the desk's monitors off for the session, or on again. */
    fun setDeskOff(off: Boolean)

    /** Turns the microphone on or off, asking for the permission first when needed. */
    fun setMic(on: Boolean)

    /** Sets how long the computer stays awake after the session, a [KeepAwake] value. */
    fun setKeepAwake(minutes: Int)

    /** Sets whether the desk locks when the session ends. */
    fun setLockAfter(on: Boolean)

    /** Ends the session. */
    fun disconnect()
}

/** How wide the sheet is. */
private val SHEET_WIDTH = 368.dp

/** The sheet's rows are denser than a settings page's but keep a 48 dp touch target. */
private val ROW = 48.dp

/** How wide a long-press explanation grows. */
private val TIP_WIDTH = 320.dp

/**
 * The HUD: a full-height sheet on the right edge over the video. At the top
 * the host and one status line, then the eight actions as tiles, the
 * Screen, Picture and Session settings as one-line rows (a long press
 * explains each), the trackpad's speed in trackpad mode, and Disconnect at
 * the bottom, apart from the rest. Every change applies to the session at
 * once and is saved for the host. Everything between the header and
 * Disconnect scrolls when the window is too short for it.
 *
 * It takes its colours from a dark [MaterialTheme] ([HudTheme]), since it
 * sits over video in both themes. The sheet is a [Surface], so every touch
 * on it stays with it and none reaches the desktop beneath.
 */
@Composable
fun HudSheet(state: HudUiState, actions: HudActions, modifier: Modifier = Modifier) {
    Surface(
        modifier.fillMaxHeight().padding(8.dp).width(SHEET_WIDTH),
        shape = RoundedCornerShape(20.dp),
        color = colorResource(R.color.hud_scrim),
        contentColor = MaterialTheme.colorScheme.onSurface,
    ) {
        Column(Modifier.padding(top = 8.dp, bottom = 4.dp)) {
            Header(state, actions::close)
            Column(Modifier.weight(1f).verticalScroll(rememberScrollState())) {
                Tiles(state, actions)
                state.screen?.let { ScreenSection(it, actions) }
                state.picture?.let { PictureSection(it, actions) }
                state.session?.let { SessionSection(it, actions) }
                if (state.trackpad && state.session != null) InputSection(state.trackpadSpeed, actions)
            }
            HorizontalDivider(Modifier.padding(horizontal = 16.dp), color = MaterialTheme.colorScheme.outlineVariant)
            Disconnect(actions::disconnect)
        }
    }
}

/** The host's name, the status line and ✕. */
@Composable
private fun Header(state: HudUiState, onClose: () -> Unit) {
    Row(Modifier.fillMaxWidth().padding(start = 16.dp, end = 4.dp), verticalAlignment = Alignment.CenterVertically) {
        Column(Modifier.weight(1f)) {
            Text(state.hostName, style = MaterialTheme.typography.titleLarge, maxLines = 1, overflow = TextOverflow.Ellipsis)
            LinkLine(state.link)
        }
        IconButton(onClick = onClose) {
            Icon(painterResource(R.drawable.ic_hud_close), contentDescription = stringResource(R.string.action_close))
        }
    }
}

/** "● Excellent · 9 ms · 120 fps", or a spinner with Connecting… or Reconnecting. */
@Composable
private fun LinkLine(link: HudLink) {
    Row(verticalAlignment = Alignment.CenterVertically) {
        if (link is HudLink.Live) {
            Box(Modifier.size(8.dp).background(colorResource(gradeOf(link.quality).second), CircleShape))
        } else {
            CircularProgressIndicator(Modifier.size(10.dp), strokeWidth = 1.5.dp)
        }
        Spacer(Modifier.width(8.dp))
        Text(
            when (link) {
                HudLink.Connecting -> stringResource(R.string.label_connecting)
                is HudLink.Reconnecting -> stringResource(R.string.label_reconnecting, link.attempt, link.of)
                is HudLink.Live -> stringResource(R.string.hud_link, stringResource(gradeOf(link.quality).first), link.rttMs, link.fps)
            },
            style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant,
            maxLines = 2, overflow = TextOverflow.Ellipsis,
        )
    }
}

/** The eight actions, four to a row; Keyboard, Trackpad and Stats are filled while on. */
@Composable
private fun Tiles(state: HudUiState, actions: HudActions) {
    Column(Modifier.padding(start = 16.dp, end = 16.dp, top = 12.dp, bottom = 4.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            val tile = Modifier.weight(1f)
            Tile(R.drawable.ic_hud_keyboard, R.string.hud_keyboard, tile, on = state.keyboardShown, onClick = actions::toggleKeyboard)
            Tile(R.drawable.ic_hud_mouse, R.string.hud_trackpad, tile, on = state.trackpad, onClick = actions::toggleTrackpad)
            Tile(R.drawable.ic_hud_apps, R.string.hud_apps, tile, onClick = actions::apps)
            Tile(R.drawable.ic_hud_overview, R.string.hud_overview, tile, onClick = actions::overview)
        }
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            val tile = Modifier.weight(1f)
            Tile(R.drawable.ic_hud_send, R.string.hud_send_clip, tile, description = R.string.hud_send_clipboard, onClick = actions::sendClipboard)
            Tile(R.drawable.ic_hud_get, R.string.hud_get_clip, tile, description = R.string.hud_get_clipboard, onClick = actions::getClipboard)
            Tile(R.drawable.ic_hud_stats, R.string.hud_stats, tile, on = state.statsShown, onClick = actions::toggleStats)
            // Android keeps the keyboard's Print Screen for its own
            // screenshot; this tile is that key for the desktop.
            Tile(R.drawable.ic_hud_screenshot, R.string.hud_screenshot, tile, description = R.string.hud_print_screen, onClick = actions::printScreen)
        }
    }
}

/**
 * One action: an icon over a one-line [label], with the longer
 * [description] for screen readers. A tile that switches something ([on]
 * not null) is filled while it is on.
 */
@Composable
private fun Tile(
    @DrawableRes icon: Int,
    @StringRes label: Int,
    modifier: Modifier = Modifier,
    @StringRes description: Int = label,
    on: Boolean? = null,
    onClick: () -> Unit,
) {
    val active = on == true
    val content = if (active) MaterialTheme.colorScheme.onPrimaryContainer else MaterialTheme.colorScheme.onSurface
    Column(
        modifier.clip(RoundedCornerShape(12.dp))
            .background(if (active) MaterialTheme.colorScheme.primaryContainer else colorResource(R.color.hud_tile))
            .clickable(onClickLabel = stringResource(description), role = Role.Button, onClick = onClick)
            .semantics { if (on != null) selected = on }
            .padding(top = 10.dp, bottom = 8.dp, start = 2.dp, end = 2.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.spacedBy(4.dp),
    ) {
        Icon(painterResource(icon), contentDescription = null, tint = content, modifier = Modifier.size(22.dp))
        Text(
            stringResource(label), color = content, style = MaterialTheme.typography.labelMedium,
            maxLines = 1, overflow = TextOverflow.Ellipsis,
        )
    }
}

/** Own screen or Mirror, the monitor while mirroring several, the own screen's scale, Primary and Desk off. */
@Composable
private fun ScreenSection(screen: HudScreen, actions: HudActions) {
    SectionHeader(R.string.hud_screen)
    Tip(stringResource(R.string.hud_mode_tip)) {
        SettingRow(stringResource(R.string.hud_mode), null, minHeight = ROW) {
            ChoiceSegments(
                listOf(ScreenMode.OWN to R.string.screen_own, ScreenMode.MIRROR to R.string.screen_mirror),
                screen.mode, actions::setScreen,
            )
        }
    }
    // A tap mirrors that monitor at once and is remembered for the host.
    if (screen.monitors.isNotEmpty()) {
        MenuRow(
            stringResource(R.string.hud_monitor), null,
            screen.monitors.map {
                MenuChoice(
                    it.connector, it.connector,
                    detail = stringResource(if (it.primary) R.string.hud_monitor_primary else R.string.value_size, it.width, it.height),
                )
            },
            screen.mirroring, actions::chooseMonitor, minHeight = ROW,
        )
    }
    screen.scale?.let { scale ->
        // The whole scales carry a ✓: GNOME stretches XWayland apps at the others.
        Tip(stringResource(R.string.scale_sharp_hint)) {
            SettingRow(stringResource(R.string.label_scale), stringResource(R.string.hud_scale_sharp), minHeight = ROW) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    StepButton(R.string.action_scale_down, screen.canShrink) { actions.changeScale(-1) }
                    Text(
                        stringResource(if (screen.sharp) R.string.value_percent_sharp else R.string.value_percent, (scale * 100).roundToInt()),
                        style = MaterialTheme.typography.labelLarge, textAlign = TextAlign.Center, maxLines = 1,
                        modifier = Modifier.width(64.dp),
                    )
                    StepButton(R.string.action_scale_up, screen.canGrow) { actions.changeScale(1) }
                }
            }
        }
    }
    // Only hosts that can move GNOME's primary monitor report primary, and
    // only sessions beside an unlocked desk may switch the desk off.
    screen.primary?.let { on ->
        Tip(stringResource(R.string.primary_explain)) {
            SwitchRow(stringResource(R.string.label_primary), stringResource(R.string.hud_primary_short), on, actions::setPrimary, minHeight = ROW)
        }
    }
    screen.deskOff?.let { off ->
        Tip(stringResource(R.string.desk_off_explain)) {
            SwitchRow(stringResource(R.string.label_desk_off), stringResource(R.string.hud_desk_off_short), off, actions::setDeskOff, minHeight = ROW)
        }
    }
}

/** A round − or + beside the scale. */
@Composable
private fun StepButton(@StringRes label: Int, enabled: Boolean, onClick: () -> Unit) {
    Box(
        Modifier.size(40.dp).clip(CircleShape).background(colorResource(R.color.hud_tile))
            .clickable(enabled = enabled, role = Role.Button, onClick = onClick).alpha(if (enabled) 1f else 0.38f),
        contentAlignment = Alignment.Center,
    ) {
        Text(stringResource(label), style = MaterialTheme.typography.titleMedium)
    }
}

/** The quality preset, the codec, Sharper colours and Exact text, which needs Sharper colours. */
@Composable
private fun PictureSection(picture: HudPicture, actions: HudActions) {
    SectionHeader(R.string.settings_picture)
    MenuRow(stringResource(R.string.label_quality), null, presetChoices(), picture.preset, actions::setPreset, minHeight = ROW)
    SettingRow(stringResource(R.string.label_codec), if (picture.av1Available) null else stringResource(R.string.codec_no_av1), minHeight = ROW) {
        ChoiceSegments(codecOptions, picture.codec, actions::setCodec, enabled = { it != HardwareDecoder.CODEC_AV1 || picture.av1Available })
    }
    Tip(stringResource(R.string.sharper_colours_explain)) {
        SwitchRow(stringResource(R.string.label_sharper_colours), null, picture.sharperColours, actions::setSharperColours, minHeight = ROW)
    }
    Tip(stringResource(if (picture.sharperColours) R.string.exact_text_explain else R.string.exact_text_needs_sharper)) {
        SwitchRow(
            stringResource(R.string.label_exact_text), null, picture.exactText && picture.sharperColours, actions::setExactText,
            enabled = picture.sharperColours, minHeight = ROW,
        )
    }
}

/** The trackpad's speed, in trackpad mode. */
@Composable
private fun InputSection(speed: Float, actions: HudActions) {
    SectionHeader(R.string.settings_input)
    SettingRow(stringResource(R.string.label_trackpad_speed), stringResource(R.string.value_speed, speed), minHeight = ROW) {
        Slider(value = speed, onValueChange = actions::setTrackpadSpeed, valueRange = TRACKPAD_SPEEDS, modifier = Modifier.width(160.dp))
    }
}

/** Sound, the microphone, how long the computer stays awake afterwards, and whether the desk locks. */
@Composable
private fun SessionSection(session: HudSession, actions: HudActions) {
    SectionHeader(R.string.hud_session)
    SwitchRow(stringResource(R.string.label_sound), null, session.sound, actions::setSound, minHeight = ROW)
    session.mic?.let { on ->
        Tip(stringResource(R.string.mic_explain)) {
            SwitchRow(stringResource(R.string.label_mic), null, on, actions::setMic, minHeight = ROW)
        }
    }
    Tip(stringResource(R.string.keep_awake_hint)) {
        MenuRow(stringResource(R.string.hud_keep_awake), null, keepAwakeChoices(), session.keepAwake, actions::setKeepAwake, minHeight = ROW)
    }
    // The host reports the setting it uses; changing it here also saves it for later sessions.
    Tip(stringResource(R.string.lock_after_explain)) {
        SwitchRow(stringResource(R.string.label_lock_after), null, session.lockAfter, actions::setLockAfter, minHeight = ROW)
    }
}

/** Disconnect in the error colour, below a divider and away from the tiles. */
@Composable
private fun ColumnScope.Disconnect(onClick: () -> Unit) {
    TextButton(
        onClick = onClick,
        colors = ButtonDefaults.textButtonColors(contentColor = MaterialTheme.colorScheme.error),
        contentPadding = ButtonDefaults.TextButtonWithIconContentPadding,
        modifier = Modifier.align(Alignment.CenterHorizontally).padding(top = 4.dp),
    ) {
        Icon(painterResource(R.drawable.ic_hud_disconnect), contentDescription = null, modifier = Modifier.size(18.dp))
        Spacer(Modifier.width(8.dp))
        Text(stringResource(R.string.action_disconnect))
    }
}

/**
 * [content] with [text] shown above it on a long press, which then does not
 * also tap the row, or while the mouse pointer is over it.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun Tip(text: String, content: @Composable () -> Unit) {
    TooltipBox(
        positionProvider = TooltipDefaults.rememberTooltipPositionProvider(TooltipAnchorPosition.Above),
        tooltip = { PlainTooltip(maxWidth = TIP_WIDTH) { Text(text) } },
        state = rememberTooltipState(),
        content = content,
    )
}
