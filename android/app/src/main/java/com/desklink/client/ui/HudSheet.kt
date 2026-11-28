package com.desklink.client.ui

import androidx.annotation.StringRes
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
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
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.PlainTooltip
import androidx.compose.material3.Slider
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TooltipAnchorPosition
import androidx.compose.material3.TooltipBox
import androidx.compose.material3.TooltipDefaults
import androidx.compose.material3.rememberTooltipState
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.res.colorResource
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
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
    /** Closes the tools: the rail or the sheet. */
    fun close()

    /** Opens the settings sheet from the rail. */
    fun openSettings()

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

    /** Sets how far finger scrolls move. */
    fun setScrollSpeed(speed: Float)

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
 * The session's settings sheet, opened from the rail: a full-height,
 * frosted white sheet on the pill's side over the video. At the top the
 * host and one status line, then the Screen, Picture and Session settings
 * on grey groups (a long press explains a row), the input speeds, and
 * Disconnect at the bottom, apart from the rest. Every change applies to
 * the session at once and is saved for the host. Everything between the
 * header and Disconnect scrolls when the window is too short for it.
 *
 * The sheet is a [Surface], so every touch on it stays with it and none
 * reaches the desktop beneath.
 */
@Composable
fun HudSheet(state: HudUiState, actions: HudActions, modifier: Modifier = Modifier) {
    Surface(
        modifier.fillMaxHeight().padding(8.dp).width(SHEET_WIDTH),
        shape = DeskShape,
        color = frost(),
        contentColor = DeskTheme.colors.text,
        border = BorderStroke(1.dp, DeskTheme.colors.line),
    ) {
        Column(Modifier.padding(top = 8.dp, bottom = 12.dp)) {
            Header(state, actions::close)
            Column(Modifier.weight(1f).verticalScroll(rememberScrollState()).padding(horizontal = 12.dp)) {
                state.screen?.let { ScreenSection(it, actions) }
                state.picture?.let { PictureSection(it, actions) }
                state.session?.let { SessionSection(it, actions) }
                if (state.session != null) InputSection(state, actions)
            }
            QuietButton(onClick = actions::disconnect, modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp)) {
                Icon(painterResource(R.drawable.ic_hud_disconnect), contentDescription = null, tint = DeskTheme.colors.dangerText, modifier = Modifier.size(18.dp))
                Spacer(Modifier.width(8.dp))
                Text(stringResource(R.string.action_disconnect), color = DeskTheme.colors.dangerText)
            }
        }
    }
}

/** The host's name, the status line and ✕. */
@Composable
private fun Header(state: HudUiState, onClose: () -> Unit) {
    Row(Modifier.fillMaxWidth().padding(start = 16.dp, end = 4.dp, bottom = 4.dp), verticalAlignment = Alignment.CenterVertically) {
        Column(Modifier.weight(1f)) {
            Text(state.hostName, style = MaterialTheme.typography.titleMedium, maxLines = 1, overflow = TextOverflow.Ellipsis)
            LinkLine(state.link)
        }
        IconButton(onClick = onClose) {
            Icon(painterResource(R.drawable.ic_hud_close), contentDescription = stringResource(R.string.action_close), tint = DeskTheme.colors.muted)
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
            style = MaterialTheme.typography.bodySmall, color = DeskTheme.colors.muted,
            maxLines = 2, overflow = TextOverflow.Ellipsis,
        )
    }
}

/** A group of the sheet's rows: its name in Blue Ink, then the rows on the page's grey. */
@Composable
private fun SheetGroup(@StringRes title: Int, content: @Composable ColumnScope.() -> Unit) {
    Text(
        stringResource(title), style = MaterialTheme.typography.labelMedium, color = DeskTheme.colors.link,
        modifier = Modifier.padding(start = 4.dp, top = 14.dp, bottom = 6.dp),
    )
    Column(Modifier.fillMaxWidth().clip(DeskShape).background(DeskTheme.colors.page), content = content)
}

/** Own screen or Mirror, the monitor while mirroring several, the own screen's scale, Primary and Desk off. */
@Composable
private fun ScreenSection(screen: HudScreen, actions: HudActions) {
    SheetGroup(R.string.hud_screen) {
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
}

/** A small square − or + beside the scale, with a 1 dp line. */
@Composable
private fun StepButton(@StringRes label: Int, enabled: Boolean, onClick: () -> Unit) {
    val shape = RoundedCornerShape(10.dp)
    Box(
        Modifier.size(36.dp).clip(shape).border(1.dp, DeskTheme.colors.line, shape).background(DeskTheme.colors.card)
            .clickable(enabled = enabled, role = Role.Button, onClick = onClick),
        contentAlignment = Alignment.Center,
    ) {
        Text(stringResource(label), style = MaterialTheme.typography.titleMedium, color = if (enabled) DeskTheme.colors.link else DeskTheme.colors.muted)
    }
}

/** The quality preset, the codec, Sharper colours and Exact text, which needs Sharper colours. */
@Composable
private fun PictureSection(picture: HudPicture, actions: HudActions) {
    SheetGroup(R.string.settings_picture) {
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
}

/** The trackpad's speed, in trackpad mode, and the scroll speed. */
@Composable
private fun InputSection(state: HudUiState, actions: HudActions) {
    SheetGroup(R.string.settings_input) {
        if (state.trackpad) {
            SettingRow(stringResource(R.string.label_trackpad_speed), stringResource(R.string.value_speed, state.trackpadSpeed), minHeight = ROW) {
                Slider(value = state.trackpadSpeed, onValueChange = actions::setTrackpadSpeed, valueRange = TRACKPAD_SPEEDS, modifier = Modifier.width(160.dp))
            }
        }
        SettingRow(stringResource(R.string.label_scroll_speed), stringResource(R.string.value_speed, state.scrollSpeed), minHeight = ROW) {
            Slider(value = state.scrollSpeed, onValueChange = actions::setScrollSpeed, valueRange = SCROLL_SPEEDS, modifier = Modifier.width(160.dp))
        }
    }
}

/** Sound, the microphone, how long the computer stays awake afterwards, and whether the desk locks. */
@Composable
private fun SessionSection(session: HudSession, actions: HudActions) {
    SheetGroup(R.string.hud_session) {
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
