package com.desklink.client.ui

import android.content.ComponentName
import android.content.Intent
import android.provider.Settings
import androidx.annotation.StringRes
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Slider
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.LifecycleResumeEffect
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.desklink.client.R
import com.desklink.client.input.KeyCapture
import com.desklink.client.input.KeyCaptureService
import com.desklink.client.input.TouchMode
import com.desklink.client.model.AppLockTrigger
import com.desklink.client.video.HardwareDecoder

/**
 * The app-wide settings, the same for every host: [keyCapture] is whether
 * the accessibility service that passes Win and Alt+Tab is on, [screenLock]
 * whether the tablet has a screen lock for the fingerprint-or-PIN prompt to
 * fall back on.
 */
data class SettingsUiState(
    val sharperColours: Boolean,
    val exactText: Boolean,
    val touchMode: TouchMode,
    val trackpadSpeed: Float,
    val keyCapture: Boolean,
    val appLock: Boolean,
    val screenLock: Boolean,
    val lockAfter: Boolean,
    val codec: String,
    val av1Available: Boolean,
)

/** What the settings' controls do. */
interface SettingsActions {
    /** Turns Sharper colours on or off. */
    fun setSharperColours(on: Boolean)

    /** Turns Exact text when still on or off. */
    fun setExactText(on: Boolean)

    /** Chooses how touches on the video act. */
    fun setTouchMode(mode: TouchMode)

    /** Sets the touchscreen trackpad's speed. */
    fun setTrackpadSpeed(speed: Float)

    /** Opens Android's accessibility settings for the keyboard shortcuts service. */
    fun openKeyCaptureSettings()

    /** Turns the fingerprint-or-PIN check on Connect on or off. */
    fun setAppLock(on: Boolean)

    /** Chooses whether the desk locks when a session ends. */
    fun setLockAfter(on: Boolean)

    /** Chooses the codec, a [HardwareDecoder] codec name. */
    fun setCodec(codec: String)
}

/** The range of the trackpad speed slider. */
private val TRACKPAD_SPEEDS = 0.5f..4f

/** From this width the settings sit in two columns. */
private val TWO_COLUMN_WIDTH = 720.dp

/** The space between the two columns. */
private val COLUMN_GAP = 32.dp

/**
 * How tall every row is at least: as tall as one with a line under its
 * title, so the rows and headers of the two columns line up.
 */
private val ROW = 56.dp

/**
 * The app-wide settings under the chosen host's details, grouped into
 * Picture, Input, Security and Advanced. Each row is a title, at most a short
 * line under it, and its control on the right, flush with the details'
 * edges. From [TWO_COLUMN_WIDTH] the groups sit in two columns, Picture and
 * Security on the left, Input and Advanced on the right. The right column
 * has a row to spare, which Trackpad speed takes in trackpad mode instead
 * of making the page longer. Narrower, the groups follow one another.
 */
@Composable
fun SettingsPanel(state: SettingsUiState, actions: SettingsActions, modifier: Modifier = Modifier) {
    BoxWithConstraints(modifier.fillMaxWidth()) {
        if (maxWidth >= TWO_COLUMN_WIDTH) {
            Row {
                Column(Modifier.weight(1f)) {
                    Picture(state, actions)
                    Security(state, actions)
                }
                Spacer(Modifier.width(COLUMN_GAP))
                Column(Modifier.weight(1f)) {
                    Input(state, actions)
                    Advanced(state, actions)
                }
            }
        } else {
            Column {
                Picture(state, actions)
                Input(state, actions)
                Security(state, actions)
                Advanced(state, actions)
            }
        }
    }
}

/** A group's title, flush with the rows and a little closer to the group before than on a page of its own. */
@Composable
private fun GroupHeader(@StringRes title: Int) = SectionHeader(title, inset = 0.dp, top = 12.dp)

@Composable
private fun Picture(state: SettingsUiState, actions: SettingsActions) {
    GroupHeader(R.string.settings_picture)
    SwitchRow(
        stringResource(R.string.label_sharper_colours), stringResource(R.string.sharper_colours_explain),
        state.sharperColours, actions::setSharperColours, inset = 0.dp, minHeight = ROW,
    )
    // Exact tiles are drawn by the GPU renderer, so they need Sharper colours.
    SwitchRow(
        stringResource(R.string.label_exact_text),
        stringResource(if (state.sharperColours) R.string.exact_text_explain else R.string.exact_text_needs_sharper),
        state.exactText && state.sharperColours, actions::setExactText,
        enabled = state.sharperColours, inset = 0.dp, minHeight = ROW,
    )
}

@Composable
private fun Input(state: SettingsUiState, actions: SettingsActions) {
    GroupHeader(R.string.settings_input)
    SettingRow(stringResource(R.string.label_touch), null, inset = 0.dp, minHeight = ROW) {
        ChoiceSegments(
            listOf(TouchMode.DIRECT to R.string.touch_direct, TouchMode.TRACKPAD to R.string.touch_trackpad),
            state.touchMode, actions::setTouchMode,
        )
    }
    if (state.touchMode == TouchMode.TRACKPAD) {
        SettingRow(
            stringResource(R.string.label_trackpad_speed), stringResource(R.string.value_speed, state.trackpadSpeed),
            inset = 0.dp, minHeight = ROW,
        ) {
            Slider(
                value = state.trackpadSpeed, onValueChange = actions::setTrackpadSpeed,
                valueRange = TRACKPAD_SPEEDS, modifier = Modifier.width(220.dp),
            )
        }
    }
    SettingRow(
        stringResource(R.string.label_key_capture),
        stringResource(if (state.keyCapture) R.string.key_capture_on else R.string.key_capture_off),
        inset = 0.dp, minHeight = ROW,
    ) {
        // As tall as the segments, so the row is no taller than its neighbours;
        // Compose still takes touches within 48 dp of the button.
        OutlinedButton(onClick = actions::openKeyCaptureSettings, modifier = Modifier.height(40.dp)) {
            Text(stringResource(R.string.action_key_capture_settings))
        }
    }
}

/**
 * Without a screen lock there is nothing for the fingerprint-or-PIN prompt
 * to ask, so its switch is off and disabled with a line saying why.
 */
@Composable
private fun Security(state: SettingsUiState, actions: SettingsActions) {
    GroupHeader(R.string.settings_security)
    SwitchRow(
        stringResource(R.string.label_app_lock),
        stringResource(if (state.screenLock) R.string.app_lock_explain else R.string.app_lock_no_screen_lock),
        state.appLock && state.screenLock, actions::setAppLock, enabled = state.screenLock, inset = 0.dp, minHeight = ROW,
    )
    SwitchRow(
        stringResource(R.string.label_lock_after), stringResource(R.string.lock_after_explain),
        state.lockAfter, actions::setLockAfter, inset = 0.dp, minHeight = ROW,
    )
}

/** The codec, with a line only when AV1 cannot be chosen. */
@Composable
private fun Advanced(state: SettingsUiState, actions: SettingsActions) {
    GroupHeader(R.string.settings_advanced)
    SettingRow(
        stringResource(R.string.label_codec),
        if (state.av1Available) null else stringResource(R.string.codec_no_av1),
        inset = 0.dp, minHeight = ROW,
    ) {
        ChoiceSegments(
            listOf(HardwareDecoder.CODEC_AV1 to R.string.codec_av1, HardwareDecoder.CODEC_HEVC to R.string.codec_hevc),
            state.codec, actions::setCodec,
            enabled = { it != HardwareDecoder.CODEC_AV1 || state.av1Available },
        )
    }
}

/**
 * The settings as [vm] holds them. The keyboard shortcuts service and the
 * screen lock are read again whenever the app comes back, since the user may
 * change them in Android's settings.
 */
@Composable
fun settingsState(vm: AppViewModel): SettingsUiState {
    val context = LocalContext.current
    val sharperColours by vm.sharperColours.collectAsStateWithLifecycle()
    val exactText by vm.exactText.collectAsStateWithLifecycle()
    val touchMode by vm.touchMode.collectAsStateWithLifecycle()
    val sensitivity by vm.sensitivity.collectAsStateWithLifecycle()
    val appLock by vm.appLock.collectAsStateWithLifecycle()
    val lockAfter by vm.lockAfter.collectAsStateWithLifecycle()
    val codec by vm.codec.collectAsStateWithLifecycle()
    var keyCapture by remember { mutableStateOf(KeyCapture.enabled(context)) }
    var screenLock by remember { mutableStateOf(deviceSecure(context)) }
    LifecycleResumeEffect(Unit) {
        keyCapture = KeyCapture.enabled(context)
        screenLock = deviceSecure(context)
        onPauseOrDispose { }
    }
    return SettingsUiState(
        sharperColours = sharperColours,
        exactText = exactText,
        touchMode = touchMode,
        trackpadSpeed = sensitivity,
        keyCapture = keyCapture,
        appLock = appLock,
        screenLock = screenLock,
        lockAfter = lockAfter,
        codec = codec,
        av1Available = vm.av1Available,
    )
}

/**
 * The settings' controls, bound to [vm]. Turning the fingerprint-or-PIN
 * check off asks for it first; otherwise anyone holding the unlocked tablet
 * could switch it off and connect.
 */
@Composable
fun rememberSettingsActions(vm: AppViewModel): SettingsActions {
    val context = LocalContext.current
    val turnOffTitle = stringResource(R.string.app_lock_turn_off_title)
    return remember(vm, context, turnOffTitle) {
        object : SettingsActions {
            override fun setSharperColours(on: Boolean) = vm.setSharperColours(on)

            override fun setExactText(on: Boolean) = vm.setExactText(on)

            override fun setTouchMode(mode: TouchMode) = vm.setTouchMode(mode)

            override fun setTrackpadSpeed(speed: Float) = vm.setSensitivity(speed)

            override fun openKeyCaptureSettings() {
                // The service's own page where Android offers it; the list of services otherwise.
                val details = Intent("android.settings.ACCESSIBILITY_DETAILS_SETTINGS")
                    .putExtra(Intent.EXTRA_COMPONENT_NAME, ComponentName(context, KeyCaptureService::class.java).flattenToString())
                runCatching { context.startActivity(details) }
                    .onFailure { context.startActivity(Intent(Settings.ACTION_ACCESSIBILITY_SETTINGS)) }
            }

            override fun setAppLock(on: Boolean) {
                if (on) {
                    vm.setAppLock(true)
                } else {
                    withAppLock(context, vm.appLock.value, AppLockTrigger.TURN_OFF, turnOffTitle, vm::showMessage) { vm.setAppLock(false) }
                }
            }

            override fun setLockAfter(on: Boolean) = vm.setLockAfter(on)

            override fun setCodec(codec: String) = vm.setCodec(codec)
        }
    }
}
