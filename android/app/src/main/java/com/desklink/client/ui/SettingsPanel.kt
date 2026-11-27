package com.desklink.client.ui

import android.content.ComponentName
import android.content.Intent
import android.provider.Settings
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.material3.OutlinedButton
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
import com.desklink.client.model.AppLockTrigger

/**
 * The tablet's own settings, the same for every computer: [keyCapture] is
 * whether the accessibility service that passes Win and Alt+Tab is on,
 * [screenLock] whether the tablet has a screen lock for the
 * fingerprint-or-PIN prompt to fall back on. Each computer's settings are
 * on its own page ([HostSettingsScreen]).
 */
data class SettingsUiState(
    val keyCapture: Boolean,
    val appLock: Boolean,
    val screenLock: Boolean,
)

/** What the tablet's settings' controls do. */
interface SettingsActions {
    /** Opens Android's accessibility settings for the keyboard shortcuts service. */
    fun openKeyCaptureSettings()

    /** Turns the fingerprint-or-PIN check on Connect on or off. */
    fun setAppLock(on: Boolean)
}

/**
 * How tall every row is at least: as tall as one with a line under its
 * title, so the page's rows line up.
 */
private val ROW = 56.dp

/**
 * The tablet's settings on the gear's page: Security with App lock, and
 * Input with the keyboard shortcuts service. Each row is a title, at most a
 * short line under it, and its control on the right, flush with the page's
 * edges.
 */
@Composable
fun SettingsPanel(state: SettingsUiState, actions: SettingsActions, modifier: Modifier = Modifier) {
    Column(modifier.fillMaxWidth()) {
        Security(state, actions)
        Input(state, actions)
    }
}

/**
 * Without a screen lock there is nothing for the fingerprint-or-PIN prompt
 * to ask, so its switch is off and disabled with a line saying why.
 */
@Composable
private fun Security(state: SettingsUiState, actions: SettingsActions) {
    SectionHeader(R.string.settings_security, inset = 0.dp, top = 12.dp)
    SwitchRow(
        stringResource(R.string.label_app_lock),
        stringResource(if (state.screenLock) R.string.app_lock_explain else R.string.app_lock_no_screen_lock),
        state.appLock && state.screenLock, actions::setAppLock, enabled = state.screenLock, inset = 0.dp, minHeight = ROW,
    )
}

@Composable
private fun Input(state: SettingsUiState, actions: SettingsActions) {
    SectionHeader(R.string.settings_input, inset = 0.dp, top = 12.dp)
    SettingRow(
        stringResource(R.string.label_key_capture),
        stringResource(if (state.keyCapture) R.string.key_capture_on else R.string.key_capture_off),
        inset = 0.dp, minHeight = ROW,
    ) {
        // As tall as segments, so the row is no taller than a switch's;
        // Compose still takes touches within 48 dp of the button.
        OutlinedButton(onClick = actions::openKeyCaptureSettings, modifier = Modifier.height(40.dp)) {
            Text(stringResource(R.string.action_key_capture_settings))
        }
    }
}

/**
 * The tablet's settings as [vm] holds them. The keyboard shortcuts service
 * and the screen lock are read again whenever the app comes back, since the
 * user may change them in Android's settings.
 */
@Composable
fun settingsState(vm: AppViewModel): SettingsUiState {
    val context = LocalContext.current
    val appLock by vm.appLock.collectAsStateWithLifecycle()
    var keyCapture by remember { mutableStateOf(KeyCapture.enabled(context)) }
    var screenLock by remember { mutableStateOf(deviceSecure(context)) }
    LifecycleResumeEffect(Unit) {
        keyCapture = KeyCapture.enabled(context)
        screenLock = deviceSecure(context)
        onPauseOrDispose { }
    }
    return SettingsUiState(keyCapture = keyCapture, appLock = appLock, screenLock = screenLock)
}

/**
 * The tablet's settings' controls, bound to [vm]. Turning the
 * fingerprint-or-PIN check off asks for it first; otherwise anyone holding
 * the unlocked tablet could switch it off and connect.
 */
@Composable
fun rememberSettingsActions(vm: AppViewModel): SettingsActions {
    val context = LocalContext.current
    val turnOffTitle = stringResource(R.string.app_lock_turn_off_title)
    return remember(vm, context, turnOffTitle) {
        object : SettingsActions {
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
        }
    }
}
