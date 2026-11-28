package com.desklink.client.ui

import android.Manifest
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.layout.wrapContentHeight
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Slider
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.desklink.client.R
import com.desklink.client.audio.MicPolicy
import com.desklink.client.input.TouchMode
import com.desklink.client.model.HostProfile
import com.desklink.client.model.HostSettings
import com.desklink.client.model.OwnScale
import com.desklink.client.model.ScreenMode
import com.desklink.client.model.StreamPreset
import com.desklink.client.video.HardwareDecoder
import kotlin.math.roundToInt

/**
 * What a computer's own page shows: the computer's [hostName], its
 * [settings], the [codec] its sessions ask for, whether the tablet can
 * decode AV1 ([av1Available]), the [addresses] it is tried at and its
 * [mac] for Wake-on-LAN.
 */
data class HostSettingsUiState(
    val hostName: String,
    val settings: HostSettings,
    val codec: String,
    val av1Available: Boolean,
    val addresses: List<String> = emptyList(),
    val mac: String? = null,
)

/** What a computer's own page's controls do; each change is saved for that computer. */
interface HostSettingsActions {
    /** Chooses the stream preset. */
    fun setPreset(preset: StreamPreset)

    /** Chooses the codec, a [HardwareDecoder] codec name. */
    fun setCodec(codec: String)

    /** Turns Sharp Colour on or off. */
    fun setSharperColours(on: Boolean)

    /** Turns Exact Text on or off. */
    fun setExactText(on: Boolean)

    /** Chooses what sessions show, a [ScreenMode] value. */
    fun setScreenMode(mode: String)

    /** Chooses whether the tablet plays the desktop's sound. */
    fun setSound(on: Boolean)

    /** Makes Second Desk's scale one step smaller (-1) or larger (+1). */
    fun changeScale(direction: Int)

    /** Chooses whether Second Desk takes the top bar and overview. */
    fun setPrimary(on: Boolean)

    /** Chooses whether Second Desk turns the desk's monitors off. */
    fun setDeskOff(off: Boolean)

    /** Turns Tablet Mic on or off, asking for the permission first when needed. */
    fun setMic(on: Boolean)

    /** Sets how long the computer stays awake after a session, a KeepAwake value. */
    fun setKeepAwake(minutes: Int)

    /** Chooses whether the desk locks when a session ends. */
    fun setLockAfter(on: Boolean)

    /** Chooses how touches on the video act. */
    fun setTouchMode(mode: TouchMode)

    /** Sets the touchscreen trackpad's speed. */
    fun setTrackpadSpeed(speed: Float)

    /** Sets how far finger scrolls move. */
    fun setScrollSpeed(speed: Float)

    /** Tests every address of the computer. */
    fun testConnection()

    /** Asks whether to forget the computer. */
    fun forget()
}

/** How tall every row is at least (docs/design.md, Spacing). */
private val ROW = 56.dp

/**
 * One computer's own page, opened from the main page: "‹ DeskLink", the
 * computer's name as the big title, and its settings on white cards in the
 * order a session meets them: When I Connect, Second Desk, Picture, Input
 * and When I Leave, then Connection with its addresses, Wake-on-LAN and
 * Test connection, and Forget last, in red. A session with the computer
 * starts with these settings, and its HUD changes the same ones.
 */
@Composable
fun HostSettingsScreen(state: HostSettingsUiState, actions: HostSettingsActions, onBack: () -> Unit) {
    val s = state.settings
    Surface(Modifier.fillMaxSize(), color = DeskTheme.colors.page, contentColor = DeskTheme.colors.text) {
        Box(Modifier.fillMaxSize().safeDrawingPadding().verticalScroll(rememberScrollState()).padding(horizontal = 24.dp), contentAlignment = Alignment.TopCenter) {
            Column(Modifier.widthIn(max = SETTINGS_MAX_WIDTH).fillMaxWidth().padding(bottom = 32.dp)) {
                Box(Modifier.heightIn(min = TITLE_AIR), contentAlignment = Alignment.CenterStart) {
                    BackLink(stringResource(R.string.app_name), onBack)
                }
                PageTitle(state.hostName, null, Modifier.padding(start = 4.dp))
                GroupName(stringResource(R.string.group_when_connect))
                CardGroup(whenIConnect(s, actions))
                if (s.screen != ScreenMode.MIRROR) {
                    GroupName(stringResource(R.string.screen_own))
                    CardGroup(secondDesk(s, actions))
                }
                GroupName(stringResource(R.string.settings_picture))
                CardGroup(picture(state, actions))
                GroupName(stringResource(R.string.settings_input))
                CardGroup(input(s, actions))
                GroupName(stringResource(R.string.group_when_leave))
                CardGroup(whenILeave(s, actions))
                GroupName(stringResource(R.string.group_connection))
                CardGroup(connection(state, actions))
            }
        }
    }
}

private typealias Rows = List<(@Composable () -> Unit)?>

private fun whenIConnect(s: HostSettings, actions: HostSettingsActions): Rows = listOf(
    {
        SettingRow(stringResource(R.string.label_screen), stringResource(screenExplain(s.screen)), minHeight = ROW) {
            ChoiceSegments(screenOptions, s.screen, actions::setScreenMode)
        }
    },
    {
        MenuRow(
            stringResource(R.string.label_quality), stringResource(presetShort(s.preset)), presetChoices(), s.preset, actions::setPreset,
            minHeight = ROW,
        )
    },
    { SwitchRow(stringResource(R.string.label_sound), null, s.sound, actions::setSound, minHeight = ROW) },
    { SwitchRow(stringResource(R.string.label_mic), stringResource(R.string.mic_explain), s.mic, actions::setMic, minHeight = ROW) },
)

private fun secondDesk(s: HostSettings, actions: HostSettingsActions): Rows = listOf(
    {
        // The whole scales carry a ✓: GNOME stretches XWayland apps at the others.
        SettingRow(stringResource(R.string.label_scale), stringResource(R.string.hud_scale_sharp), minHeight = ROW) {
            ScaleStepper(s.scale, actions::changeScale)
        }
    },
    { SwitchRow(stringResource(R.string.label_primary), stringResource(R.string.primary_setting_explain), s.primary, actions::setPrimary, minHeight = ROW) },
    { SwitchRow(stringResource(R.string.label_desk_off), stringResource(R.string.desk_off_setting_explain), s.deskOff, actions::setDeskOff, minHeight = ROW) },
)

private fun picture(state: HostSettingsUiState, actions: HostSettingsActions): Rows {
    val s = state.settings
    return listOf(
        {
            SettingRow(
                stringResource(R.string.label_codec), if (state.av1Available) null else stringResource(R.string.codec_no_av1), minHeight = ROW,
            ) {
                ChoiceSegments(codecOptions, state.codec, actions::setCodec, enabled = { it != HardwareDecoder.CODEC_AV1 || state.av1Available })
            }
        },
        {
            SwitchRow(
                stringResource(R.string.label_sharper_colours), stringResource(R.string.sharper_colours_explain),
                s.sharperColours, actions::setSharperColours, minHeight = ROW,
            )
        },
        {
            // Exact tiles are drawn by the GPU renderer, so they need Sharp Colour.
            SwitchRow(
                stringResource(R.string.label_exact_text),
                stringResource(if (s.sharperColours) R.string.exact_text_explain else R.string.exact_text_needs_sharper),
                s.exactText && s.sharperColours, actions::setExactText, enabled = s.sharperColours, minHeight = ROW,
            )
        },
    )
}

private fun input(s: HostSettings, actions: HostSettingsActions): Rows = listOf(
    {
        SettingRow(stringResource(R.string.label_touch), null, minHeight = ROW) {
            ChoiceSegments(
                listOf(TouchMode.DIRECT to R.string.touch_direct, TouchMode.TRACKPAD to R.string.touch_trackpad),
                s.touch, actions::setTouchMode,
            )
        }
    },
    if (s.touch == TouchMode.TRACKPAD) {
        {
            SettingRow(stringResource(R.string.label_trackpad_speed), stringResource(R.string.value_speed, s.trackpadSpeed), minHeight = ROW) {
                Slider(value = s.trackpadSpeed, onValueChange = actions::setTrackpadSpeed, valueRange = TRACKPAD_SPEEDS, modifier = Modifier.width(220.dp))
            }
        }
    } else {
        null
    },
    {
        SettingRow(stringResource(R.string.label_scroll_speed), stringResource(R.string.value_speed, s.scrollSpeed), minHeight = ROW) {
            Slider(value = s.scrollSpeed, onValueChange = actions::setScrollSpeed, valueRange = SCROLL_SPEEDS, modifier = Modifier.width(220.dp))
        }
    },
)

private fun whenILeave(s: HostSettings, actions: HostSettingsActions): Rows = listOf(
    { SwitchRow(stringResource(R.string.label_lock_desk), stringResource(R.string.lock_after_explain), s.lockAfter, actions::setLockAfter, minHeight = ROW) },
    { MenuRow(stringResource(R.string.label_keep_it_awake), null, keepAwakeChoices(), s.keepAwake, actions::setKeepAwake, minHeight = ROW) },
)

private fun connection(state: HostSettingsUiState, actions: HostSettingsActions): Rows = listOf(
    {
        SettingRow(
            stringResource(R.string.label_addresses),
            state.addresses.joinToString(" · ").ifEmpty { stringResource(R.string.value_unknown) }, minHeight = ROW,
        ) {}
    },
    {
        SettingRow(
            stringResource(R.string.label_wake_on_lan),
            state.mac?.let { stringResource(R.string.wake_ready, it) } ?: stringResource(R.string.wake_not_ready), minHeight = ROW,
        ) {}
    },
    {
        SettingRow(
            stringResource(R.string.action_test_connection), stringResource(R.string.test_connection_explain),
            Modifier.clickable(onClick = actions::testConnection), minHeight = ROW,
        ) { Text("›", style = MaterialTheme.typography.titleMedium, color = DeskTheme.colors.link) }
    },
    {
        Text(
            stringResource(R.string.action_forget_named, state.hostName), style = MaterialTheme.typography.titleSmall,
            color = DeskTheme.colors.dangerText,
            modifier = Modifier.fillMaxWidth().clickable(onClick = actions::forget).heightIn(min = ROW)
                .wrapContentHeight(Alignment.CenterVertically).padding(horizontal = 16.dp),
        )
    },
)

/** Auto, Second Desk and Mirror as segments. */
private val screenOptions = listOf(
    ScreenMode.AUTO to R.string.screen_auto,
    ScreenMode.OWN to R.string.screen_own,
    ScreenMode.MIRROR to R.string.screen_mirror,
)

/** − and + around Second Desk's scale, which carries ✓ at the scales that keep every app sharp. */
@Composable
private fun ScaleStepper(scale: Double, onStep: (Int) -> Unit) {
    Row(verticalAlignment = Alignment.CenterVertically) {
        StepButton(R.string.action_scale_down, scale > OwnScale.MIN + 0.01) { onStep(-1) }
        Text(
            stringResource(if (OwnScale.isSharp(scale)) R.string.value_percent_sharp else R.string.value_percent, (scale * 100).roundToInt()),
            style = MaterialTheme.typography.labelLarge, textAlign = TextAlign.Center, maxLines = 1, modifier = Modifier.width(72.dp),
        )
        StepButton(R.string.action_scale_up, scale < OwnScale.MAX - 0.01) { onStep(1) }
    }
}

/** A small square button with a 1 dp line, for − and +. */
@Composable
private fun StepButton(label: Int, enabled: Boolean, onClick: () -> Unit) {
    val shape = RoundedCornerShape(10.dp)
    Box(
        Modifier.size(36.dp).clip(shape).border(1.dp, DeskTheme.colors.line, shape).background(DeskTheme.colors.card)
            .clickable(enabled = enabled, role = Role.Button, onClick = onClick),
        contentAlignment = Alignment.Center,
    ) {
        Text(
            stringResource(label), style = MaterialTheme.typography.titleMedium,
            color = if (enabled) DeskTheme.colors.link else DeskTheme.colors.muted,
        )
    }
}

/** The explanation of a [ScreenMode] value. */
internal fun screenExplain(mode: String): Int = when (mode) {
    ScreenMode.OWN -> R.string.screen_own_explain
    ScreenMode.MIRROR -> R.string.screen_mirror_explain
    else -> R.string.screen_auto_explain
}

/**
 * The settings page of [host], bound to [vm]. Turning the microphone on the
 * first time asks for RECORD_AUDIO, as the HUD does.
 */
@Composable
fun HostSettingsRoute(vm: AppViewModel, host: HostProfile, onForget: (HostProfile) -> Unit, onBack: () -> Unit) {
    val all by vm.hostSettings.collectAsStateWithLifecycle()
    val settings = all[host.id] ?: vm.settingsOf(host.id)
    val id = host.id
    val askMic = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        if (MicPolicy.answered(granted) == MicPolicy.Step.TURN_ON) vm.setMic(id, true) else vm.micRefused()
    }
    val actions = remember(vm, id, askMic) {
        object : HostSettingsActions {
            override fun setPreset(preset: StreamPreset) = vm.setPreset(id, preset)

            override fun setCodec(codec: String) = vm.setCodec(id, codec)

            override fun setSharperColours(on: Boolean) = vm.setSharperColours(id, on)

            override fun setExactText(on: Boolean) = vm.setExactText(id, on)

            override fun setScreenMode(mode: String) = vm.setScreenMode(id, mode)

            override fun setSound(on: Boolean) = vm.setPlayAudio(id, on)

            override fun changeScale(direction: Int) = vm.changeScale(id, direction)

            override fun setPrimary(on: Boolean) = vm.setPrimary(id, on)

            override fun setDeskOff(off: Boolean) = vm.setDeskOff(id, off)

            override fun setMic(on: Boolean) {
                when (MicPolicy.toggle(on, vm.micPermitted())) {
                    MicPolicy.Step.TURN_ON -> vm.setMic(id, true)
                    MicPolicy.Step.TURN_OFF -> vm.setMic(id, false)
                    MicPolicy.Step.ASK_PERMISSION -> askMic.launch(Manifest.permission.RECORD_AUDIO)
                    MicPolicy.Step.EXPLAIN_REFUSAL -> vm.micRefused()
                }
            }

            override fun setKeepAwake(minutes: Int) = vm.setKeepAwake(id, minutes)

            override fun setLockAfter(on: Boolean) = vm.setLockAfter(id, on)

            override fun setTouchMode(mode: TouchMode) = vm.setTouchMode(id, mode)

            override fun setTrackpadSpeed(speed: Float) = vm.setSensitivity(id, speed)

            override fun setScrollSpeed(speed: Float) = vm.setScrollSpeed(id, speed)

            override fun testConnection() {
                vm.profiles.value.firstOrNull { it.id == id }?.let(vm::testConnection)
            }

            override fun forget() {
                vm.profiles.value.firstOrNull { it.id == id }?.let(onForget)
            }
        }
    }
    HostSettingsScreen(
        HostSettingsUiState(host.name, settings, vm.codecOf(settings), vm.av1Available, host.candidates(), host.mac),
        actions, onBack,
    )
}
