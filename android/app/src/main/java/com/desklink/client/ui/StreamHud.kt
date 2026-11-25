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
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
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
import com.desklink.client.net.StreamSession
import com.desklink.client.model.OwnScale
import androidx.compose.foundation.BorderStroke
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.SegmentedButton
import androidx.compose.material3.SegmentedButtonDefaults
import androidx.compose.material3.SingleChoiceSegmentedButtonRow
import androidx.compose.material3.Switch
import androidx.compose.material3.SwitchDefaults
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.graphics.TransformOrigin
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.runtime.getValue
import androidx.compose.runtime.setValue
import com.desklink.client.model.KeepAwake
import com.desklink.client.video.VideoRendering
import com.desklink.client.model.StreamSize
import com.desklink.client.input.Zoom
import androidx.compose.runtime.State
import androidx.compose.ui.layout.layout
import androidx.compose.ui.unit.Constraints
import android.Manifest
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import com.desklink.client.audio.MicPolicy

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
    val zoom = vm.zoom.collectAsStateWithLifecycle()
    // The stream view's size, and the video surface's as surfaceChanged last
    // reported it, both in physical pixels, for the stats.
    var viewSize by remember { mutableStateOf(IntSize.Zero) }
    val surfaceSize = remember { mutableStateOf<StreamSize?>(null) }
    LaunchedEffect(keyboardShown, inputView) { inputView?.showKeyboard(keyboardShown) }
    // While the HUD is open the mouse gets Android's own pointer, to click the
    // HUD; closing it captures the pointer for the desktop again.
    LaunchedEffect(hudOpen, inputView) {
        inputView?.captureEnabled = !hudOpen
        if (!hudOpen) inputView?.reclaim()
    }
    DisposableEffect(Unit) { onDispose { onInputView(null) } }

    // The window is full screen with the system bars hidden, and the soft
    // keyboard covers it rather than resizing it (adjustNothing), so this is
    // the screen's size; the stream and the own screen take it.
    Box(
        Modifier.fillMaxSize().background(colorResource(R.color.video_background)).onSizeChanged {
            viewSize = it
            vm.setStreamViewSize(it.width, it.height)
        },
    ) {
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
        Box(Modifier.placeVideo(info.width, info.height)) {
            // The decoder's surface follows its view's transform, so the zoom
            // costs the decoder nothing; the clip keeps the video in its area.
            // The zoom is read only in the layer, so a pinch redraws without
            // recomposing the screen.
            val videoLayer = Modifier.fillMaxSize().clipToBounds().graphicsLayer {
                val z = zoom.value
                transformOrigin = TransformOrigin(0f, 0f)
                scaleX = z.scale
                scaleY = z.scale
                translationX = z.x * size.width
                translationY = z.y * size.height
            }
            AndroidView(modifier = videoLayer, factory = { ctx ->
                SurfaceView(ctx).apply {
                    holder.addCallback(object : SurfaceHolder.Callback {
                        override fun surfaceCreated(holder: SurfaceHolder) {
                            holder.surface.setFrameRate(info.fps.toFloat(), Surface.FRAME_RATE_COMPATIBILITY_FIXED_SOURCE)
                            session.attachSurface(holder.surface)
                        }

                        // The buffer the decoder renders into, which the screen
                        // shows at this size: the stats compare it with the stream's.
                        override fun surfaceChanged(holder: SurfaceHolder, format: Int, width: Int, height: Int) {
                            surfaceSize.value = StreamSize(width, height)
                        }

                        override fun surfaceDestroyed(holder: SurfaceHolder) {
                            surfaceSize.value = null
                            session.attachSurface(null)
                        }
                    })
                }
            }, update = { view ->
                // The GPU renderer draws one buffer pixel per stream pixel; the
                // compositor scales the buffer to the view, as it does the
                // decoder's own buffers on the direct path.
                if (session.sharperColours) view.holder.setFixedSize(info.width, info.height)
            })
            // Not zoomed: touches map through the zoom instead (InputMapper.zoom).
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
        if (showStats) {
            val placed = videoRect(viewSize.width, viewSize.height, info.width, info.height)
            StatsOverlay(session, info, placed, surfaceSize, zoom, Modifier.align(Alignment.TopStart))
        }
        ZoomChip(vm, Modifier.align(Alignment.TopCenter))
        StreamHud(vm, session, Modifier.align(Alignment.TopEnd))
        if (keyboardShown) {
            ModifierBar(vm, Modifier.align(Alignment.BottomCenter).imePadding())
        }
    }
}

/**
 * Lays the video out at [videoRect] in the parent's physical pixels, on whole
 * pixels, so a stream of the view's size lands pixel for pixel.
 */
private fun Modifier.placeVideo(streamWidth: Int, streamHeight: Int) = layout { measurable, constraints ->
    val r = videoRect(constraints.maxWidth, constraints.maxHeight, streamWidth, streamHeight)
    val placeable = measurable.measure(Constraints.fixed(r.width, r.height))
    layout(constraints.maxWidth, constraints.maxHeight) { placeable.place(r.x, r.y) }
}

/**
 * Link statistics in the top-left corner. Collapsed it is a small dot coloured
 * by the quality grade; a tap expands it to the grade, FPS, bitrate, round
 * trip, loss, data received, stream format, where the picture lands in
 * physical pixels (the video [placed] in the view, its [surface] buffer and
 * the pinch [zoom]) and a chart of each metric over the last minute, and
 * another tap collapses it.
 * Only the indicator itself takes touches; the rest of the corner reaches the
 * desktop.
 */
@Composable
private fun StatsOverlay(
    session: StreamSession,
    info: SessionInfo,
    placed: VideoRect,
    surface: State<StreamSize?>,
    zoom: State<Zoom>,
    modifier: Modifier = Modifier,
) {
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
        // Capture, encoded and on-screen sizes in physical pixels, and whether
        // the screen resamples the picture, which blurs text.
        val shown = shownRect(placed, surface.value, zoom.value)
        val encoded = StreamSize(info.width, info.height)
        val capture = captureSize(info)?.let { stringResource(R.string.value_size, it.width, it.height) }
            ?: stringResource(R.string.value_unknown)
        val scale = shownScale(shown, encoded)?.let { stringResource(R.string.stats_scaled, it) }
            ?: stringResource(R.string.stats_one_to_one)
        Text(
            stringResource(
                R.string.stats_geometry, capture, stringResource(R.string.value_size, encoded.width, encoded.height),
                stringResource(R.string.value_size, shown.width, shown.height), shown.x, shown.y, scale,
            ),
            color = colorResource(R.color.hud_text_secondary), style = MaterialTheme.typography.labelSmall,
        )
        val rendering by session.rendering.collectAsStateWithLifecycle()
        renderingLine(rendering)?.let {
            Text(it, color = colorResource(R.color.hud_text_secondary), style = MaterialTheme.typography.labelSmall)
        }
        val exactTiles by session.exactTiles.collectAsStateWithLifecycle()
        exactTiles?.let {
            Text(stringResource(R.string.stats_exact_tiles, it), color = colorResource(R.color.hud_text_secondary), style = MaterialTheme.typography.labelSmall)
        }
        val history by session.history.collectAsStateWithLifecycle()
        StatsCharts(history, Modifier.padding(top = 6.dp, bottom = 2.dp))
    }
}

/** How the video is drawn, for the stats overlay; null when Sharper colours is off. */
@Composable
private fun renderingLine(r: VideoRendering): String? = when (r) {
    VideoRendering.Direct -> null
    is VideoRendering.FellBack -> stringResource(R.string.stats_render_off, r.reason)
    is VideoRendering.Guided -> when (val s = r.second) {
        null -> stringResource(R.string.stats_render_starting)
        else -> stringResource(
            if (r.gpuTimed) R.string.stats_render_gpu else R.string.stats_render_cpu,
            s.meanMicros / 1000.0, s.maxMicros / 1000.0,
        )
    }
}

/**
 * While a pinch has zoomed the video, the zoom level and Reset at the top of
 * the screen; a tap on it shows the whole video again. Only the chip takes
 * touches.
 */
@Composable
private fun ZoomChip(vm: AppViewModel, modifier: Modifier = Modifier) {
    val zoom by vm.zoom.collectAsStateWithLifecycle()
    if (!zoom.zoomed) return
    val textColor = colorResource(R.color.hud_text)
    val reset = stringResource(R.string.action_reset_zoom)
    Row(
        modifier.padding(6.dp).clip(RoundedCornerShape(16.dp)).background(colorResource(R.color.hud_scrim))
            .clickable(onClickLabel = reset, onClick = vm::resetZoom).padding(start = 12.dp, end = 4.dp, top = 4.dp, bottom = 4.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        Text(stringResource(R.string.zoom_level, zoom.scale), color = textColor, style = MaterialTheme.typography.labelLarge)
        Text(
            reset, color = textColor, style = MaterialTheme.typography.labelLarge,
            modifier = Modifier.clip(RoundedCornerShape(12.dp)).background(colorResource(R.color.hud_tile))
                .padding(horizontal = 10.dp, vertical = 4.dp),
        )
    }
}

/** Bytes as KB, MB or GB with one decimal above a megabyte. */
internal fun formatBytes(b: Long): String = when {
    b >= 1L shl 30 -> String.format(Locale.ROOT, "%.2f GB", b / (1L shl 30).toDouble())
    b >= 1L shl 20 -> String.format(Locale.ROOT, "%.1f MB", b / (1L shl 20).toDouble())
    else -> String.format(Locale.ROOT, "%d KB", b shr 10)
}

/** The edge pill and, when open, the session's status, controls and screen settings. */
@Composable
fun StreamHud(vm: AppViewModel, session: StreamSession, modifier: Modifier = Modifier) {
    val open by vm.hudOpen.collectAsStateWithLifecycle()
    val stats by session.stats.collectAsStateWithLifecycle()
    val state by session.state.collectAsStateWithLifecycle()
    val keyboardShown by vm.keyboardShown.collectAsStateWithLifecycle()
    val touchMode by vm.touchMode.collectAsStateWithLifecycle()
    val showStats by vm.showStats.collectAsStateWithLifecycle()
    val hudColor = colorResource(R.color.hud_scrim)
    val textColor = colorResource(R.color.hud_text)
    val secondary = colorResource(R.color.hud_text_secondary)

    if (!open) {
        HudPill(vm, hudColor, textColor)
        return
    }
    val info = (state as? StreamSession.State.Streaming)?.info
    val (gradeRes, gradeColor) = gradeOf(stats.quality)
    Surface(color = hudColor, shape = RoundedCornerShape(24.dp), modifier = modifier.padding(16.dp).width(360.dp)) {
        Column(Modifier.padding(20.dp), verticalArrangement = Arrangement.spacedBy(16.dp)) {
            // Where and how well: the host, the link grade and the stream.
            Row(verticalAlignment = Alignment.CenterVertically) {
                Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
                    Text(session.hostName, color = textColor, style = MaterialTheme.typography.titleMedium, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Box(Modifier.size(8.dp).background(colorResource(gradeColor), CircleShape))
                        Spacer(Modifier.width(6.dp))
                        val delay = if (stats.delayMs > 0) " · " + stringResource(R.string.stats_delay, stats.delayMs) else ""
                        Text(stringResource(R.string.hud_status, stringResource(gradeRes), stats.rttMs) + delay, color = textColor, style = MaterialTheme.typography.bodySmall)
                    }
                    info?.let {
                        val stream = stringResource(R.string.hud_stream_line, stats.fps, stats.mbps, it.codec.uppercase(), it.width, it.height)
                        val level = if (it.auto) " · " + stringResource(R.string.hud_auto_level, it.level + 1, it.levels.size) else ""
                        Text(stream + level, color = secondary, style = MaterialTheme.typography.bodySmall)
                    }
                }
                IconButton(onClick = { vm.setHudOpen(false) }) {
                    Icon(painterResource(R.drawable.ic_hud_close), contentDescription = stringResource(R.string.action_close), tint = textColor)
                }
            }
            // Everything between the status and Disconnect scrolls when the panel
            // is taller than the screen, so Disconnect always stays in reach.
            Column(
                Modifier.weight(1f, fill = false).verticalScroll(rememberScrollState()),
                verticalArrangement = Arrangement.spacedBy(16.dp),
            ) {
                Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        HudTile(R.drawable.ic_hud_keyboard, stringResource(R.string.hud_keyboard), keyboardShown, Modifier.weight(1f)) {
                            vm.setKeyboardShown(!keyboardShown)
                            vm.setHudOpen(false)
                        }
                        HudTile(R.drawable.ic_hud_mouse, stringResource(R.string.hud_trackpad), touchMode == TouchMode.TRACKPAD, Modifier.weight(1f)) {
                            vm.setTouchMode(if (touchMode == TouchMode.DIRECT) TouchMode.TRACKPAD else TouchMode.DIRECT)
                            vm.setHudOpen(false)
                        }
                        HudTile(R.drawable.ic_hud_apps, stringResource(R.string.hud_apps), false, Modifier.weight(1f)) {
                            vm.pressKeys(LinuxKeyCodes.KEY_LEFTMETA, LinuxKeyCodes.KEY_A)
                        }
                        HudTile(R.drawable.ic_hud_overview, stringResource(R.string.hud_overview), false, Modifier.weight(1f)) {
                            vm.pressKeys(LinuxKeyCodes.KEY_LEFTMETA)
                        }
                    }
                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        HudTile(R.drawable.ic_hud_send, stringResource(R.string.hud_send_clipboard), false, Modifier.weight(1f)) {
                            vm.pushClipboard(false)
                        }
                        HudTile(R.drawable.ic_hud_get, stringResource(R.string.hud_get_clipboard), false, Modifier.weight(1f)) {
                            vm.pullClipboard()
                        }
                        HudTile(R.drawable.ic_hud_stats, stringResource(R.string.hud_stats), showStats, Modifier.weight(1f)) {
                            vm.setShowStats(!showStats)
                        }
                        // Android keeps the keyboard's Print Screen for its own
                        // screenshot; this tile is that key for the desktop.
                        HudTile(R.drawable.ic_hud_screenshot, stringResource(R.string.hud_screenshot), false, Modifier.weight(1f)) {
                            vm.pressKeys(LinuxKeyCodes.KEY_SYSRQ)
                        }
                    }
                }
                // Only hosts that offer the virtual microphone report mic.
                info?.mic?.let { MicRow(it, vm, textColor, secondary) }
                info?.takeIf { it.screen.isNotEmpty() }?.let { ScreenSection(it, vm, textColor, secondary) }
                info?.let { KeepAwakeSection(it, vm, textColor, secondary) }
            }
            OutlinedButton(
                onClick = vm::disconnect, modifier = Modifier.fillMaxWidth(),
                colors = ButtonDefaults.outlinedButtonColors(contentColor = colorResource(R.color.hud_danger)),
                border = BorderStroke(1.dp, colorResource(R.color.hud_danger)),
            ) {
                Icon(painterResource(R.drawable.ic_hud_disconnect), contentDescription = null, modifier = Modifier.size(18.dp))
                Spacer(Modifier.width(8.dp))
                Text(stringResource(R.string.action_disconnect))
            }
        }
    }
}

/** The link grade's label and colour. */
private fun gradeOf(q: LinkQuality): Pair<Int, Int> = when (q) {
    LinkQuality.EXCELLENT -> R.string.quality_excellent to R.color.quality_excellent
    LinkQuality.GOOD -> R.string.quality_good to R.color.quality_good
    LinkQuality.FAIR -> R.string.quality_fair to R.color.quality_fair
    LinkQuality.POOR -> R.string.quality_poor to R.color.quality_poor
}

/** One square action: an icon over a short label, highlighted while [active]. */
@Composable
private fun HudTile(icon: Int, label: String, active: Boolean, modifier: Modifier = Modifier, onClick: () -> Unit) {
    val textColor = colorResource(R.color.hud_text)
    Column(
        modifier.clip(RoundedCornerShape(16.dp))
            .background(colorResource(if (active) R.color.hud_tile_active else R.color.hud_tile))
            .clickable(onClick = onClick).padding(vertical = 12.dp, horizontal = 4.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.spacedBy(6.dp),
    ) {
        Icon(painterResource(icon), contentDescription = null, tint = textColor, modifier = Modifier.size(24.dp))
        // Every tile keeps room for two lines, so a label that wraps ("Send
        // clipboard") does not make its tile taller than the others; a
        // one-line label is centred in that room.
        Box(Modifier.fillMaxWidth(), contentAlignment = Alignment.Center) {
            Text("", style = MaterialTheme.typography.labelSmall, minLines = 2)
            Text(
                label, color = textColor, style = MaterialTheme.typography.labelSmall, textAlign = TextAlign.Center,
                maxLines = 2, overflow = TextOverflow.Ellipsis,
            )
        }
    }
}

/** Own screen or mirror, what the session shows, and the own screen's scale. */
@Composable
private fun ScreenSection(info: SessionInfo, vm: AppViewModel, textColor: Color, secondary: Color) {
    Column(verticalArrangement = Arrangement.spacedBy(10.dp)) {
        Text(stringResource(R.string.hud_screen), color = secondary, style = MaterialTheme.typography.labelMedium)
        val modes = listOf(ScreenMode.OWN to R.string.screen_own, ScreenMode.MIRROR to R.string.screen_mirror)
        SingleChoiceSegmentedButtonRow(Modifier.fillMaxWidth()) {
            modes.forEachIndexed { i, (mode, label) ->
                SegmentedButton(
                    selected = info.screen == mode,
                    onClick = { if (info.screen != mode) vm.switchScreen(info.screen) },
                    shape = SegmentedButtonDefaults.itemShape(i, modes.size),
                    colors = SegmentedButtonDefaults.colors(
                        activeContainerColor = colorResource(R.color.hud_tile_active), activeContentColor = textColor,
                        inactiveContainerColor = Color.Transparent, inactiveContentColor = textColor,
                        activeBorderColor = colorResource(R.color.hud_tile), inactiveBorderColor = colorResource(R.color.hud_tile),
                    ),
                    // No check mark, as in Keep awake: the fill shows the choice.
                    icon = {},
                ) { Text(stringResource(label), maxLines = 1) }
            }
        }
        // The monitor to mirror: a tap switches a mirroring session at once,
        // or connects the own screen again to mirror it, and is remembered.
        if (info.monitors.size > 1) {
            SingleChoiceSegmentedButtonRow(Modifier.fillMaxWidth()) {
                info.monitors.forEachIndexed { i, m ->
                    SegmentedButton(
                        selected = info.mirroring == m.connector,
                        onClick = { if (info.mirroring != m.connector) vm.chooseMonitor(m.connector) },
                        shape = SegmentedButtonDefaults.itemShape(i, info.monitors.size),
                        colors = SegmentedButtonDefaults.colors(
                            activeContainerColor = colorResource(R.color.hud_tile_active), activeContentColor = textColor,
                            inactiveContainerColor = Color.Transparent, inactiveContentColor = textColor,
                            activeBorderColor = colorResource(R.color.hud_tile), inactiveBorderColor = colorResource(R.color.hud_tile),
                        ),
                        icon = {},
                    ) { Text(m.connector, maxLines = 1, overflow = TextOverflow.Ellipsis) }
                }
            }
        }
        Text(info.display, color = secondary, style = MaterialTheme.typography.bodySmall)
        if (info.screen == ScreenMode.OWN && info.scale > 0) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Text(stringResource(R.string.label_scale), color = textColor, style = MaterialTheme.typography.titleSmall, modifier = Modifier.weight(1f))
                ScaleButton(R.string.action_scale_down, info.scale > OwnScale.MIN + 0.01) { vm.changeScale(-1) }
                // The whole scales carry a mark: GNOME stretches XWayland apps at the others.
                val percent = if (OwnScale.isSharp(info.scale)) R.string.value_percent_sharp else R.string.value_percent
                Text(
                    stringResource(percent, (info.scale * 100).roundToInt()), color = textColor,
                    style = MaterialTheme.typography.titleSmall, textAlign = TextAlign.Center, modifier = Modifier.width(64.dp),
                )
                ScaleButton(R.string.action_scale_up, info.scale < OwnScale.MAX - 0.01) { vm.changeScale(1) }
            }
            Text(stringResource(R.string.scale_sharp_hint), color = secondary, style = MaterialTheme.typography.bodySmall)
        }
        // Beside an unlocked desk the own screen starts as an extra monitor;
        // this moves GNOME's top bar and overview onto it for the session.
        // Only hosts that can do that report primary.
        info.primary?.let { primary ->
            HudSwitch(R.string.label_primary, R.string.primary_explain, primary, vm::setPrimary, textColor, secondary)
        }
        // GNOME moves every window onto the remaining monitor when the desk's
        // monitors turn off, and back when they turn on: the only way to
        // bring windows to the tablet on Wayland. Only hosts that let this
        // session switch the desk report desk_off.
        info.deskOff?.let { off ->
            HudSwitch(R.string.label_desk_off, R.string.desk_off_explain, off, vm::setDeskOff, textColor, secondary)
        }
    }
}

/**
 * The Microphone switch: on while the user wants the tablet's microphone on
 * the desk and the host reports that its virtual microphone plays it
 * ([hostOn]). Turning it on the first time asks for RECORD_AUDIO.
 */
@Composable
private fun MicRow(hostOn: Boolean, vm: AppViewModel, textColor: Color, secondary: Color) {
    val wanted by vm.mic.collectAsStateWithLifecycle()
    val ask = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        if (MicPolicy.answered(granted) == MicPolicy.Step.TURN_ON) vm.setMic(true) else vm.micRefused()
    }
    HudSwitch(
        R.string.label_mic, R.string.mic_explain, wanted && hostOn,
        { on ->
            when (MicPolicy.toggle(on, vm.micPermitted())) {
                MicPolicy.Step.TURN_ON -> vm.setMic(true)
                MicPolicy.Step.TURN_OFF -> vm.setMic(false)
                MicPolicy.Step.ASK_PERMISSION -> ask.launch(Manifest.permission.RECORD_AUDIO)
                MicPolicy.Step.EXPLAIN_REFUSAL -> vm.micRefused()
            }
        },
        textColor, secondary,
    )
}

/** A labelled switch with a line of explanation, as the screen section's settings use. */
@Composable
private fun HudSwitch(label: Int, explain: Int, checked: Boolean, onChange: (Boolean) -> Unit, textColor: Color, secondary: Color) {
    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
        Column(Modifier.weight(1f)) {
            Text(stringResource(label), color = textColor, style = MaterialTheme.typography.titleSmall)
            Text(stringResource(explain), color = secondary, style = MaterialTheme.typography.bodySmall)
        }
        Switch(
            checked = checked, onCheckedChange = onChange,
            colors = SwitchDefaults.colors(
                checkedThumbColor = textColor, checkedTrackColor = colorResource(R.color.hud_tile_active),
                uncheckedThumbColor = secondary, uncheckedTrackColor = colorResource(R.color.hud_tile),
                uncheckedBorderColor = secondary,
            ),
        )
    }
}

/**
 * How long the computer stays awake after disconnecting, so a job left
 * running (a build, an agent in a terminal) goes on; the desk still locks.
 */
@Composable
private fun KeepAwakeSection(info: SessionInfo, vm: AppViewModel, textColor: Color, secondary: Color) {
    val current = info.keepAwake
    Column(verticalArrangement = Arrangement.spacedBy(10.dp)) {
        Text(stringResource(R.string.hud_keep_awake), color = secondary, style = MaterialTheme.typography.labelMedium)
        val choices = listOf(
            KeepAwake.OFF to R.string.keep_awake_off, KeepAwake.ONE_HOUR to R.string.keep_awake_1h,
            KeepAwake.FOUR_HOURS to R.string.keep_awake_4h, KeepAwake.UNTIL_RETURN to R.string.keep_awake_return,
            KeepAwake.SUSPEND to R.string.keep_awake_sleep,
        )
        SingleChoiceSegmentedButtonRow(Modifier.fillMaxWidth()) {
            choices.forEachIndexed { i, (minutes, label) ->
                SegmentedButton(
                    selected = current == minutes,
                    onClick = { if (current != minutes) vm.setKeepAwake(minutes) },
                    shape = SegmentedButtonDefaults.itemShape(i, choices.size),
                    colors = SegmentedButtonDefaults.colors(
                        activeContainerColor = colorResource(R.color.hud_tile_active), activeContentColor = textColor,
                        inactiveContainerColor = Color.Transparent, inactiveContentColor = textColor,
                        activeBorderColor = colorResource(R.color.hud_tile), inactiveBorderColor = colorResource(R.color.hud_tile),
                    ),
                    icon = {},
                ) { Text(stringResource(label), maxLines = 1) }
            }
        }
        Text(stringResource(R.string.keep_awake_hint), color = secondary, style = MaterialTheme.typography.bodySmall)
        // The host reports the setting it uses; changing it here also saves it for later sessions.
        HudSwitch(R.string.label_lock_after, R.string.lock_after_explain, info.lockAfter, vm::setLockAfter, textColor, secondary)
    }
}

@Composable
private fun ScaleButton(label: Int, enabled: Boolean, onClick: () -> Unit) {
    Box(
        Modifier.size(40.dp).clip(CircleShape)
            .background(colorResource(R.color.hud_tile).copy(alpha = if (enabled) 1f else 0.4f))
            .clickable(enabled = enabled, onClick = onClick),
        contentAlignment = Alignment.Center,
    ) {
        Text(stringResource(label), color = colorResource(R.color.hud_text).copy(alpha = if (enabled) 1f else 0.4f), style = MaterialTheme.typography.titleMedium)
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
