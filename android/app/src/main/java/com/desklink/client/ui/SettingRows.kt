package com.desklink.client.ui

import androidx.annotation.StringRes
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.IntrinsicSize
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.wrapContentHeight
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.selection.selectableGroup
import androidx.compose.foundation.selection.toggleable
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.MenuDefaults
import androidx.compose.material3.Switch
import androidx.compose.material3.SwitchColors
import androidx.compose.material3.SwitchDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import com.desklink.client.R
import com.desklink.client.model.KeepAwake
import com.desklink.client.model.ScreenMode
import com.desklink.client.model.StreamPreset
import com.desklink.client.video.HardwareDecoder

/** How faint a disabled row's text is, as Material's disabled content. */
private const val DISABLED_ALPHA = 0.38f

/** How tall a row is at least: a touch target. A subtitle or a larger control makes it taller. */
private val ROW_HEIGHT = 48.dp

/**
 * One compact setting: a title with at most a short line under it, and its
 * control on the right. [inset] is the space on both sides, and the row is
 * at least [minHeight] tall.
 */
@Composable
fun SettingRow(
    title: String,
    subtitle: String?,
    modifier: Modifier = Modifier,
    enabled: Boolean = true,
    inset: Dp = 16.dp,
    minHeight: Dp = ROW_HEIGHT,
    control: @Composable () -> Unit,
) {
    Row(
        modifier.fillMaxWidth().heightIn(min = minHeight).padding(horizontal = inset, vertical = 6.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Column(Modifier.weight(1f).alpha(if (enabled) 1f else DISABLED_ALPHA), verticalArrangement = Arrangement.spacedBy(2.dp)) {
            Text(title, style = MaterialTheme.typography.titleSmall)
            subtitle?.let {
                Text(it, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
        }
        Spacer(Modifier.width(16.dp))
        control()
    }
}

/** A [SettingRow] with a switch; a tap anywhere on the row flips it. */
@Composable
fun SwitchRow(
    title: String,
    subtitle: String?,
    checked: Boolean,
    onChange: (Boolean) -> Unit,
    modifier: Modifier = Modifier,
    enabled: Boolean = true,
    inset: Dp = 16.dp,
    minHeight: Dp = ROW_HEIGHT,
) {
    SettingRow(
        title, subtitle,
        modifier.toggleable(value = checked, enabled = enabled, role = Role.Switch, onValueChange = onChange),
        enabled, inset, minHeight,
    ) {
        Switch(checked = checked, onCheckedChange = null, enabled = enabled, colors = deskSwitchColors())
    }
}

/** GNOME's switch: a white knob on Blue Ink when on and on the line grey when off, without an outline. */
@Composable
fun deskSwitchColors(): SwitchColors {
    val c = DeskTheme.colors
    val off = if (c.dark) c.line else Color(0xFFD1D0CD)
    return SwitchDefaults.colors(
        checkedThumbColor = Color.White, checkedTrackColor = c.blueInk, checkedBorderColor = Color.Transparent,
        uncheckedThumbColor = if (c.dark) c.text else Color.White, uncheckedTrackColor = off, uncheckedBorderColor = Color.Transparent,
        disabledCheckedTrackColor = c.blueInk.copy(alpha = DISABLED_ALPHA), disabledCheckedThumbColor = Color.White.copy(alpha = 0.8f),
        disabledUncheckedTrackColor = off.copy(alpha = DISABLED_ALPHA), disabledUncheckedThumbColor = Color.White.copy(alpha = 0.8f),
        disabledUncheckedBorderColor = Color.Transparent, disabledCheckedBorderColor = Color.Transparent,
    )
}

/**
 * Segments for a choice of a few [options], each a value and its label, all
 * as wide as the longest label needs: a grey track with the chosen one as a
 * card on it in Blue Ink, without a check mark. Material's SegmentedButton
 * keeps room for its check mark even when it has none, which clips a long
 * label in a row sized to its labels.
 */
@Composable
fun <T> ChoiceSegments(
    options: List<Pair<T, Int>>,
    selected: T,
    onSelect: (T) -> Unit,
    modifier: Modifier = Modifier,
    enabled: (T) -> Boolean = { true },
) {
    val c = DeskTheme.colors
    val track = if (c.dark) c.page else Color(0xFFEDECEA)
    Row(
        modifier.width(IntrinsicSize.Max).height(IntrinsicSize.Min).clip(DeskShape).background(track).padding(3.dp).selectableGroup(),
    ) {
        options.forEach { (value, label) ->
            val on = value == selected
            val usable = enabled(value)
            val segment = Modifier.weight(1f).fillMaxHeight().clip(DeskShape)
            Box(
                (if (on) segment.background(if (c.dark) c.blueTint else c.card).border(1.dp, c.line, DeskShape) else segment)
                    .selectable(selected = on, enabled = usable, role = Role.RadioButton) { if (!on) onSelect(value) }
                    .heightIn(min = 34.dp).padding(horizontal = 14.dp),
                contentAlignment = Alignment.Center,
            ) {
                Text(
                    stringResource(label), style = MaterialTheme.typography.labelMedium, maxLines = 1, softWrap = false,
                    color = if (on && !c.dark) c.link else c.text,
                    modifier = Modifier.alpha(if (usable) 1f else DISABLED_ALPHA),
                )
            }
        }
    }
}

/**
 * One choice of a [MenuRow]: its value, its label in the menu with an
 * optional [detail] line under it, and the [shown] label the row displays
 * while it is chosen.
 */
data class MenuChoice<T>(val value: T, val label: String, val shown: String = label, val detail: String? = null)

/**
 * A [SettingRow] for a choice of more [choices] than segments hold: the row
 * shows the chosen one as a link, "Auto ›", and a tap anywhere on the row
 * opens a menu of them all, the chosen one filled.
 */
@Composable
fun <T> MenuRow(
    title: String,
    subtitle: String?,
    choices: List<MenuChoice<T>>,
    selected: T,
    onSelect: (T) -> Unit,
    modifier: Modifier = Modifier,
    inset: Dp = 16.dp,
    minHeight: Dp = ROW_HEIGHT,
) {
    var open by remember { mutableStateOf(false) }
    val c = DeskTheme.colors
    SettingRow(
        title, subtitle, modifier.clickable(role = Role.DropdownList) { open = true },
        inset = inset, minHeight = minHeight,
    ) {
        Box {
            Text(
                stringResource(R.string.value_link, choices.firstOrNull { it.value == selected }?.shown.orEmpty()),
                style = MaterialTheme.typography.labelLarge, color = c.link, maxLines = 1,
                modifier = Modifier.heightIn(min = 40.dp).wrapContentHeight(Alignment.CenterVertically),
            )
            // A card with a 1 dp edge instead of a shadow.
            DropdownMenu(
                expanded = open, onDismissRequest = { open = false }, containerColor = c.card,
                shape = DeskShape, border = BorderStroke(1.dp, c.line), shadowElevation = 0.dp, tonalElevation = 0.dp,
            ) {
                for (c in choices) {
                    val on = c.value == selected
                    val text = if (on) MaterialTheme.colorScheme.onSecondaryContainer else MaterialTheme.colorScheme.onSurface
                    DropdownMenuItem(
                        text = {
                            Column {
                                Text(c.label)
                                c.detail?.let {
                                    Text(
                                        it, style = MaterialTheme.typography.bodySmall,
                                        color = if (on) text else MaterialTheme.colorScheme.onSurfaceVariant,
                                    )
                                }
                            }
                        },
                        onClick = {
                            open = false
                            if (!on) onSelect(c.value)
                        },
                        colors = MenuDefaults.itemColors(textColor = text),
                        modifier = if (on) Modifier.background(MaterialTheme.colorScheme.secondaryContainer) else Modifier,
                    )
                }
            }
        }
    }
}

/** A group's title in a list of settings, [inset] from both sides like its rows and [top] below the group before. */
@Composable
fun SectionHeader(@StringRes title: Int, modifier: Modifier = Modifier, inset: Dp = 16.dp, top: Dp = 16.dp) {
    Text(
        stringResource(title),
        style = MaterialTheme.typography.labelMedium,
        color = DeskTheme.colors.link,
        modifier = modifier.padding(start = inset, end = inset, top = top, bottom = 6.dp),
    )
}

/** The range of the trackpad speed slider. */
internal val TRACKPAD_SPEEDS = 0.5f..4f

/** The range of the scroll speed slider. */
internal val SCROLL_SPEEDS = 0.2f..2f

/** The codecs as segments; AV1 needs a hardware decoder. */
internal val codecOptions = listOf(HardwareDecoder.CODEC_AV1 to R.string.codec_av1, HardwareDecoder.CODEC_HEVC to R.string.codec_hevc)

/** The quality presets, each with its explanation; the row shows Auto Quality as "Auto", beside "Quality". */
@Composable
internal fun presetChoices(): List<MenuChoice<StreamPreset>> = StreamPreset.entries.map {
    val label = stringResource(it.label)
    val shown = if (it == StreamPreset.AUTO) stringResource(R.string.preset_auto_short) else label
    MenuChoice(it, label, shown, detail = stringResource(it.explain))
}

/** What sessions show, each choice with its explanation. */
@Composable
internal fun screenChoices(): List<MenuChoice<String>> = listOf(
    MenuChoice(ScreenMode.AUTO, stringResource(R.string.screen_auto), detail = stringResource(R.string.screen_auto_explain)),
    MenuChoice(ScreenMode.OWN, stringResource(R.string.screen_own), detail = stringResource(R.string.screen_own_explain)),
    MenuChoice(ScreenMode.MIRROR, stringResource(R.string.screen_mirror), detail = stringResource(R.string.screen_mirror_explain)),
)

/** How long the computer stays awake after a session. */
@Composable
internal fun keepAwakeChoices(): List<MenuChoice<Int>> = listOf(
    MenuChoice(KeepAwake.OFF, stringResource(R.string.keep_awake_off)),
    MenuChoice(KeepAwake.ONE_HOUR, stringResource(R.string.keep_awake_1h)),
    MenuChoice(KeepAwake.FOUR_HOURS, stringResource(R.string.keep_awake_4h)),
    MenuChoice(KeepAwake.UNTIL_RETURN, stringResource(R.string.keep_awake_return_long), shown = stringResource(R.string.keep_awake_return)),
    MenuChoice(KeepAwake.SUSPEND, stringResource(R.string.keep_awake_sleep), detail = stringResource(R.string.keep_awake_sleep_explain)),
)
