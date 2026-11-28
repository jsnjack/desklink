package com.desklink.client.ui

import android.os.SystemClock
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import com.desklink.client.R
import kotlinx.coroutines.delay

/** The stages of a connection, in the order they run (docs/design.md, A session). */
enum class ConnectStage {
    /** Wake-on-LAN, only when the computer did not answer and can be woken. */
    WAKE,

    /** An address of the computer answers /health. */
    REACH,

    /** The session opens and its media path answers. */
    PICTURE,

    /** The first frame reaches the screen. */
    FRAME,
}

/** Where a stage is. */
sealed interface StageState {
    /** Not started yet. */
    data object Waiting : StageState

    /** Running since [since], in elapsedRealtime milliseconds. */
    data class Running(val since: Long) : StageState

    /** Done after [ms]; [note] adds what it found, such as the address that answered. */
    data class Done(val ms: Long, val note: String? = null) : StageState

    /** Failed: [what] failed, with names and counts, and [check] says what to look at. */
    data class Failed(val what: String, val check: String) : StageState
}

/**
 * A connection starting to the computer [hostId] named [hostName]: each
 * stage's state. The page stays until the first frame is shown, or, after
 * a failure, until the user goes back or tries again.
 */
data class ConnectProgress(val hostId: String, val hostName: String, val stages: Map<ConnectStage, StageState>) {
    /** Whether a stage failed, which stops the page on it. */
    val failed: Boolean get() = stages.values.any { it is StageState.Failed }

    /** Whether the first frame has been shown, which ends the page. */
    val done: Boolean get() = stages[ConnectStage.FRAME] is StageState.Done

    /** This with [stage] in [state]. */
    fun with(stage: ConnectStage, state: StageState) = copy(stages = stages + (stage to state))

    /** This with the running stage, or else the first waiting one, failed with [what] and [check]. */
    fun failRunning(what: String, check: String): ConnectProgress {
        val at = stages.entries.firstOrNull { it.value is StageState.Running }?.key
            ?: ConnectStage.entries.firstOrNull { stages[it] is StageState.Waiting }
            ?: ConnectStage.FRAME
        return with(at, StageState.Failed(what, check))
    }

    companion object {
        /** A connection that is reaching the computer since [now]; Wake joins only when it is needed. */
        fun starting(hostId: String, hostName: String, now: Long) = ConnectProgress(
            hostId, hostName,
            mapOf(
                ConnectStage.REACH to StageState.Running(now),
                ConnectStage.PICTURE to StageState.Waiting,
                ConnectStage.FRAME to StageState.Waiting,
            ),
        )
    }
}

/** What the connecting page's buttons do. */
interface ConnectingActions {
    /** Stops connecting, or leaves the page after a failure. */
    fun cancel()

    /** Connects again after a failure. */
    fun retry()
}

/** Keeps the elapsed time current while a connection stage is running. */
@Composable
fun ConnectingRoute(progress: ConnectProgress, actions: ConnectingActions) {
    var now by remember { mutableLongStateOf(SystemClock.elapsedRealtime()) }
    if (progress.stages.values.any { it is StageState.Running }) {
        LaunchedEffect(progress) {
            while (true) {
                now = SystemClock.elapsedRealtime()
                delay(1_000)
            }
        }
    }
    ConnectingPage(progress, actions, now)
}

/** Shows each connection stage, with the elapsed time or an actionable error. */
@Composable
internal fun ConnectingPage(progress: ConnectProgress, actions: ConnectingActions, now: Long) {
    BackHandler { actions.cancel() }
    Surface(Modifier.fillMaxSize(), color = DeskTheme.colors.page, contentColor = DeskTheme.colors.text) {
        Box(Modifier.fillMaxSize().safeDrawingPadding().padding(horizontal = 32.dp), contentAlignment = Alignment.TopCenter) {
            Column(Modifier.widthIn(max = SETTINGS_MAX_WIDTH).fillMaxWidth().padding(bottom = 24.dp)) {
                Column(Modifier.weight(1f).verticalScroll(rememberScrollState()).padding(bottom = 16.dp)) {
                    Spacer(Modifier.height(TITLE_AIR))
                    PageTitle(progress.hostName, stringResource(R.string.connecting_from_tablet))
                    Gap(24.dp)
                    val shown = ConnectStage.entries.filter { it in progress.stages }
                    shown.forEachIndexed { i, stage ->
                        if (i == 0) HorizontalDivider(color = DeskTheme.colors.line)
                        StageLine(stage, progress.stages.getValue(stage), now)
                        HorizontalDivider(color = DeskTheme.colors.line)
                    }
                    progress.stages.values.filterIsInstance<StageState.Failed>().firstOrNull()?.let {
                        Gap(20.dp)
                        ErrorBanner(it.what, it.check)
                    }
                }
                if (progress.failed) {
                    Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                        QuietButton(onClick = actions::cancel, modifier = Modifier.weight(1f)) { Text(stringResource(R.string.action_back)) }
                        PrimaryButton(onClick = actions::retry, modifier = Modifier.weight(1f)) { Text(stringResource(R.string.action_try_again)) }
                    }
                } else {
                    QuietButton(onClick = actions::cancel, modifier = Modifier.fillMaxWidth()) { Text(stringResource(R.string.action_cancel)) }
                }
            }
        }
    }
}

/** One stage: its mark, what it does or did, and its time. */
@Composable
private fun StageLine(stage: ConnectStage, state: StageState, now: Long) {
    val c = DeskTheme.colors
    Row(Modifier.fillMaxWidth().heightIn(min = 52.dp), verticalAlignment = Alignment.CenterVertically) {
        Box(Modifier.size(20.dp), contentAlignment = Alignment.Center) {
            when (state) {
                is StageState.Done -> Mark(c.online, "✓")
                is StageState.Failed -> Mark(c.unreachable, "✕")
                is StageState.Running -> BusyIndicator(Modifier.size(16.dp), strokeWidth = 2.dp, color = c.blueInk)
                StageState.Waiting -> Box(Modifier.size(16.dp).border(1.5.dp, c.line, CircleShape))
            }
        }
        Spacer(Modifier.width(14.dp))
        Text(
            stageLabel(stage, state), style = MaterialTheme.typography.titleSmall,
            color = if (state == StageState.Waiting) c.muted else c.text, modifier = Modifier.weight(1f),
        )
        val time = when (state) {
            is StageState.Done -> state.ms
            is StageState.Running -> (now - state.since).takeIf { it >= 1_000 }
            else -> null
        }
        time?.let { Text(duration(it), style = MaterialTheme.typography.labelMedium, color = c.muted) }
    }
}

/** A filled circle in [color] with a white [glyph]. */
@Composable
private fun Mark(color: Color, glyph: String) {
    Box(Modifier.size(18.dp).background(color, CircleShape), contentAlignment = Alignment.Center) {
        Text(glyph, color = Color.White, style = MaterialTheme.typography.labelSmall)
    }
}

/** "Waking it up", "Woke it up", "Reached it at 192.168.178.11": what a stage does, did or failed to do. */
@Composable
private fun stageLabel(stage: ConnectStage, state: StageState): String {
    val done = state is StageState.Done
    val failed = state is StageState.Failed
    return when (stage) {
        ConnectStage.WAKE -> stringResource(
            when {
                done -> R.string.stage_wake_done
                failed -> R.string.stage_wake_failed
                else -> R.string.stage_wake
            },
        )
        ConnectStage.REACH -> when {
            done -> (state as StageState.Done).note?.let { stringResource(R.string.stage_reach_done_at, it) } ?: stringResource(R.string.stage_reach_done)
            failed -> stringResource(R.string.stage_reach_failed)
            else -> stringResource(R.string.stage_reach)
        }
        ConnectStage.PICTURE -> stringResource(
            when {
                done -> R.string.stage_picture_done
                failed -> R.string.stage_picture_failed
                else -> R.string.stage_picture
            },
        )
        ConnectStage.FRAME -> stringResource(
            when {
                done -> R.string.stage_frame_done
                failed -> R.string.stage_frame_failed
                else -> R.string.stage_frame
            },
        )
    }
}

/** "12 ms" under a second, "1.4 s" under ten, "21 s" after. */
@Composable
private fun duration(ms: Long): String = when {
    ms < 1_000 -> stringResource(R.string.value_ms, ms.toInt())
    ms < 10_000 -> stringResource(R.string.value_seconds_tenths, ms / 1000.0)
    else -> stringResource(R.string.value_seconds, (ms / 1000).toInt())
}
