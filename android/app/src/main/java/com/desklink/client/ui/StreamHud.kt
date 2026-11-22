package com.desklink.client.ui

import android.view.Surface
import android.view.SurfaceHolder
import android.view.SurfaceView
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Surface
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
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.desklink.client.R
import com.desklink.client.input.InputView
import com.desklink.client.input.LinuxKeyCodes
import com.desklink.client.model.AppEntry
import com.desklink.client.net.StreamSession

/** Full-screen video, the input overlay and the floating HUD. */
@Composable
fun StreamScreen(vm: AppViewModel, session: StreamSession, onInputView: (InputView?) -> Unit) {
    val state by session.state.collectAsStateWithLifecycle()
    val keyboardShown by vm.keyboardShown.collectAsStateWithLifecycle()
    var inputView by remember { mutableStateOf<InputView?>(null) }

    LaunchedEffect(state) {
        when (val s = state) {
            is StreamSession.State.Failed -> vm.dismissSession(s.message)
            StreamSession.State.Closed -> vm.dismissSession(null)
            else -> Unit
        }
    }
    val hudOpen by vm.hudOpen.collectAsStateWithLifecycle()
    LaunchedEffect(keyboardShown, inputView) { inputView?.showKeyboard(keyboardShown) }
    LaunchedEffect(hudOpen, inputView) { if (!hudOpen) inputView?.reclaim() }
    DisposableEffect(Unit) { onDispose { onInputView(null) } }

    Box(Modifier.fillMaxSize().background(Color.Black)) {
        val streaming = state as? StreamSession.State.Streaming
        if (streaming == null) {
            Column(Modifier.align(Alignment.Center), horizontalAlignment = Alignment.CenterHorizontally) {
                CircularProgressIndicator()
                Text(stringResource(R.string.label_connecting), color = Color.White, modifier = Modifier.padding(16.dp))
                OutlinedButton(onClick = vm::disconnect) { Text(stringResource(R.string.action_cancel)) }
            }
            return@Box
        }
        val info = streaming.info
        Box(Modifier.align(Alignment.Center).aspectRatio(info.width.toFloat() / info.height)) {
            AndroidView(modifier = Modifier.fillMaxSize(), factory = { ctx ->
                SurfaceView(ctx).apply {
                    holder.addCallback(object : SurfaceHolder.Callback {
                        override fun surfaceCreated(holder: SurfaceHolder) {
                            holder.surface.setFrameRate(info.fps.toFloat(), Surface.FRAME_RATE_COMPATIBILITY_FIXED_SOURCE)
                            session.attachSurface(holder.surface)
                        }

                        override fun surfaceChanged(holder: SurfaceHolder, format: Int, width: Int, height: Int) = Unit

                        override fun surfaceDestroyed(holder: SurfaceHolder) = session.attachSurface(null)
                    })
                }
            })
            AndroidView(modifier = Modifier.fillMaxSize(), factory = { ctx ->
                InputView(ctx, vm.mapper).also {
                    it.captureEnabled = true
                    it.requestFocus()
                    inputView = it
                    onInputView(it)
                }
            })
        }
        StreamHud(vm, session, Modifier.align(Alignment.TopEnd))
        if (keyboardShown) {
            ModifierBar(vm, Modifier.align(Alignment.BottomCenter).imePadding())
        }
    }
}

/** The edge pill and, when open, the app drawer, clipboard, keyboard and link controls. */
@Composable
fun StreamHud(vm: AppViewModel, session: StreamSession, modifier: Modifier = Modifier) {
    val open by vm.hudOpen.collectAsStateWithLifecycle()
    val stats by session.stats.collectAsStateWithLifecycle()
    val state by session.state.collectAsStateWithLifecycle()
    val apps by session.apps.collectAsStateWithLifecycle()
    val keyboardShown by vm.keyboardShown.collectAsStateWithLifecycle()
    val hudColor = Color(0xCC1E1E1E)

    if (!open) {
        Surface(
            color = hudColor,
            shape = RoundedCornerShape(bottomStart = 16.dp, topStart = 16.dp),
            modifier = modifier.padding(top = 48.dp).clickable { vm.setHudOpen(true) },
        ) {
            Text("☰", color = Color.White, modifier = Modifier.padding(horizontal = 10.dp, vertical = 14.dp))
        }
        return
    }
    val telemetry = stringResource(R.string.hud_telemetry, stats.rttMs, stats.fps, stats.mbps)
    val display = (state as? StreamSession.State.Streaming)?.info?.let { "${it.codec.uppercase()} ${it.width}x${it.height} QP${it.qp} · ${it.display}" }
    Surface(color = hudColor, shape = RoundedCornerShape(16.dp), modifier = modifier.padding(16.dp).width(380.dp)) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Column(Modifier.weight(1f)) {
                    Text(telemetry, color = Color.White, style = MaterialTheme.typography.titleSmall)
                    display?.let { Text(it, color = Color.LightGray, style = MaterialTheme.typography.bodySmall) }
                }
                TextButton(onClick = { vm.setHudOpen(false) }) { Text(stringResource(R.string.action_close)) }
            }
            Row(Modifier.horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                FilledTonalButton(onClick = { vm.setKeyboardShown(!keyboardShown); vm.setHudOpen(false) }) {
                    Text(stringResource(if (keyboardShown) R.string.action_hide_keyboard else R.string.action_keyboard))
                }
                FilledTonalButton(onClick = { vm.pushClipboard(false) }) { Text(stringResource(R.string.action_push_clipboard)) }
                FilledTonalButton(onClick = { vm.pullClipboard() }) { Text(stringResource(R.string.action_pull_clipboard)) }
            }
            AppDrawer(apps, onLaunch = vm::launch)
            Button(onClick = vm::disconnect, modifier = Modifier.fillMaxWidth()) { Text(stringResource(R.string.action_disconnect)) }
        }
    }
}

/** Pinned apps first (Bunk Terminal), then a searchable list of the host's .desktop apps. */
@Composable
private fun AppDrawer(apps: List<AppEntry>, onLaunch: (AppEntry) -> Unit) {
    var query by remember { mutableStateOf("") }
    val pinned = apps.firstOrNull()
    pinned?.let {
        Button(onClick = { onLaunch(it) }, modifier = Modifier.fillMaxWidth()) { Text(it.name) }
    }
    OutlinedTextField(
        value = query, onValueChange = { query = it }, singleLine = true,
        label = { Text(stringResource(R.string.field_search_apps)) }, modifier = Modifier.fillMaxWidth(),
    )
    val filtered = apps.drop(1).filter { query.isBlank() || it.name.contains(query, ignoreCase = true) }
    LazyColumn(Modifier.fillMaxWidth().heightIn(max = 280.dp)) {
        items(filtered, key = { it.id }) { app ->
            Text(
                app.name, color = Color.White,
                modifier = Modifier.fillMaxWidth().clickable { onLaunch(app) }.padding(vertical = 10.dp, horizontal = 4.dp),
            )
        }
    }
}

/** A single row of terminal keys above the soft keyboard; Ctrl, Alt and Super latch for the next key. */
@Composable
private fun ModifierBar(vm: AppViewModel, modifier: Modifier = Modifier) {
    val latched by vm.latched.collectAsStateWithLifecycle()
    val keys = listOf(
        "Esc" to LinuxKeyCodes.KEY_ESC, "Tab" to LinuxKeyCodes.KEY_TAB,
        "Ctrl" to LinuxKeyCodes.KEY_LEFTCTRL, "Alt" to LinuxKeyCodes.KEY_LEFTALT, "Super" to LinuxKeyCodes.KEY_LEFTMETA,
        "↑" to LinuxKeyCodes.KEY_UP, "↓" to LinuxKeyCodes.KEY_DOWN, "←" to LinuxKeyCodes.KEY_LEFT, "→" to LinuxKeyCodes.KEY_RIGHT,
    )
    val modifiers = setOf(LinuxKeyCodes.KEY_LEFTCTRL, LinuxKeyCodes.KEY_LEFTALT, LinuxKeyCodes.KEY_LEFTMETA)
    Row(
        modifier.fillMaxWidth().background(Color(0xE61E1E1E)).padding(6.dp).horizontalScroll(rememberScrollState()),
        horizontalArrangement = Arrangement.spacedBy(6.dp, Alignment.CenterHorizontally),
    ) {
        for ((label, code) in keys) {
            if (code in modifiers) {
                val on = code in latched
                if (on) {
                    Button(onClick = { vm.toggleModifier(code) }) { Text(label) }
                } else {
                    OutlinedButton(onClick = { vm.toggleModifier(code) }) { Text(label, color = Color.White) }
                }
            } else {
                OutlinedButton(onClick = { vm.tapKey(code) }) { Text(label, color = Color.White) }
            }
        }
    }
}
