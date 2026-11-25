package com.desklink.client.ui

import androidx.annotation.StringRes
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.StrokeJoin
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.res.colorResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.unit.dp
import com.desklink.client.R
import com.desklink.client.net.LinkStats
import com.desklink.client.net.StatsHistory
import java.util.Locale

/**
 * One chart of the stats overlay: a metric of [LinkStats] on its own scale,
 * which reaches at least [minimum] so a quiet link does not fill the chart.
 * [value] is null where the metric is not known yet.
 */
private class ChartMetric(
    @StringRes val title: Int,
    val minimum: Double,
    val decimals: Int,
    val value: (LinkStats) -> Double?,
)

/** One series per chart, each on its own scale: small multiples, never two scales on one chart. */
private val chartMetrics = listOf(
    ChartMetric(R.string.chart_fps, minimum = 30.0, decimals = 0) { it.fps.toDouble() },
    ChartMetric(R.string.chart_mbps, minimum = 1.0, decimals = 1) { it.mbps },
    ChartMetric(R.string.chart_rtt, minimum = 50.0, decimals = 0) { it.rttMs.toDouble() },
    ChartMetric(R.string.chart_loss, minimum = 2.0, decimals = 1) { it.lossPct },
    // 0 means no frame has been shown yet.
    ChartMetric(R.string.chart_delay, minimum = 50.0, decimals = 0) { s -> s.delayMs.takeIf { it > 0 }?.toDouble() },
)

/**
 * The last minute of the link, one small chart per metric, three to a row
 * sharing the width, for the stats panel. Each chart names its metric and
 * shows its current value and the top of its scale; the newest sample is at
 * the right.
 */
@Composable
internal fun StatsCharts(history: StatsHistory, modifier: Modifier = Modifier) {
    Column(modifier, verticalArrangement = Arrangement.spacedBy(4.dp)) {
        for (row in chartMetrics.chunked(CHARTS_PER_ROW)) {
            Row(horizontalArrangement = Arrangement.spacedBy(16.dp)) {
                for (metric in row) MetricChart(metric, history, Modifier.weight(1f))
                // A short last row keeps the others' chart width.
                repeat(CHARTS_PER_ROW - row.size) { Spacer(Modifier.weight(1f)) }
            }
        }
        Text(
            stringResource(R.string.chart_window, StatsHistory.CAPACITY),
            color = colorResource(R.color.hud_text_secondary), style = MaterialTheme.typography.labelSmall,
        )
    }
}

@Composable
private fun MetricChart(metric: ChartMetric, history: StatsHistory, modifier: Modifier) {
    val values = history.samples.map(metric.value)
    val ceiling = chartCeiling(values.filterNotNull(), metric.minimum)
    val title = stringResource(metric.title)
    val now = values.lastOrNull()?.let { String.format(Locale.ROOT, "%.${metric.decimals}f", it) } ?: "–"
    val description = stringResource(R.string.chart_description, title, now, formatScale(ceiling))
    val secondary = colorResource(R.color.hud_text_secondary)
    val line = colorResource(R.color.hud_chart_line)
    val rule = secondary.copy(alpha = 0.3f)
    Column(modifier.clearAndSetSemantics { contentDescription = description }) {
        Row(verticalAlignment = Alignment.Bottom) {
            Text(title, color = secondary, style = MaterialTheme.typography.labelSmall)
            Spacer(Modifier.width(4.dp))
            Text(
                stringResource(R.string.chart_scale, formatScale(ceiling)),
                color = secondary.copy(alpha = 0.7f), style = MaterialTheme.typography.labelSmall,
            )
            Spacer(Modifier.weight(1f))
            Text(now, color = colorResource(R.color.hud_text), style = MaterialTheme.typography.labelSmall)
        }
        Canvas(Modifier.fillMaxWidth().height(CHART_HEIGHT).padding(vertical = 2.dp)) {
            val hairline = 1.dp.toPx()
            // Recessive rules at the top of the scale and at zero.
            drawLine(rule, Offset(0f, 0f), Offset(size.width, 0f), hairline)
            drawLine(rule, Offset(0f, size.height), Offset(size.width, size.height), hairline)
            val points = chartPoints(values, StatsHistory.CAPACITY, size.width, size.height, ceiling)
            val path = Path()
            var drawing = false
            for (p in points) {
                if (p == null) {
                    drawing = false
                } else if (drawing) {
                    path.lineTo(p.x, p.y)
                } else {
                    path.moveTo(p.x, p.y)
                    drawing = true
                }
            }
            drawPath(path, line, style = Stroke(width = 2.dp.toPx(), cap = StrokeCap.Round, join = StrokeJoin.Round))
            // The newest sample, so a lone first point shows too.
            points.lastOrNull()?.let { drawCircle(line, radius = 2.5.dp.toPx(), center = Offset(it.x, it.y)) }
        }
    }
}

private const val CHARTS_PER_ROW = 3
private val CHART_HEIGHT = 32.dp
