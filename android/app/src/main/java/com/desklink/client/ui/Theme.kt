package com.desklink.client.ui

import android.app.Activity
import android.content.Context
import android.content.ContextWrapper
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.ColorScheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Shapes
import androidx.compose.material3.Typography
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.Immutable
import androidx.compose.runtime.ReadOnlyComposable
import androidx.compose.runtime.SideEffect
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalView
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.Font
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontSynthesis
import androidx.compose.ui.text.font.FontVariation
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.core.view.WindowCompat
import com.desklink.client.R

/**
 * The design's colour roles (docs/design.md, Colour) for one theme. [link]
 * is Blue Ink in light and Blue in dark, where Blue Ink is too dark for
 * text; [danger] fills destructive buttons in both, and [dangerText] is
 * its readable shade for text.
 */
@Immutable
data class DeskColors(
    val dark: Boolean,
    val blue: Color,
    val blueInk: Color,
    val blueTint: Color,
    val page: Color,
    val card: Color,
    val line: Color,
    val text: Color,
    val muted: Color,
    val terminal: Color,
    val link: Color,
    val online: Color,
    val waking: Color,
    val asleep: Color,
    val unreachable: Color,
    val danger: Color,
    val dangerText: Color,
    val warningText: Color,
)

internal val LightDesk = DeskColors(
    dark = false,
    blue = Color(0xFF4380D0),
    blueInk = Color(0xFF306EC0),
    blueTint = Color(0xFFEBF1FA),
    page = Color(0xFFF6F5F4),
    card = Color(0xFFFFFFFF),
    line = Color(0xFFDEDDDA),
    text = Color(0xFF241F31),
    muted = Color(0xFF5E5C64),
    terminal = Color(0xFF241F31),
    link = Color(0xFF306EC0),
    online = Color(0xFF26A269),
    waking = Color(0xFFE5A50A),
    asleep = Color(0xFF9A9996),
    unreachable = Color(0xFFE01B24),
    danger = Color(0xFFC01C28),
    dangerText = Color(0xFFC01C28),
    // GNOME's yellow is a dot colour, too light for text on white: this is its 5.9:1 shade.
    warningText = Color(0xFF8A5A00),
)

internal val DarkDesk = DeskColors(
    dark = true,
    blue = Color(0xFF7AA9E6),
    blueInk = Color(0xFF306EC0),
    blueTint = Color(0xFF2B3A55),
    page = Color(0xFF1C2330),
    card = Color(0xFF262F3F),
    line = Color(0xFF364156),
    text = Color(0xFFE9EDF3),
    muted = Color(0xFFA7B1C2),
    terminal = Color(0xFF10151F),
    link = Color(0xFF7AA9E6),
    online = Color(0xFF33D17A),
    waking = Color(0xFFF6D32D),
    asleep = Color(0xFF77767B),
    unreachable = Color(0xFFF66151),
    danger = Color(0xFFC01C28),
    dangerText = Color(0xFFF66151),
    warningText = Color(0xFFF6D32D),
)

private val LocalDeskColors = staticCompositionLocalOf { LightDesk }

/** The design's tokens beside Material's: `DeskTheme.colors.online`. */
object DeskTheme {
    val colors: DeskColors
        @Composable @ReadOnlyComposable get() = LocalDeskColors.current
}

/** Montserrat Medium: titles, names, labels and numbers. */
val Montserrat = FontFamily(Font(R.font.montserrat_medium, FontWeight.Medium))

/** Noto Sans: running text. */
val NotoSans = FontFamily(Font(R.font.noto_sans, FontWeight.Normal))

/** Noto Sans Mono at its regular weight: commands and code, in terminal blocks. */
val NotoSansMono = FontFamily(
    Font(R.font.noto_sans_mono, FontWeight.Normal, variationSettings = FontVariation.Settings(FontVariation.weight(400))),
)

/** The one radius: cards, buttons, fields, sheets, dialogs, the rail and the stats panel. */
val DeskShape = RoundedCornerShape(18.dp)

/**
 * The type scale (docs/design.md, Type): Display 34, Title 22, Subtitle 17
 * and Label 13 in Montserrat Medium with tabular figures, Body 15 in Noto
 * Sans. Each family has one weight, so nothing is synthesised bold.
 */
internal val DeskTypography: Typography = run {
    fun montserrat(size: Int, line: Int, spacing: Double = 0.0) = TextStyle(
        fontFamily = Montserrat, fontWeight = FontWeight.Medium, fontSize = size.sp, lineHeight = line.sp,
        letterSpacing = spacing.sp, fontFeatureSettings = "tnum", fontSynthesis = FontSynthesis.None,
    )
    fun noto(size: Int, line: Int) = TextStyle(
        fontFamily = NotoSans, fontWeight = FontWeight.Normal, fontSize = size.sp, lineHeight = line.sp,
        fontSynthesis = FontSynthesis.None,
    )
    val display = montserrat(34, 40, -0.6)
    val title = montserrat(22, 28, -0.2)
    Typography(
        displayLarge = display, displayMedium = display, displaySmall = display,
        headlineLarge = title, headlineMedium = title, headlineSmall = title,
        titleLarge = title,
        titleMedium = montserrat(17, 24),
        titleSmall = montserrat(15, 20),
        bodyLarge = noto(15, 22),
        bodyMedium = noto(14, 20),
        bodySmall = noto(13, 18),
        labelLarge = montserrat(14, 20),
        labelMedium = montserrat(13, 18),
        labelSmall = montserrat(12, 16),
    )
}

/** Commands and code in a terminal block. */
val MonoStyle = TextStyle(fontFamily = NotoSansMono, fontSize = 14.sp, lineHeight = 20.sp, fontSynthesis = FontSynthesis.None)

private val DeskShapes = Shapes(
    extraSmall = DeskShape, small = DeskShape, medium = DeskShape, large = DeskShape, extraLarge = DeskShape,
)

/** Material's roles filled from the design's: pages, white cards, Blue Ink actions, no tint. */
internal fun DeskColors.scheme(dark: Boolean): ColorScheme {
    val base = if (dark) darkColorScheme() else lightColorScheme()
    return base.copy(
        primary = blueInk, onPrimary = Color.White,
        primaryContainer = blueTint, onPrimaryContainer = if (dark) text else blueInk,
        inversePrimary = blue,
        secondary = blueInk, onSecondary = Color.White,
        secondaryContainer = blueTint, onSecondaryContainer = if (dark) text else blueInk,
        tertiary = warningText, onTertiary = if (dark) page else Color.White,
        tertiaryContainer = if (dark) Color(0xFF3D3520) else Color(0xFFFDF3D4), onTertiaryContainer = if (dark) waking else warningText,
        background = page, onBackground = text,
        surface = page, onSurface = text,
        surfaceVariant = card, onSurfaceVariant = muted,
        surfaceTint = Color.Transparent,
        surfaceBright = card, surfaceDim = page,
        surfaceContainerLowest = card, surfaceContainerLow = card, surfaceContainer = card,
        surfaceContainerHigh = card, surfaceContainerHighest = card,
        inverseSurface = text, inverseOnSurface = page,
        outline = asleep, outlineVariant = line,
        error = dangerText, onError = Color.White,
        errorContainer = if (dark) Color(0xFF4C2531) else Color(0xFFFBE5E6),
        onErrorContainer = if (dark) Color(0xFFFFB8B0) else Color(0xFFA51D2D),
    )
}

/**
 * DeskLink's light or dark theme, as the system is. The status and
 * navigation bars' icons follow, dark on the light theme's pages, because
 * the window's theme alone would keep them white; over the stream
 * ([overVideo]) they stay white for the dark video.
 */
@Composable
fun DeskLinkTheme(overVideo: Boolean = false, content: @Composable () -> Unit) {
    val dark = isSystemInDarkTheme()
    val view = LocalView.current
    val lightBars = !dark && !overVideo
    SideEffect {
        val window = view.context.activity()?.window ?: return@SideEffect
        // Android's scrim behind a light gesture bar would draw a white strip under the page.
        window.isNavigationBarContrastEnforced = false
        WindowCompat.getInsetsController(window, view).apply {
            isAppearanceLightStatusBars = lightBars
            isAppearanceLightNavigationBars = lightBars
        }
    }
    val colors = if (dark) DarkDesk else LightDesk
    CompositionLocalProvider(LocalDeskColors provides colors) {
        MaterialTheme(colorScheme = colors.scheme(dark), typography = DeskTypography, shapes = DeskShapes, content = content)
    }
}

private tailrec fun Context.activity(): Activity? = when (this) {
    is Activity -> this
    is ContextWrapper -> baseContext.activity()
    else -> null
}

/** The dark theme in both system themes, for the HUD, which sits over video. */
@Composable
fun HudTheme(content: @Composable () -> Unit) {
    CompositionLocalProvider(LocalDeskColors provides DarkDesk) {
        MaterialTheme(colorScheme = DarkDesk.scheme(dark = true), typography = DeskTypography, shapes = DeskShapes, content = content)
    }
}
