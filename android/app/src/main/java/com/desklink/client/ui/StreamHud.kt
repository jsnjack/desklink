package com.desklink.client.ui

import android.Manifest
import android.util.Log
import android.view.Display
import android.view.Surface
import android.view.SurfaceHolder
import android.view.SurfaceView
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.TransformOrigin
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.layout
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.res.colorResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.unit.Constraints
import androidx.compose.ui.unit.IntSize
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.desklink.client.R
import com.desklink.client.audio.MicPolicy
import com.desklink.client.input.InputView
import com.desklink.client.input.LinuxKeyCodes
import com.desklink.client.input.TouchMode
import com.desklink.client.model.StreamPreset
import com.desklink.client.model.StreamSize
import com.desklink.client.net.StreamSession
import kotlinx.coroutines.delay

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
    val sharperColours by session.sharperColours.collectAsStateWithLifecycle()
    val drawsPointer = (state as? StreamSession.State.Streaming)?.info?.pointer == true
    LaunchedEffect(drawsPointer, inputView) { inputView?.hideSystemPointer(drawsPointer) }
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
                Text(label, color = Color.White, modifier = Modifier.padding(16.dp))
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
            // The surface asks again when the panel's fastest mode changes.
            var videoView by remember { mutableStateOf<SurfaceView?>(null) }
            val highestHz by vm.panelHighestHz.collectAsStateWithLifecycle()
            LaunchedEffect(highestHz, videoView) {
                val v = videoView ?: return@LaunchedEffect
                v.holder.surface?.takeIf { it.isValid }?.let { hintHighestRate(it, v.display) }
            }
            AndroidView(modifier = videoLayer, factory = { ctx ->
                SurfaceView(ctx).apply {
                    val view = this
                    videoView = view
                    holder.addCallback(object : SurfaceHolder.Callback {
                        override fun surfaceCreated(holder: SurfaceHolder) {
                            hintHighestRate(holder.surface, view.display)
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
                if (sharperColours) view.holder.setFixedSize(info.width, info.height) else view.holder.setSizeFromLayout()
            })
            // The desk's pointer, drawn here when the video leaves it out,
            // zoomed with the video.
            if (info.pointer) {
                AndroidView(modifier = videoLayer, factory = { ctx -> PointerView(ctx, vm.pointer) { info.width } })
            }
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
            val panel = vm.panel.collectAsStateWithLifecycle()
            StatsOverlay(session, info, placed, surfaceSize, zoom, panel, Modifier.align(Alignment.TopStart))
        }
        ZoomChip(vm, Modifier.align(Alignment.TopCenter))
        val allSettings by vm.hostSettings.collectAsStateWithLifecycle()
        val trackpad = (allSettings[session.hostId] ?: vm.settingsOf(session.hostId)).touch == TouchMode.TRACKPAD
        TrackpadHint(trackpad, Modifier.align(Alignment.TopCenter))
        StreamHud(vm, session)
        if (keyboardShown) {
            ModifierBar(vm, Modifier.align(Alignment.BottomCenter).imePadding())
        }
    }
}

/**
 * Hints the panel's highest refresh rate for the video surface, not the
 * stream's rate: the surface's vote then keeps the panel, and with it touch
 * and S-Pen sampling, at 120 Hz for a 60 fps preset or a capped stream, and
 * lets the panel come back up once whatever held it lower lets go.
 */
private fun hintHighestRate(surface: Surface, display: Display?) {
    val hz = display?.let(PanelWatch::highestMode)?.refreshRate ?: return
    try {
        surface.setFrameRate(hz, Surface.FRAME_RATE_COMPATIBILITY_DEFAULT)
    } catch (e: IllegalArgumentException) {
        Log.d("DeskLink", "frame-rate hint of $hz Hz refused: ${e.message}")
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
        modifier.padding(8.dp).clip(DeskShape).background(colorResource(R.color.hud_scrim))
            .border(1.dp, colorResource(R.color.hud_edge), DeskShape)
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

/**
 * One line of help at the top for two seconds when the touchscreen becomes
 * a trackpad, at a session's start too; then only the pointer and the pill.
 */
@Composable
private fun TrackpadHint(trackpad: Boolean, modifier: Modifier = Modifier) {
    var shown by remember { mutableStateOf(false) }
    LaunchedEffect(trackpad) {
        shown = trackpad
        if (trackpad) {
            delay(TRACKPAD_HINT_MS)
            shown = false
        }
    }
    if (!shown) return
    val shape = RoundedCornerShape(14.dp)
    Text(
        stringResource(R.string.trackpad_hint), style = MaterialTheme.typography.labelMedium, color = colorResource(R.color.hud_text),
        modifier = modifier.padding(top = 10.dp).clip(shape).background(colorResource(R.color.hud_scrim))
            .border(1.dp, colorResource(R.color.hud_edge), shape).padding(horizontal = 14.dp, vertical = 6.dp),
    )
}

/** How long the trackpad's line of help shows. */
private const val TRACKPAD_HINT_MS = 2_000L

/**
 * The session's tools for [session], bound to [vm]: the edge pill while
 * they are closed, the rail of tools when it is tapped, and the settings
 * sheet from the rail, on the pill's side. Turning the microphone on the
 * first time asks for RECORD_AUDIO.
 */
@Composable
fun StreamHud(vm: AppViewModel, session: StreamSession) {
    val open by vm.hudOpen.collectAsStateWithLifecycle()
    val place by vm.hudPill.collectAsStateWithLifecycle()
    var sheet by rememberSaveable { mutableStateOf(false) }
    LaunchedEffect(open) { if (!open) sheet = false }
    if (!open) {
        HudTheme { SessionPill(place, onOpen = { vm.setHudOpen(true) }, onMoved = vm::setHudPill) }
        return
    }
    val state by session.state.collectAsStateWithLifecycle()
    val stats by session.stats.collectAsStateWithLifecycle()
    val keyboardShown by vm.keyboardShown.collectAsStateWithLifecycle()
    val showStats by vm.showStats.collectAsStateWithLifecycle()
    val allSettings by vm.hostSettings.collectAsStateWithLifecycle()
    val settings = allSettings[session.hostId] ?: vm.settingsOf(session.hostId)
    val askMic = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        if (MicPolicy.answered(granted) == MicPolicy.Step.TURN_ON) vm.setMic(session.hostId, true) else vm.micRefused()
    }
    val actions = remember(vm, session, askMic) {
        ViewModelHudActions(vm, session.hostId, askMic = { askMic.launch(Manifest.permission.RECORD_AUDIO) }, openSheet = { sheet = true })
    }
    val hud = hudUiState(session.hostName, state, stats, keyboardShown, showStats, settings, vm.av1Available)
    HudTheme {
        if (sheet) {
            // A tap beside the sheet closes the tools, and does not reach the desktop.
            Box(Modifier.fillMaxSize().pointerInput(Unit) { detectTapGestures { vm.setHudOpen(false) } }) {
                HudSheet(hud, actions, Modifier.align(if (place.left) Alignment.TopStart else Alignment.TopEnd))
            }
        } else {
            ToolRail(hud, actions, place)
        }
    }
}

/**
 * The HUD sheet's actions on [vm] for the session's host [hostId], whose
 * settings each change saves; [askMic] asks Android for the microphone
 * permission.
 */
private class ViewModelHudActions(
    private val vm: AppViewModel,
    private val hostId: String,
    private val askMic: () -> Unit,
    private val openSheet: () -> Unit,
) : HudActions {
    override fun close() = vm.setHudOpen(false)

    override fun openSettings() = openSheet()

    override fun toggleKeyboard() {
        vm.setKeyboardShown(!vm.keyboardShown.value)
        vm.setHudOpen(false)
    }

    override fun toggleTrackpad() {
        val trackpad = vm.settingsOf(hostId).touch == TouchMode.TRACKPAD
        vm.setTouchMode(hostId, if (trackpad) TouchMode.DIRECT else TouchMode.TRACKPAD)
        vm.setHudOpen(false)
    }

    override fun apps() = vm.pressKeys(LinuxKeyCodes.KEY_LEFTMETA, LinuxKeyCodes.KEY_A)

    override fun overview() = vm.pressKeys(LinuxKeyCodes.KEY_LEFTMETA)

    override fun sendClipboard() = vm.pushClipboard(false)

    override fun getClipboard() {
        vm.pullClipboard()
    }

    override fun toggleStats() = vm.setShowStats(!vm.showStats.value)

    override fun printScreen() = vm.pressKeys(LinuxKeyCodes.KEY_SYSRQ)

    override fun setScreen(mode: String) = vm.setScreenMode(hostId, mode)

    override fun setPreset(preset: StreamPreset) = vm.setPreset(hostId, preset)

    override fun setCodec(codec: String) = vm.setCodec(hostId, codec)

    override fun setSharperColours(on: Boolean) = vm.setSharperColours(hostId, on)

    override fun setExactText(on: Boolean) = vm.setExactText(hostId, on)

    override fun setSound(on: Boolean) = vm.setPlayAudio(hostId, on)

    override fun setTrackpadSpeed(speed: Float) = vm.setSensitivity(hostId, speed)

    override fun setScrollSpeed(speed: Float) = vm.setScrollSpeed(hostId, speed)

    override fun chooseMonitor(connector: String) = vm.chooseMonitor(connector)

    override fun changeScale(direction: Int) = vm.changeScale(hostId, direction)

    override fun setPrimary(on: Boolean) = vm.setPrimary(hostId, on)

    override fun setDeskOff(off: Boolean) = vm.setDeskOff(hostId, off)

    override fun setMic(on: Boolean) {
        when (MicPolicy.toggle(on, vm.micPermitted())) {
            MicPolicy.Step.TURN_ON -> vm.setMic(hostId, true)
            MicPolicy.Step.TURN_OFF -> vm.setMic(hostId, false)
            MicPolicy.Step.ASK_PERMISSION -> askMic()
            MicPolicy.Step.EXPLAIN_REFUSAL -> vm.micRefused()
        }
    }

    override fun setKeepAwake(minutes: Int) = vm.setKeepAwake(hostId, minutes)

    override fun setLockAfter(on: Boolean) = vm.setLockAfter(hostId, on)

    override fun disconnect() = vm.disconnect()
}

/**
 * The desktop's keys above the soft keyboard, on the page's grey: Esc, Tab,
 * Ctrl, Alt and Super on the left and the arrows on the right, as white
 * keys with a 1 dp edge. Ctrl, Alt and Super latch for the next key and
 * show in Blue Ink while latched.
 */
@Composable
private fun ModifierBar(vm: AppViewModel, modifier: Modifier = Modifier) {
    val latched by vm.latched.collectAsStateWithLifecycle()
    val left = listOf(
        R.string.key_esc to LinuxKeyCodes.KEY_ESC, R.string.key_tab to LinuxKeyCodes.KEY_TAB,
        R.string.key_ctrl to LinuxKeyCodes.KEY_LEFTCTRL, R.string.key_alt to LinuxKeyCodes.KEY_LEFTALT,
        R.string.key_super to LinuxKeyCodes.KEY_LEFTMETA,
    )
    val right = listOf(
        R.string.key_left to LinuxKeyCodes.KEY_LEFT, R.string.key_up to LinuxKeyCodes.KEY_UP,
        R.string.key_down to LinuxKeyCodes.KEY_DOWN, R.string.key_right to LinuxKeyCodes.KEY_RIGHT,
    )
    val modifiers = setOf(LinuxKeyCodes.KEY_LEFTCTRL, LinuxKeyCodes.KEY_LEFTALT, LinuxKeyCodes.KEY_LEFTMETA)
    val key = @Composable { labelRes: Int, code: Int ->
        val latch = code in modifiers
        DeskKey(stringResource(labelRes), on = latch && code in latched) { if (latch) vm.toggleModifier(code) else vm.tapKey(code) }
    }
    Row(
        modifier.fillMaxWidth().background(colorResource(R.color.bar_scrim)).padding(horizontal = 8.dp, vertical = 6.dp)
            .horizontalScroll(rememberScrollState()),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) { for ((l, c) in left) key(l, c) }
        Spacer(Modifier.width(24.dp).weight(1f, fill = false))
        Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) { for ((l, c) in right) key(l, c) }
    }
}

/** One desktop key: white with a 1 dp edge, or Blue Ink with white text while [on]. */
@Composable
private fun DeskKey(label: String, on: Boolean, onClick: () -> Unit) {
    val shape = RoundedCornerShape(10.dp)
    Box(
        Modifier.heightIn(min = 40.dp).widthIn(min = 44.dp).clip(shape)
            .background(if (on) DeskTheme.colors.blueInk else Color.White)
            .border(1.dp, if (on) DeskTheme.colors.blueInk else colorResource(R.color.hud_edge), shape)
            .clickable(role = Role.Button, onClick = onClick).padding(horizontal = 12.dp),
        contentAlignment = Alignment.Center,
    ) {
        Text(label, style = MaterialTheme.typography.labelLarge, color = if (on) Color.White else colorResource(R.color.hud_text))
    }
}
