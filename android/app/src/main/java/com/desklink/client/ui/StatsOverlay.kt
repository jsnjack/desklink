package com.desklink.client.ui

import android.os.Build
import androidx.annotation.StringRes
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.State
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.colorResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.desklink.client.R
import com.desklink.client.input.Zoom
import com.desklink.client.model.SessionInfo
import com.desklink.client.model.StreamSize
import com.desklink.client.net.ExactTileStats
import com.desklink.client.net.LinkQuality
import com.desklink.client.net.LinkStats
import com.desklink.client.net.StatsHistory
import com.desklink.client.net.StreamSession
import com.desklink.client.video.DecoderRating
import com.desklink.client.video.DecoderReport
import com.desklink.client.video.VideoRendering
import java.util.Locale
import kotlin.math.roundToInt

/**
 * Everything the stats panel shows: the link's [stats] and their [history],
 * the session, the rectangle the decoder's output fills on the screen
 * ([shown]), the frame-rate cap the session asks for (0 for none), the
 * [panel]'s rate, the [decoder]'s report, how the video is drawn, the exact
 * tiles drawn and the frames that hid them (null without them), and the
 * device's chip and media performance class (0 when it declares none).
 */
data class StatsDetails(
    val stats: LinkStats,
    val info: SessionInfo,
    val shown: VideoRect,
    val maxFps: Int,
    val panel: PanelState?,
    val decoder: DecoderReport?,
    val rendering: VideoRendering,
    val exactTiles: ExactTileStats?,
    val history: StatsHistory,
    val soc: String,
    val mediaClass: Int,
)

/**
 * The stats overlay: a pill with the link grade's colour and the frame
 * rate; a tap opens the details panel, and a tap on the panel closes it.
 * The video [placed] in the view, its [surface] buffer and the pinch [zoom]
 * give where the picture lands. Only the pill and the panel take touches;
 * the rest of the screen reaches the desktop.
 */
@Composable
internal fun StatsOverlay(
    session: StreamSession,
    info: SessionInfo,
    placed: VideoRect,
    surface: State<StreamSize?>,
    zoom: State<Zoom>,
    panel: State<PanelState?>,
    modifier: Modifier = Modifier,
) {
    val stats by session.stats.collectAsStateWithLifecycle()
    var expanded by remember { mutableStateOf(false) }
    if (!expanded) {
        StatsPill(stats.fps, stats.rttMs, stats.quality, onClick = { expanded = true }, modifier)
        return
    }
    val maxFps by session.maxFps.collectAsStateWithLifecycle()
    val decoder by session.decoderReport.collectAsStateWithLifecycle()
    val rendering by session.rendering.collectAsStateWithLifecycle()
    val exactTiles by session.exactTiles.collectAsStateWithLifecycle()
    val history by session.history.collectAsStateWithLifecycle()
    StatsPanel(
        StatsDetails(
            stats, info, shownRect(placed, surface.value, zoom.value), maxFps, panel.value, decoder, rendering, exactTiles,
            history, Build.SOC_MODEL, Build.VERSION.MEDIA_PERFORMANCE_CLASS,
        ),
        onClose = { expanded = false },
        modifier,
    )
}

/**
 * A dot in the link grade's colour, the frame rate and the round trip,
 * such as "● 120 · 9 ms", on a frosted pill with a 1 dp edge. It is drawn
 * without a Material Surface, which would swallow touches meant for the
 * desktop around it.
 */
@Composable
fun StatsPill(fps: Int, rttMs: Int, quality: LinkQuality, onClick: () -> Unit, modifier: Modifier = Modifier) {
    val (gradeRes, gradeColor) = gradeOf(quality)
    val description = stringResource(R.string.stats_pill_description, stringResource(gradeRes), fps)
    Row(
        modifier.padding(8.dp).clip(PILL_SHAPE).background(colorResource(R.color.hud_scrim))
            .border(1.dp, colorResource(R.color.hud_edge), PILL_SHAPE)
            .clickable(onClickLabel = stringResource(R.string.stats_show_details), onClick = onClick)
            .semantics { contentDescription = description }
            .padding(horizontal = 10.dp, vertical = 5.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Box(Modifier.size(8.dp).background(colorResource(gradeColor), CircleShape))
        Spacer(Modifier.width(5.dp))
        // Tabular figures and room for three digits, so the pill keeps its width as the numbers change.
        Text(
            stringResource(R.string.stats_pill, fps, rttMs), color = colorResource(R.color.hud_text), style = MaterialTheme.typography.labelMedium,
            textAlign = TextAlign.End, modifier = Modifier.widthIn(min = 64.dp),
        )
    }
}

/**
 * Today's numbers as labelled values in four groups, Link, Picture, Decoder
 * and Display, and the last minute of the link as charts. The whole panel
 * takes touches, so none reaches the desktop beneath it; a tap closes it.
 */
@Composable
fun StatsPanel(details: StatsDetails, onClose: () -> Unit, modifier: Modifier = Modifier) {
    val (gradeRes, gradeColor) = gradeOf(details.stats.quality)
    val close = stringResource(R.string.stats_close_hint)
    Column(
        modifier.padding(8.dp).widthIn(max = PANEL_WIDTH).clip(DeskShape).background(colorResource(R.color.hud_scrim))
            .border(1.dp, colorResource(R.color.hud_edge), DeskShape)
            .clickable(onClickLabel = close, onClick = onClose).padding(horizontal = 14.dp, vertical = 10.dp),
        verticalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Box(Modifier.size(10.dp).background(colorResource(gradeColor), CircleShape))
            Spacer(Modifier.width(8.dp))
            Text(stringResource(gradeRes), color = colorResource(R.color.hud_text), style = MaterialTheme.typography.titleSmall, modifier = Modifier.weight(1f))
            Text(close, color = colorResource(R.color.hud_text_secondary), style = MaterialTheme.typography.labelSmall)
        }
        Row(horizontalArrangement = Arrangement.spacedBy(20.dp)) {
            Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(10.dp)) {
                StatsGroup(R.string.stats_group_link, linkValues(details.stats))
                StatsGroup(R.string.stats_group_decoder, decoderValues(details))
            }
            Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(10.dp)) {
                StatsGroup(R.string.stats_group_picture, pictureValues(details))
                StatsGroup(R.string.stats_group_display, displayValues(details))
            }
        }
        StatsCharts(details.history, Modifier.fillMaxWidth())
    }
}

/** The stats pill's shape: round ends, below the one 18 dp radius at its height. */
private val PILL_SHAPE = RoundedCornerShape(14.dp)

/** A labelled value of the stats panel. */
private typealias StatsValue = Pair<Int, String>

@Composable
private fun StatsGroup(@StringRes title: Int, values: List<StatsValue>) {
    Column(verticalArrangement = Arrangement.spacedBy(1.dp)) {
        Text(stringResource(title), color = colorResource(R.color.hud_heading), style = MaterialTheme.typography.labelMedium)
        for ((label, value) in values) {
            Row {
                Text(
                    stringResource(label), color = colorResource(R.color.hud_text_secondary),
                    style = MaterialTheme.typography.bodySmall, modifier = Modifier.width(LABEL_WIDTH),
                )
                Text(value, color = colorResource(R.color.hud_text), style = MaterialTheme.typography.bodySmall)
            }
        }
    }
}

/** Frame rate, bitrate, round trip, loss, the delay from capture to the screen, and the data received. */
@Composable
private fun linkValues(s: LinkStats): List<StatsValue> = listOf(
    R.string.stats_label_fps to stringResource(R.string.value_fps, s.fps),
    R.string.stats_label_bitrate to stringResource(R.string.value_mbps, s.mbps),
    R.string.stats_label_rtt to stringResource(R.string.value_ms, s.rttMs),
    R.string.stats_label_loss to stringResource(R.string.value_loss, s.lossPct),
    // 0 until a frame has been shown.
    R.string.stats_label_delay to if (s.delayMs > 0) stringResource(R.string.value_ms, s.delayMs) else stringResource(R.string.value_not_yet),
    R.string.stats_label_received to formatBytes(s.receivedBytes),
)

/**
 * The stream's format and quality, what it shows, and where the picture
 * lands in physical pixels: the capture, encoded and on-screen sizes, and
 * whether the screen resamples it, which blurs text.
 */
@Composable
private fun pictureValues(d: StatsDetails): List<StatsValue> {
    val info = d.info
    val encoded = StreamSize(info.width, info.height)
    val resources = LocalResources.current
    val size = { s: StreamSize -> resources.getString(R.string.value_size, s.width, s.height) }
    return listOfNotNull(
        R.string.stats_label_stream to stringResource(R.string.value_stream, info.codec.uppercase(), info.width, info.height, info.fps),
        R.string.stats_label_quality to if (info.auto) {
            stringResource(R.string.value_auto_qp, info.level + 1, info.levels.size, info.qp)
        } else {
            stringResource(R.string.value_qp, info.qp)
        },
        info.display.takeIf { it.isNotBlank() }?.let { R.string.stats_label_screen to it },
        R.string.stats_label_capture to (captureSize(info)?.let(size) ?: stringResource(R.string.value_unknown)),
        R.string.stats_label_encoded to size(encoded),
        R.string.stats_label_shown to stringResource(R.string.value_shown, size(StreamSize(d.shown.width, d.shown.height)), d.shown.x, d.shown.y),
        R.string.stats_label_scale to (shownScale(d.shown, encoded)?.let { stringResource(R.string.stats_scaled, it) } ?: stringResource(R.string.stats_one_to_one)),
        d.exactTiles?.let { R.string.stats_label_exact_text to stringResource(R.string.value_tiles, it.drawn, it.hiddenFrames) },
    )
}

/**
 * The decoder's last second (mean and 95th percentile from a frame being
 * complete to leaving it), the session's lost frames and keyframe requests,
 * the GPU renderer's last second with Sharper colours, and what the decoder
 * reports about itself: its name, whether it is rated for the stream's size
 * at the session's top rate, and the low-latency options it refused.
 */
@Composable
private fun decoderValues(d: StatsDetails): List<StatsValue> {
    val s = d.stats
    val values = mutableListOf(
        R.string.stats_label_decode to stringResource(R.string.value_decode, s.decodeMicros / 1000.0, s.decodeP95Micros / 1000.0),
        R.string.stats_label_frames_lost to s.framesLost.toString(),
        R.string.stats_label_keyframes to s.keyframeRequests.toString(),
    )
    renderingValue(d.rendering)?.let { values += R.string.stats_label_renderer to it }
    d.decoder?.let { r ->
        val rating = stringResource(
            when (r.rating) {
                DecoderRating.RATED -> R.string.decoder_rated
                DecoderRating.NOT_RATED -> R.string.decoder_not_rated
                DecoderRating.UNKNOWN -> R.string.decoder_rating_unknown
            },
        )
        values += R.string.stats_label_decoder to r.name
        values += R.string.stats_label_rating to stringResource(R.string.value_rating, r.width, r.height, r.fps, rating)
        if (r.refused.isNotEmpty()) values += R.string.stats_label_refused to r.refused.joinToString()
    }
    return values
}

/** How the video is drawn; null when Sharper colours is off. */
@Composable
private fun renderingValue(r: VideoRendering): String? = when (r) {
    VideoRendering.Direct -> null
    is VideoRendering.FellBack -> stringResource(R.string.value_render_off, r.reason)
    is VideoRendering.Guided -> when (val s = r.second) {
        null -> stringResource(R.string.value_render_starting)
        else -> stringResource(
            if (r.gpuTimed) R.string.value_render_gpu else R.string.value_render_cpu,
            s.meanMicros / 1000.0, s.maxMicros / 1000.0,
        )
    }
}

/** The panel's rate with power saving, the stream's frame-rate cap, the chip and Android's media performance class. */
@Composable
private fun displayValues(d: StatsDetails): List<StatsValue> {
    val panel = d.panel
    return listOf(
        R.string.stats_label_panel to when {
            panel == null -> stringResource(R.string.value_unknown)
            panel.powerSave -> stringResource(R.string.value_hz_saving, panel.hz.roundToInt())
            else -> stringResource(R.string.value_hz, panel.hz.roundToInt())
        },
        R.string.stats_label_cap to if (d.maxFps > 0) stringResource(R.string.value_fps, d.maxFps) else stringResource(R.string.value_none),
        R.string.stats_label_chip to d.soc,
        R.string.stats_label_media_class to if (d.mediaClass > 0) d.mediaClass.toString() else stringResource(R.string.value_none),
    )
}

/** The link grade's label and colour. */
internal fun gradeOf(q: LinkQuality): Pair<Int, Int> = when (q) {
    LinkQuality.EXCELLENT -> R.string.quality_excellent to R.color.quality_excellent
    LinkQuality.GOOD -> R.string.quality_good to R.color.quality_good
    LinkQuality.FAIR -> R.string.quality_fair to R.color.quality_fair
    LinkQuality.POOR -> R.string.quality_poor to R.color.quality_poor
}

/** Bytes as KB, MB or GB with one decimal above a megabyte. */
internal fun formatBytes(b: Long): String = when {
    b >= 1L shl 30 -> String.format(Locale.ROOT, "%.2f GB", b / (1L shl 30).toDouble())
    b >= 1L shl 20 -> String.format(Locale.ROOT, "%.1f MB", b / (1L shl 20).toDouble())
    else -> String.format(Locale.ROOT, "%d KB", b shr 10)
}

private val PANEL_WIDTH = 680.dp
private val LABEL_WIDTH = 112.dp
