package com.desklink.client.ui

import android.Manifest
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedIconButton
import androidx.compose.material3.Slider
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
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
 * What a computer's settings page shows: the computer's [hostName], its
 * [settings], the [codec] its sessions ask for and whether the tablet can
 * decode AV1 ([av1Available]).
 */
data class HostSettingsUiState(
    val hostName: String,
    val settings: HostSettings,
    val codec: String,
    val av1Available: Boolean,
)

/** What a computer's settings page's controls do; each change is saved for that computer. */
interface HostSettingsActions {
    /** Chooses the stream preset. */
    fun setPreset(preset: StreamPreset)

    /** Chooses the codec, a [HardwareDecoder] codec name. */
    fun setCodec(codec: String)

    /** Turns Sharper colours on or off. */
    fun setSharperColours(on: Boolean)

    /** Turns Exact text when still on or off. */
    fun setExactText(on: Boolean)

    /** Chooses what sessions show, a [ScreenMode] value. */
    fun setScreenMode(mode: String)

    /** Chooses whether the tablet plays the desktop's sound. */
    fun setSound(on: Boolean)

    /** Makes the own screen's scale one step smaller (-1) or larger (+1). */
    fun changeScale(direction: Int)

    /** Chooses whether the own screen takes the top bar and overview. */
    fun setPrimary(on: Boolean)

    /** Chooses whether the own screen turns the desk's monitors off. */
    fun setDeskOff(off: Boolean)

    /** Turns the microphone on or off, asking for the permission first when needed. */
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
}

/**
 * How tall every row is at least: as tall as one with a line under its
 * title, so the page's rows line up.
 */
private val ROW = 56.dp

/**
 * One computer's settings on a page of their own, opened from its card's
 * menu: Picture, Session and Input in one column. A session with the
 * computer starts with them, and its HUD changes the same settings. Back,
 * or the system back gesture, returns to the main page.
 */
@Composable
fun HostSettingsScreen(state: HostSettingsUiState, actions: HostSettingsActions, onBack: () -> Unit) {
    Surface(Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.surface) {
        Column(Modifier.fillMaxSize().safeDrawingPadding()) {
            Row(Modifier.fillMaxWidth().height(64.dp).padding(start = 4.dp, end = 16.dp), verticalAlignment = Alignment.CenterVertically) {
                IconButton(onClick = onBack) {
                    Icon(painterResource(R.drawable.ic_arrow_back), contentDescription = stringResource(R.string.action_back))
                }
                Text(
                    stringResource(R.string.host_settings_title, state.hostName), style = MaterialTheme.typography.titleLarge,
                    maxLines = 1, overflow = TextOverflow.Ellipsis,
                )
            }
            Box(Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(horizontal = 16.dp), contentAlignment = Alignment.TopCenter) {
                Column(Modifier.widthIn(max = SETTINGS_MAX_WIDTH).fillMaxWidth().padding(bottom = 24.dp)) {
                    Picture(state, actions)
                    Session(state.settings, actions)
                    Input(state.settings, actions)
                }
            }
        }
    }
}

@Composable
private fun Picture(state: HostSettingsUiState, actions: HostSettingsActions) {
    val s = state.settings
    SectionHeader(R.string.settings_picture, inset = 0.dp, top = 12.dp)
    MenuRow(
        stringResource(R.string.label_quality), stringResource(s.preset.explain), presetChoices(), s.preset, actions::setPreset,
        inset = 0.dp, minHeight = ROW,
    )
    SettingRow(
        stringResource(R.string.label_codec), if (state.av1Available) null else stringResource(R.string.codec_no_av1),
        inset = 0.dp, minHeight = ROW,
    ) {
        ChoiceSegments(codecOptions, state.codec, actions::setCodec, enabled = { it != HardwareDecoder.CODEC_AV1 || state.av1Available })
    }
    SwitchRow(
        stringResource(R.string.label_sharper_colours), stringResource(R.string.sharper_colours_explain),
        s.sharperColours, actions::setSharperColours, inset = 0.dp, minHeight = ROW,
    )
    // Exact tiles are drawn by the GPU renderer, so they need Sharper colours.
    SwitchRow(
        stringResource(R.string.label_exact_text),
        stringResource(if (s.sharperColours) R.string.exact_text_explain else R.string.exact_text_needs_sharper),
        s.exactText && s.sharperColours, actions::setExactText,
        enabled = s.sharperColours, inset = 0.dp, minHeight = ROW,
    )
}

@Composable
private fun Session(s: HostSettings, actions: HostSettingsActions) {
    SectionHeader(R.string.hud_session, inset = 0.dp, top = 12.dp)
    MenuRow(
        stringResource(R.string.label_screen), stringResource(screenExplain(s.screen)), screenChoices(), s.screen, actions::setScreenMode,
        inset = 0.dp, minHeight = ROW,
    )
    SwitchRow(stringResource(R.string.label_sound), null, s.sound, actions::setSound, inset = 0.dp, minHeight = ROW)
    // The whole scales carry a ✓: GNOME stretches XWayland apps at the others.
    SettingRow(stringResource(R.string.label_scale), stringResource(R.string.hud_scale_sharp), inset = 0.dp, minHeight = ROW) {
        ScaleStepper(s.scale, actions::changeScale)
    }
    SwitchRow(
        stringResource(R.string.label_primary), stringResource(R.string.primary_setting_explain),
        s.primary, actions::setPrimary, inset = 0.dp, minHeight = ROW,
    )
    SwitchRow(
        stringResource(R.string.label_desk_off), stringResource(R.string.desk_off_setting_explain),
        s.deskOff, actions::setDeskOff, inset = 0.dp, minHeight = ROW,
    )
    SwitchRow(stringResource(R.string.label_mic), stringResource(R.string.mic_explain), s.mic, actions::setMic, inset = 0.dp, minHeight = ROW)
    MenuRow(
        stringResource(R.string.hud_keep_awake), null, keepAwakeChoices(), s.keepAwake, actions::setKeepAwake,
        inset = 0.dp, minHeight = ROW,
    )
    SwitchRow(
        stringResource(R.string.label_lock_after), stringResource(R.string.lock_after_explain),
        s.lockAfter, actions::setLockAfter, inset = 0.dp, minHeight = ROW,
    )
}

@Composable
private fun Input(s: HostSettings, actions: HostSettingsActions) {
    SectionHeader(R.string.settings_input, inset = 0.dp, top = 12.dp)
    SettingRow(stringResource(R.string.label_touch), null, inset = 0.dp, minHeight = ROW) {
        ChoiceSegments(
            listOf(TouchMode.DIRECT to R.string.touch_direct, TouchMode.TRACKPAD to R.string.touch_trackpad),
            s.touch, actions::setTouchMode,
        )
    }
    if (s.touch == TouchMode.TRACKPAD) {
        SettingRow(
            stringResource(R.string.label_trackpad_speed), stringResource(R.string.value_speed, s.trackpadSpeed),
            inset = 0.dp, minHeight = ROW,
        ) {
            Slider(value = s.trackpadSpeed, onValueChange = actions::setTrackpadSpeed, valueRange = TRACKPAD_SPEEDS, modifier = Modifier.width(220.dp))
        }
    }
    SettingRow(
        stringResource(R.string.label_scroll_speed), stringResource(R.string.value_speed, s.scrollSpeed),
        inset = 0.dp, minHeight = ROW,
    ) {
        Slider(value = s.scrollSpeed, onValueChange = actions::setScrollSpeed, valueRange = SCROLL_SPEEDS, modifier = Modifier.width(220.dp))
    }
}

/** − and + around the own screen's scale, which carries ✓ at the scales that keep every app sharp. */
@Composable
private fun ScaleStepper(scale: Double, onStep: (Int) -> Unit) {
    Row(verticalAlignment = Alignment.CenterVertically) {
        OutlinedIconButton(onClick = { onStep(-1) }, enabled = scale > OwnScale.MIN + 0.01) {
            Text(stringResource(R.string.action_scale_down), style = MaterialTheme.typography.titleMedium)
        }
        Text(
            stringResource(if (OwnScale.isSharp(scale)) R.string.value_percent_sharp else R.string.value_percent, (scale * 100).roundToInt()),
            style = MaterialTheme.typography.labelLarge, textAlign = TextAlign.Center, maxLines = 1, modifier = Modifier.width(72.dp),
        )
        OutlinedIconButton(onClick = { onStep(1) }, enabled = scale < OwnScale.MAX - 0.01) {
            Text(stringResource(R.string.action_scale_up), style = MaterialTheme.typography.titleMedium)
        }
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
fun HostSettingsRoute(vm: AppViewModel, host: HostProfile, onBack: () -> Unit) {
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
        }
    }
    HostSettingsScreen(HostSettingsUiState(host.name, settings, vm.codecOf(settings), vm.av1Available), actions, onBack)
}
