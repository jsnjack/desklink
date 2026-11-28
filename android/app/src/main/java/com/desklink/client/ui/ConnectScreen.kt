package com.desklink.client.ui

import android.content.Context
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.desklink.client.R
import com.desklink.client.model.AppLockTrigger
import com.desklink.client.model.HostProfile
import com.desklink.client.model.NearbyHost
import com.desklink.client.model.StreamPreset
import com.google.mlkit.vision.barcode.common.Barcode
import com.google.mlkit.vision.codescanner.GmsBarcodeScannerOptions
import com.google.mlkit.vision.codescanner.GmsBarcodeScanning
import kotlinx.coroutines.launch

/**
 * Binds the main page and the settings pages to [vm]: the saved hosts with
 * their health checks and settings, the hosts found on the network while
 * the page shows, pairing, the tablet's settings, and the dialogs for adding
 * a host, forgetting one and testing its connection. The tablet's settings
 * page and a host's settings page show over the main page until Back.
 */
@Composable
fun HomeScreen(vm: AppViewModel) {
    val profiles by vm.profiles.collectAsStateWithLifecycle()
    val health by vm.health.collectAsStateWithLifecycle()
    val nearby by vm.nearby.collectAsStateWithLifecycle()
    val pairing by vm.pairing.collectAsStateWithLifecycle()
    val metered by vm.metered.collectAsStateWithLifecycle()
    val hostSettings by vm.hostSettings.collectAsStateWithLifecycle()
    val connectionTest by vm.connectionTest.collectAsStateWithLifecycle()
    val selected by vm.selectedHostId.collectAsStateWithLifecycle()
    val settings = settingsState(vm)
    val settingsActions = rememberSettingsActions(vm)
    val context = LocalContext.current
    val resources = LocalResources.current
    val scope = rememberCoroutineScope()
    val version = remember { appVersion(context) }
    var adding by remember { mutableStateOf(false) }
    var showSettings by rememberSaveable { mutableStateOf(false) }
    var showPair by rememberSaveable { mutableStateOf(false) }
    // A pairing that adds a computer closes Pair Your Computer.
    var known by rememberSaveable { mutableStateOf(profiles.size) }
    LaunchedEffect(profiles.size) {
        if (profiles.size > known) showPair = false
        known = profiles.size
    }
    var settingsHost by rememberSaveable { mutableStateOf<String?>(null) }
    var forgetTarget by remember { mutableStateOf<HostProfile?>(null) }

    LaunchedEffect(Unit) { vm.checkAll() }
    DisposableEffect(Unit) {
        vm.startDiscovery()
        onDispose { vm.stopDiscovery() }
    }

    val actions = remember(vm, context, resources) {
        object : HostsActions {
            override fun connect(host: HostProfile) {
                val title = resources.getString(R.string.app_lock_connect_title, host.name)
                withAppLock(context, vm.appLock.value, AppLockTrigger.CONNECT, title, vm::showMessage) { vm.connect(host) }
            }

            override fun wake(host: HostProfile) = vm.wake(host)

            override fun pair(host: HostProfile) = scanPairingQr(context, vm) { vm.pairFromQr(it, chosen = host) }

            override fun pairNearby(host: NearbyHost) = scanPairingQr(context, vm) { vm.pairFromQr(it, announced = host) }

            override fun scanQr() = scanPairingQr(context, vm) { vm.pairFromQr(it) }

            override fun addHost() {
                adding = true
            }

            override fun addComputer() {
                showPair = true
            }

            override fun select(host: HostProfile) = vm.selectHost(host.id)

            override fun check(host: HostProfile) = vm.checkHealth(host)

            override fun test(host: HostProfile) = vm.testConnection(host)

            override fun forget(host: HostProfile) {
                forgetTarget = host
            }

            override fun openSettings() {
                showSettings = true
            }

            override fun openHostSettings(host: HostProfile) {
                settingsHost = host.id
            }

            override fun setPreset(host: HostProfile, preset: StreamPreset) = vm.setPreset(host.id, preset)

            override fun setScreenMode(host: HostProfile, mode: String) = vm.setScreenMode(host.id, mode)

            override fun setPlayAudio(host: HostProfile, play: Boolean) = vm.setPlayAudio(host.id, play)

            override fun setExactText(host: HostProfile, on: Boolean) = vm.setExactText(host.id, on)

            override fun setDeskOff(host: HostProfile, off: Boolean) = vm.setDeskOff(host.id, off)
        }
    }
    // A host forgotten while its page is open takes the page with it.
    val pageHost = settingsHost?.let { id -> profiles.firstOrNull { it.id == id } }
    when {
        pageHost != null -> {
            BackHandler { settingsHost = null }
            HostSettingsRoute(vm, pageHost, onBack = { settingsHost = null })
        }
        showSettings -> {
            BackHandler { showSettings = false }
            SettingsScreen(
                settings, version, settingsActions,
                onShareLogs = { scope.launch { shareLog(context, vm::showMessage) } },
                onBack = { showSettings = false },
            )
        }
        profiles.isEmpty() -> PairScreen(nearby, pairing, first = true, actions, onBack = null)
        showPair -> {
            BackHandler { showPair = false }
            PairScreen(nearby, pairing, first = false, actions, onBack = { showPair = false })
        }
        else -> HostsScreen(
            HostsUiState(
                hosts = profiles.map { HostItem(it, health[it.id], hostSettings[it.id] ?: vm.settingsOf(it.id)) },
                nearby = nearby,
                pairing = pairing,
                metered = metered,
                selected = selected,
            ),
            actions,
        )
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
                DangerButton(onClick = {
                    vm.forgetHost(target)
                    forgetTarget = null
                }) { Text(stringResource(R.string.action_forget)) }
            },
            dismissButton = { QuietButton(onClick = { forgetTarget = null }) { Text(stringResource(R.string.action_cancel)) } },
        )
    }
    connectionTest?.let { ConnectionTestDialog(it, onDismiss = vm::closeConnectionTest) }
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
