package com.desklink.client.ui

import android.content.Context
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.SegmentedButton
import androidx.compose.material3.SegmentedButtonDefaults
import androidx.compose.material3.SingleChoiceSegmentedButtonRow
import androidx.compose.material3.Slider
import androidx.compose.material3.Surface
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.colorResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.desklink.client.R
import com.desklink.client.input.TouchMode
import com.desklink.client.model.AppLockTrigger
import com.desklink.client.model.HostProfile
import com.desklink.client.model.ScreenMode
import com.desklink.client.model.StreamPreset
import com.desklink.client.video.HardwareDecoder
import com.google.mlkit.vision.barcode.common.Barcode
import com.google.mlkit.vision.codescanner.GmsBarcodeScannerOptions
import com.google.mlkit.vision.codescanner.GmsBarcodeScanning
import android.content.ComponentName
import android.content.Intent
import android.provider.Settings
import androidx.lifecycle.compose.LifecycleResumeEffect
import com.desklink.client.input.KeyCapture
import com.desklink.client.input.KeyCaptureService

/** Saved hosts, pairing, stream presets and the pre-flight health check. */
@Composable
fun ConnectScreen(vm: AppViewModel) {
    val profiles by vm.profiles.collectAsStateWithLifecycle()
    val health by vm.health.collectAsStateWithLifecycle()
    val pairing by vm.pairing.collectAsStateWithLifecycle()
    val context = LocalContext.current
    var adding by remember { mutableStateOf(false) }
    var forgetTarget by remember { mutableStateOf<HostProfile?>(null) }

    val nearby by vm.nearby.collectAsStateWithLifecycle()
    LaunchedEffect(Unit) { vm.checkAll() }
    DisposableEffect(Unit) {
        vm.startDiscovery()
        onDispose { vm.stopDiscovery() }
    }

    Surface(modifier = Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.background) {
        LazyColumn(
            modifier = Modifier.fillMaxSize().safeDrawingPadding().padding(24.dp),
            verticalArrangement = Arrangement.spacedBy(16.dp),
        ) {
            item {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(stringResource(R.string.app_name), style = MaterialTheme.typography.headlineMedium, modifier = Modifier.weight(1f))
                    OutlinedButton(onClick = { scanPairingQr(context, vm) { vm.pairFromQr(it) } }, enabled = !pairing) {
                        Text(stringResource(R.string.action_scan_qr))
                    }
                    Spacer(Modifier.width(8.dp))
                    Button(onClick = { adding = true }) { Text(stringResource(R.string.action_add_host)) }
                }
            }
            item { StreamSettings(vm) }
            items(nearby, key = { "nearby-" + it.fingerprint }) { n ->
                Card(modifier = Modifier.fillMaxWidth()) {
                    Row(Modifier.padding(16.dp), verticalAlignment = Alignment.CenterVertically) {
                        Column(Modifier.weight(1f)) {
                            Text(n.name, style = MaterialTheme.typography.titleMedium)
                            Text(stringResource(R.string.nearby_subtitle, n.ip), style = MaterialTheme.typography.bodySmall)
                        }
                        // The announcement proves nothing; pairing takes the code from desklinkd pair.
                        OutlinedButton(onClick = { scanPairingQr(context, vm) { vm.pairFromQr(it, announced = n) } }, enabled = !pairing) {
                            Text(stringResource(R.string.action_pair))
                        }
                    }
                }
            }
            items(profiles, key = { it.id }) { p ->
                val lockTitle = stringResource(R.string.app_lock_connect_title, p.name)
                HostCard(
                    profile = p,
                    health = health[p.id],
                    pairing = pairing,
                    onConnect = {
                        withAppLock(context, vm.appLock.value, AppLockTrigger.CONNECT, lockTitle, vm::showMessage) { vm.connect(p) }
                    },
                    onPair = { scanPairingQr(context, vm) { vm.pairFromQr(it, chosen = p) } },
                    onCheck = { vm.checkHealth(p) },
                    onWake = { vm.wake(p) },
                    onDelete = { forgetTarget = p },
                )
            }
        }
    }

    if (adding) {
        AddHostDialog(onDismiss = { adding = false }, onAdd = { name, ip, port ->
            vm.addHost(name, ip, port)
            adding = false
        })
    }
    forgetTarget?.let { target ->
        AlertDialog(
            onDismissRequest = { forgetTarget = null },
            title = { Text(stringResource(R.string.forget_title, target.name)) },
            text = { Text(stringResource(if (target.paired) R.string.forget_paired else R.string.forget_unpaired, target.name)) },
            confirmButton = {
                TextButton(onClick = {
                    vm.forgetHost(target)
                    forgetTarget = null
                }) { Text(stringResource(R.string.action_forget), color = MaterialTheme.colorScheme.error) }
            },
            dismissButton = { TextButton(onClick = { forgetTarget = null }) { Text(stringResource(R.string.action_cancel)) } },
        )
    }
}

/**
 * Opens the QR scanner for the code `desklinkd pair` shows, the only way to
 * pair, and hands its text to [onCode]. The scanner is Google Play services
 * code; a failure to start it shows a message instead of crashing the app.
 */
private fun scanPairingQr(context: Context, vm: AppViewModel, onCode: (String) -> Unit) {
    val failed = { e: Exception -> vm.showMessage(context.getString(R.string.msg_scan_failed, e.message ?: e.toString())) }
    try {
        val options = GmsBarcodeScannerOptions.Builder().setBarcodeFormats(Barcode.FORMAT_QR_CODE).build()
        GmsBarcodeScanning.getClient(context, options).startScan()
            .addOnSuccessListener { code -> code.rawValue?.let(onCode) }
            .addOnFailureListener(failed)
    } catch (e: RuntimeException) {
        failed(e)
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun StreamSettings(vm: AppViewModel) {
    val preset by vm.preset.collectAsStateWithLifecycle()
    val codec by vm.codec.collectAsStateWithLifecycle()
    val sensitivity by vm.sensitivity.collectAsStateWithLifecycle()
    val touchMode by vm.touchMode.collectAsStateWithLifecycle()
    val playAudio by vm.playAudio.collectAsStateWithLifecycle()
    val screenMode by vm.screenMode.collectAsStateWithLifecycle()
    Card(modifier = Modifier.fillMaxWidth()) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            Text(stringResource(R.string.label_quality), style = MaterialTheme.typography.titleMedium)
            SingleChoiceSegmentedButtonRow(Modifier.fillMaxWidth()) {
                StreamPreset.entries.forEachIndexed { i, p ->
                    SegmentedButton(
                        selected = p == preset,
                        onClick = { vm.setPreset(p) },
                        shape = SegmentedButtonDefaults.itemShape(i, StreamPreset.entries.size),
                    ) { Text(stringResource(p.label)) }
                }
            }
            Text(stringResource(R.string.label_codec), style = MaterialTheme.typography.titleMedium)
            val codecs = listOf(HardwareDecoder.CODEC_AV1 to R.string.codec_av1, HardwareDecoder.CODEC_HEVC to R.string.codec_hevc)
            SingleChoiceSegmentedButtonRow(Modifier.fillMaxWidth()) {
                codecs.forEachIndexed { i, (id, label) ->
                    SegmentedButton(
                        selected = codec == id,
                        onClick = { vm.setCodec(id) },
                        enabled = id != HardwareDecoder.CODEC_AV1 || vm.av1Available,
                        shape = SegmentedButtonDefaults.itemShape(i, codecs.size),
                    ) { Text(stringResource(label)) }
                }
            }
            Text(stringResource(R.string.label_touch), style = MaterialTheme.typography.titleMedium)
            val modes = listOf(TouchMode.DIRECT to R.string.touch_direct, TouchMode.TRACKPAD to R.string.touch_trackpad)
            SingleChoiceSegmentedButtonRow(Modifier.fillMaxWidth()) {
                modes.forEachIndexed { i, (mode, label) ->
                    SegmentedButton(
                        selected = touchMode == mode,
                        onClick = { vm.setTouchMode(mode) },
                        shape = SegmentedButtonDefaults.itemShape(i, modes.size),
                    ) { Text(stringResource(label)) }
                }
            }
            Text(stringResource(R.string.label_screen), style = MaterialTheme.typography.titleMedium)
            val screens = listOf(ScreenMode.AUTO to R.string.screen_auto, ScreenMode.OWN to R.string.screen_own, ScreenMode.MIRROR to R.string.screen_mirror)
            SingleChoiceSegmentedButtonRow(Modifier.fillMaxWidth()) {
                screens.forEachIndexed { i, (mode, label) ->
                    SegmentedButton(
                        selected = screenMode == mode,
                        onClick = { vm.setScreenMode(mode) },
                        shape = SegmentedButtonDefaults.itemShape(i, screens.size),
                    ) { Text(stringResource(label)) }
                }
            }
            Text(stringResource(R.string.screen_explain), style = MaterialTheme.typography.bodySmall)
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(stringResource(R.string.label_play_audio), style = MaterialTheme.typography.titleMedium, modifier = Modifier.weight(1f))
                Switch(checked = playAudio, onCheckedChange = vm::setPlayAudio)
            }
            AppLockRow(vm)
            KeyCaptureRow()
            if (touchMode == TouchMode.TRACKPAD) {
                Text(stringResource(R.string.label_sensitivity, sensitivity), style = MaterialTheme.typography.titleMedium)
                Slider(value = sensitivity, onValueChange = vm::setSensitivity, valueRange = 0.5f..4f)
            }
        }
    }
}

@Composable
private fun HostCard(
    profile: HostProfile,
    health: HostHealth?,
    pairing: Boolean,
    onConnect: () -> Unit,
    onPair: () -> Unit,
    onCheck: () -> Unit,
    onWake: () -> Unit,
    onDelete: () -> Unit,
) {
    Card(modifier = Modifier.fillMaxWidth()) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                val (color, text) = when (health) {
                    is HostHealth.Online -> colorResource(R.color.health_online) to stringResource(R.string.health_online, health.hostname, health.version)
                    is HostHealth.Offline -> MaterialTheme.colorScheme.error to stringResource(R.string.health_offline, health.reason)
                    HostHealth.Waking -> MaterialTheme.colorScheme.tertiary to stringResource(R.string.health_waking)
                    HostHealth.Checking, null -> MaterialTheme.colorScheme.outline to stringResource(R.string.health_checking)
                }
                Box(Modifier.size(12.dp).clip(CircleShape).background(color))
                Spacer(Modifier.width(12.dp))
                Column(Modifier.weight(1f)) {
                    Text(stringResource(R.string.host_title, profile.name, profile.ip, profile.port), style = MaterialTheme.typography.titleMedium)
                    Text(text, style = MaterialTheme.typography.bodySmall)
                    Text(
                        if (profile.paired) stringResource(R.string.label_paired, profile.fingerprint.orEmpty().take(16))
                        else stringResource(R.string.label_not_paired),
                        style = MaterialTheme.typography.bodySmall,
                    )
                }
            }
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
                Button(onClick = onConnect, enabled = profile.paired) { Text(stringResource(R.string.action_connect)) }
                OutlinedButton(onClick = onPair, enabled = !pairing) {
                    Text(stringResource(if (profile.paired) R.string.action_repair else R.string.action_pair))
                }
                if (pairing) CircularProgressIndicator(Modifier.size(24.dp))
                TextButton(onClick = onCheck) { Text(stringResource(R.string.action_check)) }
                if (profile.mac != null) TextButton(onClick = onWake) { Text(stringResource(R.string.action_wake)) }
                Spacer(Modifier.weight(1f))
                TextButton(onClick = onDelete) { Text(stringResource(R.string.action_forget)) }
            }
        }
    }
}

/**
 * Whether Connect first asks for a fingerprint or the device's PIN. Without
 * a screen lock there is nothing to ask for, so the switch is off and
 * disabled with a line saying why. Turning it off asks too; otherwise anyone
 * holding the unlocked tablet could switch it off and connect.
 */
@Composable
private fun AppLockRow(vm: AppViewModel) {
    val context = LocalContext.current
    val lockOn by vm.appLock.collectAsStateWithLifecycle()
    var secure by remember { mutableStateOf(deviceSecure(context)) }
    // The user may set up or remove the screen lock in Android's settings and come back.
    LifecycleResumeEffect(Unit) {
        secure = deviceSecure(context)
        onPauseOrDispose { }
    }
    Row(verticalAlignment = Alignment.CenterVertically) {
        Column(Modifier.weight(1f)) {
            Text(stringResource(R.string.label_app_lock), style = MaterialTheme.typography.titleMedium)
            Text(stringResource(if (secure) R.string.app_lock_explain else R.string.app_lock_no_screen_lock), style = MaterialTheme.typography.bodySmall)
        }
        val turnOffTitle = stringResource(R.string.app_lock_turn_off_title)
        Switch(checked = lockOn && secure, enabled = secure, onCheckedChange = { on ->
            if (on) {
                vm.setAppLock(true)
            } else {
                withAppLock(context, lockOn, AppLockTrigger.TURN_OFF, turnOffTitle, vm::showMessage) { vm.setAppLock(false) }
            }
        })
    }
}

/** Whether Win and Alt+Tab reach the desktop, with a way to the setting that allows it. */
@Composable
private fun KeyCaptureRow() {
    val context = LocalContext.current
    var on by remember { mutableStateOf(KeyCapture.enabled(context)) }
    LifecycleResumeEffect(Unit) {
        on = KeyCapture.enabled(context)
        onPauseOrDispose { }
    }
    Row(verticalAlignment = Alignment.CenterVertically) {
        Column(Modifier.weight(1f)) {
            Text(stringResource(R.string.label_key_capture), style = MaterialTheme.typography.titleMedium)
            Text(stringResource(if (on) R.string.key_capture_on else R.string.key_capture_off), style = MaterialTheme.typography.bodySmall)
        }
        OutlinedButton(onClick = {
            // The service's own page where Android offers it; the list of services otherwise.
            val details = Intent("android.settings.ACCESSIBILITY_DETAILS_SETTINGS")
                .putExtra(Intent.EXTRA_COMPONENT_NAME, ComponentName(context, KeyCaptureService::class.java).flattenToString())
            runCatching { context.startActivity(details) }
                .onFailure { context.startActivity(Intent(Settings.ACTION_ACCESSIBILITY_SETTINGS)) }
        }) { Text(stringResource(R.string.action_key_capture_settings)) }
    }
}

@Composable
private fun AddHostDialog(onDismiss: () -> Unit, onAdd: (String, String, Int) -> Unit) {
    var name by remember { mutableStateOf("") }
    var ip by remember { mutableStateOf(HostProfile.DEFAULT_IP) }
    var port by remember { mutableStateOf(HostProfile.DEFAULT_PORT.toString()) }
    val portValue = port.toIntOrNull()?.takeIf { it in 1..65535 }
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(stringResource(R.string.action_add_host)) },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                OutlinedTextField(name, { name = it }, label = { Text(stringResource(R.string.field_name)) }, singleLine = true)
                OutlinedTextField(ip, { ip = it }, label = { Text(stringResource(R.string.field_ip)) }, singleLine = true,
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri))
                OutlinedTextField(port, { port = it }, label = { Text(stringResource(R.string.field_port)) }, singleLine = true,
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number))
            }
        },
        confirmButton = {
            TextButton(onClick = { onAdd(name, ip, portValue ?: HostProfile.DEFAULT_PORT) }, enabled = ip.isNotBlank() && portValue != null) {
                Text(stringResource(R.string.action_add))
            }
        },
        dismissButton = { TextButton(onClick = onDismiss) { Text(stringResource(R.string.action_cancel)) } },
    )
}
