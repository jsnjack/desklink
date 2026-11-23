package com.desklink.client.ui

import android.view.Surface
import android.view.SurfaceHolder
import android.view.SurfaceView
import androidx.compose.foundation.background
import kotlin.math.roundToInt
import androidx.compose.ui.unit.IntSize
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.gestures.detectDragGestures
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.BoxWithConstraints
import java.util.Locale
import com.desklink.client.net.LinkQuality
import com.desklink.client.model.ScreenMode
import com.desklink.client.model.SessionInfo
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.FlowRow
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
import androidx.compose.ui.res.colorResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.desklink.client.R
import com.desklink.client.input.InputView
import com.desklink.client.input.TouchMode
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

    Box(Modifier.fillMaxSize().background(colorResource(R.color.video_background))) {
        val streaming = state as? StreamSession.State.Streaming
        if (streaming == null) {
            val reconnecting = state as? StreamSession.State.Reconnecting
            val label = reconnecting?.let { stringResource(R.string.label_reconnecting, it.attempt, it.of) }
                ?: stringResource(R.string.label_connecting)
            Column(Modifier.align(Alignment.Center), horizontalAlignment = Alignment.CenterHorizontally) {
                CircularProgressIndicator()
                Text(label, color = colorResource(R.color.hud_text), modifier = Modifier.padding(16.dp))
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
        val showStats by vm.showStats.collectAsStateWithLifecycle()
        if (showStats) StatsOverlay(session, streaming.info, Modifier.align(Alignment.TopStart))
        StreamHud(vm, session, Modifier.align(Alignment.TopEnd))
        if (keyboardShown) {
            ModifierBar(vm, Modifier.align(Alignment.BottomCenter).imePadding())
        }
    }
}

/**
 * Link statistics in the top-left corner. Collapsed it is a small dot coloured
 * by the quality grade; a tap expands it to the grade, FPS, bitrate, round
 * trip, loss, data received and stream format, and another tap collapses it.
 * Only the indicator itself takes touches; the rest of the corner reaches the
 * desktop.
 */
@Composable
private fun StatsOverlay(session: StreamSession, info: SessionInfo, modifier: Modifier = Modifier) {
    val stats by session.stats.collectAsStateWithLifecycle()
    var expanded by remember { mutableStateOf(false) }
    val (gradeRes, gradeColor) = when (stats.quality) {
        LinkQuality.EXCELLENT -> R.string.quality_excellent to R.color.quality_excellent
        LinkQuality.GOOD -> R.string.quality_good to R.color.quality_good
        LinkQuality.FAIR -> R.string.quality_fair to R.color.quality_fair
        LinkQuality.POOR -> R.string.quality_poor to R.color.quality_poor
    }
    val grade = stringResource(gradeRes)
    if (!expanded) {
        Box(
            modifier.padding(6.dp).size(20.dp).background(colorResource(R.color.hud_scrim), CircleShape)
                .clickable(onClickLabel = grade) { expanded = true },
            contentAlignment = Alignment.Center,
        ) {
            Box(Modifier.size(8.dp).background(colorResource(gradeColor), CircleShape))
        }
        return
    }
    val stream = stringResource(R.string.hud_stream, info.codec.uppercase(), info.width, info.height, info.fps, info.qp, info.display)
    val format = if (info.auto) stringResource(R.string.hud_stream_auto, info.level + 1, info.levels.size, stream) else stream
    Column(
        modifier.padding(6.dp).background(colorResource(R.color.hud_scrim), RoundedCornerShape(8.dp))
            .clickable { expanded = false }.padding(horizontal = 8.dp, vertical = 4.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Box(Modifier.size(8.dp).background(colorResource(gradeColor), CircleShape))
            Spacer(Modifier.width(6.dp))
            val delay = if (stats.delayMs > 0) " · " + stringResource(R.string.stats_delay, stats.delayMs) else ""
            Text(
                stringResource(R.string.stats_link, grade, stats.fps, stats.mbps, stats.rttMs, stats.lossPct) + delay,
                color = colorResource(R.color.hud_text), style = MaterialTheme.typography.labelSmall,
            )
        }
        Text(
            stringResource(R.string.stats_received, formatBytes(stats.receivedBytes)) + " · " + format,
            color = colorResource(R.color.hud_text_secondary), style = MaterialTheme.typography.labelSmall,
        )
    }
}

/** Bytes as KB, MB or GB with one decimal above a megabyte. */
internal fun formatBytes(b: Long): String = when {
    b >= 1L shl 30 -> String.format(Locale.ROOT, "%.2f GB", b / (1L shl 30).toDouble())
    b >= 1L shl 20 -> String.format(Locale.ROOT, "%.1f MB", b / (1L shl 20).toDouble())
    else -> String.format(Locale.ROOT, "%d KB", b shr 10)
}

/** The edge pill and, when open, the app drawer, clipboard, keyboard and link controls. */
@Composable
fun StreamHud(vm: AppViewModel, session: StreamSession, modifier: Modifier = Modifier) {
    val open by vm.hudOpen.collectAsStateWithLifecycle()
    val stats by session.stats.collectAsStateWithLifecycle()
    val state by session.state.collectAsStateWithLifecycle()
    val apps by session.apps.collectAsStateWithLifecycle()
    val keyboardShown by vm.keyboardShown.collectAsStateWithLifecycle()
    val hudColor = colorResource(R.color.hud_scrim)
    val textColor = colorResource(R.color.hud_text)

    if (!open) {
        HudPill(vm, hudColor, textColor)
        return
    }
    val telemetry = stringResource(R.string.hud_telemetry, stats.rttMs, stats.fps, stats.mbps) +
        if (stats.delayMs > 0) " | " + stringResource(R.string.stats_delay, stats.delayMs) else ""
    val display = (state as? StreamSession.State.Streaming)?.info?.let {
        val stream = stringResource(R.string.hud_stream, it.codec.uppercase(), it.width, it.height, it.fps, it.qp, it.display)
        if (it.auto) stringResource(R.string.hud_stream_auto, it.level + 1, it.levels.size, stream) else stream
    }
    Surface(color = hudColor, shape = RoundedCornerShape(16.dp), modifier = modifier.padding(16.dp).width(380.dp)) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Column(Modifier.weight(1f)) {
                    Text(telemetry, color = textColor, style = MaterialTheme.typography.titleSmall)
                    display?.let { Text(it, color = colorResource(R.color.hud_text_secondary), style = MaterialTheme.typography.bodySmall) }
                }
                TextButton(onClick = { vm.setHudOpen(false) }) { Text(stringResource(R.string.action_close)) }
            }
            // The buttons wrap onto more lines: a row that scrolls sideways
            // cut the third one at the panel's edge and hid the rest.
            FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
                FilledTonalButton(onClick = { vm.setKeyboardShown(!keyboardShown); vm.setHudOpen(false) }) {
                    Text(stringResource(if (keyboardShown) R.string.action_hide_keyboard else R.string.action_keyboard))
                }
                val touchMode by vm.touchMode.collectAsStateWithLifecycle()
                FilledTonalButton(onClick = {
                    vm.setTouchMode(if (touchMode == TouchMode.DIRECT) TouchMode.TRACKPAD else TouchMode.DIRECT)
                    vm.setHudOpen(false)
                }) {
                    Text(stringResource(if (touchMode == TouchMode.DIRECT) R.string.action_use_trackpad else R.string.action_use_tap))
                }
                FilledTonalButton(onClick = { vm.pushClipboard(false) }) { Text(stringResource(R.string.action_push_clipboard)) }
                FilledTonalButton(onClick = { vm.pullClipboard() }) { Text(stringResource(R.string.action_pull_clipboard)) }
                (state as? StreamSession.State.Streaming)?.info?.screen?.takeIf { it.isNotEmpty() }?.let { screen ->
                    FilledTonalButton(onClick = { vm.switchScreen(screen) }) {
                        Text(stringResource(if (screen == ScreenMode.OWN) R.string.action_mirror_screen else R.string.action_own_screen))
                    }
                }
                val showStats by vm.showStats.collectAsStateWithLifecycle()
                FilledTonalButton(onClick = { vm.setShowStats(!showStats) }) {
                    Text(stringResource(if (showStats) R.string.action_hide_stats else R.string.action_show_stats))
                }
            }
            AppDrawer(apps, textColor, onLaunch = vm::launch)
            Button(onClick = vm::disconnect, modifier = Modifier.fillMaxWidth()) { Text(stringResource(R.string.action_disconnect)) }
        }
    }
}

/**
 * The pill that opens the HUD. A tap opens it; a drag moves it, and when let
 * go it sticks to the nearer side edge at the height it was dropped, which is
 * remembered. Only the pill takes touches; the rest reaches the desktop.
 */
@Composable
private fun HudPill(vm: AppViewModel, color: Color, textColor: Color) {
    val saved by vm.hudPill.collectAsStateWithLifecycle()
    BoxWithConstraints(Modifier.fillMaxSize()) {
        val maxW = constraints.maxWidth
        val maxH = constraints.maxHeight
        var size by remember { mutableStateOf(IntSize.Zero) }
        // While dragging, the pill follows the finger freely; dragX and dragY
        // are its offset from the saved place.
        var dragX by remember { mutableFloatStateOf(0f) }
        var dragY by remember { mutableFloatStateOf(0f) }
        val baseX = if (saved.left) 0 else maxW - size.width
        val baseY = saved.y * (maxH - size.height).coerceAtLeast(0)
        val shape = if (saved.left) RoundedCornerShape(topEnd = 16.dp, bottomEnd = 16.dp)
        else RoundedCornerShape(topStart = 16.dp, bottomStart = 16.dp)
        Box(
            Modifier
                .offset {
                    IntOffset(
                        (baseX + dragX).roundToInt().coerceIn(0, (maxW - size.width).coerceAtLeast(0)),
                        (baseY + dragY).roundToInt().coerceIn(0, (maxH - size.height).coerceAtLeast(0)),
                    )
                }
                .onSizeChanged { size = it }
                .background(color, shape)
                .pointerInput(Unit) { detectTapGestures { vm.setHudOpen(true) } }
                .pointerInput(maxW, maxH, saved) {
                    detectDragGestures(
                        onDrag = { change, delta ->
                            change.consume()
                            dragX += delta.x
                            dragY += delta.y
                        },
                        onDragEnd = {
                            val centreX = baseX + dragX + size.width / 2f
                            val travel = (maxH - size.height).coerceAtLeast(1)
                            val y = ((baseY + dragY) / travel).coerceIn(0f, 1f)
                            dragX = 0f
                            dragY = 0f
                            vm.setHudPill(HudPill(left = centreX < maxW / 2f, y = y))
                        },
                        onDragCancel = {
                            dragX = 0f
                            dragY = 0f
                        },
                    )
                },
        ) {
            Text(stringResource(R.string.hud_open), color = textColor, modifier = Modifier.padding(horizontal = 10.dp, vertical = 14.dp))
        }
    }
}

/** Pinned apps first (Bunk Terminal), then a searchable list of the host's .desktop apps. */
@Composable
private fun AppDrawer(apps: List<AppEntry>, textColor: Color, onLaunch: (AppEntry) -> Unit) {
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
                app.name, color = textColor,
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
        R.string.key_esc to LinuxKeyCodes.KEY_ESC, R.string.key_tab to LinuxKeyCodes.KEY_TAB,
        R.string.key_ctrl to LinuxKeyCodes.KEY_LEFTCTRL, R.string.key_alt to LinuxKeyCodes.KEY_LEFTALT,
        R.string.key_super to LinuxKeyCodes.KEY_LEFTMETA, R.string.key_up to LinuxKeyCodes.KEY_UP,
        R.string.key_down to LinuxKeyCodes.KEY_DOWN, R.string.key_left to LinuxKeyCodes.KEY_LEFT,
        R.string.key_right to LinuxKeyCodes.KEY_RIGHT,
    )
    val textColor = colorResource(R.color.hud_text)
    val modifiers = setOf(LinuxKeyCodes.KEY_LEFTCTRL, LinuxKeyCodes.KEY_LEFTALT, LinuxKeyCodes.KEY_LEFTMETA)
    Row(
        modifier.fillMaxWidth().background(colorResource(R.color.bar_scrim)).padding(6.dp).horizontalScroll(rememberScrollState()),
        horizontalArrangement = Arrangement.spacedBy(6.dp, Alignment.CenterHorizontally),
    ) {
        for ((labelRes, code) in keys) {
            val label = stringResource(labelRes)
            if (code in modifiers) {
                val on = code in latched
                if (on) {
                    Button(onClick = { vm.toggleModifier(code) }) { Text(label) }
                } else {
                    OutlinedButton(onClick = { vm.toggleModifier(code) }) { Text(label, color = textColor) }
                }
            } else {
                OutlinedButton(onClick = { vm.tapKey(code) }) { Text(label, color = textColor) }
            }
        }
    }
}
