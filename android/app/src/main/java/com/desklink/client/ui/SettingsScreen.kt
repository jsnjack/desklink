package com.desklink.client.ui

import android.content.ComponentName
import android.content.Intent
import android.provider.Settings
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
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
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Slider
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.painterResource
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
 * The app-wide settings: [keyCapture] is whether the accessibility service
 * that passes Win and Alt+Tab is on, [screenLock] whether the tablet has a
 * screen lock for the fingerprint-or-PIN prompt to fall back on.
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
    val version: String,
)

/** What the settings' controls do. */
interface SettingsActions {
    /** Closes the settings. */
    fun back()

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

/** The widest the settings grow; wider windows centre them. */
private val SETTINGS_MAX_WIDTH = 720.dp

/**
 * The settings, grouped into Picture, Input, Security and Advanced, with the
 * app's version at the end. Each row is a title, at most a short line under
 * it, and its control on the right.
 */
@Composable
fun SettingsScreen(state: SettingsUiState, actions: SettingsActions) {
    Surface(Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.surface) {
        Column(Modifier.fillMaxSize().safeDrawingPadding()) {
            Row(Modifier.fillMaxWidth().height(56.dp).padding(start = 4.dp, end = 16.dp), verticalAlignment = Alignment.CenterVertically) {
                IconButton(onClick = actions::back) {
                    Icon(painterResource(R.drawable.ic_arrow_back), contentDescription = stringResource(R.string.action_back))
                }
                Spacer(Modifier.width(4.dp))
                Text(stringResource(R.string.action_settings), style = MaterialTheme.typography.titleLarge)
            }
            Box(Modifier.fillMaxSize().verticalScroll(rememberScrollState()), contentAlignment = Alignment.TopCenter) {
                Column(Modifier.widthIn(max = SETTINGS_MAX_WIDTH).fillMaxWidth().padding(bottom = 16.dp)) {
                    Picture(state, actions)
                    Input(state, actions)
                    Security(state, actions)
                    SectionHeader(R.string.settings_advanced)
                    SettingRow(
                        stringResource(R.string.label_codec),
                        stringResource(if (state.av1Available) R.string.codec_explain else R.string.codec_no_av1),
                    ) {
                        ChoiceSegments(
                            listOf(HardwareDecoder.CODEC_AV1 to R.string.codec_av1, HardwareDecoder.CODEC_HEVC to R.string.codec_hevc),
                            state.codec, actions::setCodec,
                            enabled = { it != HardwareDecoder.CODEC_AV1 || state.av1Available },
                        )
                    }
                    SectionHeader(R.string.settings_about)
                    SettingRow(stringResource(R.string.app_name), stringResource(R.string.about_version, state.version)) {}
                }
            }
        }
    }
}

@Composable
private fun Picture(state: SettingsUiState, actions: SettingsActions) {
    SectionHeader(R.string.settings_picture)
    SwitchRow(
        stringResource(R.string.label_sharper_colours), stringResource(R.string.sharper_colours_explain),
        state.sharperColours, actions::setSharperColours,
    )
    // Exact tiles are drawn by the GPU renderer, so they need Sharper colours.
    SwitchRow(
        stringResource(R.string.label_exact_text),
        stringResource(if (state.sharperColours) R.string.exact_text_explain else R.string.exact_text_needs_sharper),
        state.exactText && state.sharperColours, actions::setExactText, enabled = state.sharperColours,
    )
}

@Composable
private fun Input(state: SettingsUiState, actions: SettingsActions) {
    SectionHeader(R.string.settings_input)
    SettingRow(stringResource(R.string.label_touch), null) {
        ChoiceSegments(
            listOf(TouchMode.DIRECT to R.string.touch_direct, TouchMode.TRACKPAD to R.string.touch_trackpad),
            state.touchMode, actions::setTouchMode,
        )
    }
    if (state.touchMode == TouchMode.TRACKPAD) {
        SettingRow(stringResource(R.string.label_trackpad_speed), stringResource(R.string.value_speed, state.trackpadSpeed)) {
            Slider(
                value = state.trackpadSpeed, onValueChange = actions::setTrackpadSpeed,
                valueRange = TRACKPAD_SPEEDS, modifier = Modifier.width(280.dp),
            )
        }
    }
    SettingRow(
        stringResource(R.string.label_key_capture),
        stringResource(if (state.keyCapture) R.string.key_capture_on else R.string.key_capture_off),
    ) {
        OutlinedButton(onClick = actions::openKeyCaptureSettings) { Text(stringResource(R.string.action_key_capture_settings)) }
    }
}

/**
 * Without a screen lock there is nothing for the fingerprint-or-PIN prompt
 * to ask, so its switch is off and disabled with a line saying why.
 */
@Composable
private fun Security(state: SettingsUiState, actions: SettingsActions) {
    SectionHeader(R.string.settings_security)
    SwitchRow(
        stringResource(R.string.label_app_lock),
        stringResource(if (state.screenLock) R.string.app_lock_explain else R.string.app_lock_no_screen_lock),
        state.appLock && state.screenLock, actions::setAppLock, enabled = state.screenLock,
    )
    SwitchRow(
        stringResource(R.string.label_lock_after), stringResource(R.string.lock_after_explain),
        state.lockAfter, actions::setLockAfter,
    )
}

/**
 * Binds the settings to [vm]. The keyboard shortcuts service and the screen
 * lock are read again whenever the app comes back, since the user may change
 * them in Android's settings. Turning the fingerprint-or-PIN check off asks
 * for it first; otherwise anyone holding the unlocked tablet could switch it
 * off and connect.
 */
@Composable
fun AppSettings(vm: AppViewModel, onBack: () -> Unit) {
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
    val version = remember { appVersion(context) }
    val turnOffTitle = stringResource(R.string.app_lock_turn_off_title)
    val actions = remember(vm, context, turnOffTitle) {
        object : SettingsActions {
            override fun back() = onBack()

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
    SettingsScreen(
        SettingsUiState(
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
            version = version,
        ),
        actions,
    )
}
