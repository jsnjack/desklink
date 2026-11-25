package com.desklink.client.ui

import androidx.annotation.StringRes
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
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
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.selection.selectableGroup
import androidx.compose.foundation.selection.toggleable
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.MenuDefaults
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.VerticalDivider
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
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import com.desklink.client.R

/** How faint a disabled row's text is, as Material's disabled content. */
private const val DISABLED_ALPHA = 0.38f

/** How tall a row of a settings page is at least; the HUD's denser rows pass less. */
private val ROW_HEIGHT = 56.dp

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
        Column(Modifier.weight(1f).alpha(if (enabled) 1f else DISABLED_ALPHA)) {
            Text(title, style = MaterialTheme.typography.bodyLarge)
            subtitle?.let {
                Text(it, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
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
        Switch(checked = checked, onCheckedChange = null, enabled = enabled)
    }
}

/**
 * Segments for a choice of a few [options], each a value and its label, all
 * as wide as the longest label needs. The fill shows the choice, without a
 * check mark. Material's SegmentedButton keeps room for its check mark even
 * when it has none, which clips a long label in a row sized to its labels.
 */
@Composable
fun <T> ChoiceSegments(
    options: List<Pair<T, Int>>,
    selected: T,
    onSelect: (T) -> Unit,
    modifier: Modifier = Modifier,
    enabled: (T) -> Boolean = { true },
) {
    val outline = MaterialTheme.colorScheme.outline
    val shape = RoundedCornerShape(percent = 50)
    Row(
        modifier.width(IntrinsicSize.Max).height(IntrinsicSize.Min).clip(shape).border(1.dp, outline, shape).selectableGroup(),
    ) {
        options.forEachIndexed { i, (value, label) ->
            if (i > 0) VerticalDivider(color = outline)
            val on = value == selected
            val usable = enabled(value)
            Box(
                Modifier.weight(1f).fillMaxHeight()
                    .background(if (on) MaterialTheme.colorScheme.secondaryContainer else Color.Transparent)
                    .selectable(selected = on, enabled = usable, role = Role.RadioButton) { if (!on) onSelect(value) }
                    .heightIn(min = 40.dp).padding(horizontal = 14.dp),
                contentAlignment = Alignment.Center,
            ) {
                Text(
                    stringResource(label), style = MaterialTheme.typography.labelLarge, maxLines = 1, softWrap = false,
                    color = if (on) MaterialTheme.colorScheme.onSecondaryContainer else MaterialTheme.colorScheme.onSurface,
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
 * shows the chosen one with ▾ in an outlined pill like [ChoiceSegments], and
 * a tap anywhere on the row opens a menu of them all, the chosen one filled.
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
    val shape = RoundedCornerShape(percent = 50)
    SettingRow(
        title, subtitle, modifier.clickable(role = Role.DropdownList) { open = true },
        inset = inset, minHeight = minHeight,
    ) {
        Box {
            Row(
                Modifier.heightIn(min = 40.dp).clip(shape).border(1.dp, MaterialTheme.colorScheme.outline, shape)
                    .padding(start = 14.dp, end = 6.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Text(choices.firstOrNull { it.value == selected }?.shown.orEmpty(), style = MaterialTheme.typography.labelLarge, maxLines = 1)
                Icon(painterResource(R.drawable.ic_arrow_drop_down), contentDescription = null)
            }
            // The highest container stands out on the HUD's dark sheet as on a page.
            DropdownMenu(expanded = open, onDismissRequest = { open = false }, containerColor = MaterialTheme.colorScheme.surfaceContainerHighest) {
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

/** A group's title in a list of settings. */
@Composable
fun SectionHeader(@StringRes title: Int, modifier: Modifier = Modifier) {
    Text(
        stringResource(title),
        style = MaterialTheme.typography.titleSmall,
        color = MaterialTheme.colorScheme.primary,
        modifier = modifier.padding(start = 16.dp, end = 16.dp, top = 16.dp, bottom = 4.dp),
    )
}
