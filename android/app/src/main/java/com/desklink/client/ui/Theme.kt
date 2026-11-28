package com.desklink.client.ui

import android.app.Activity
import android.content.Context
import android.content.ContextWrapper
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.dynamicDarkColorScheme
import androidx.compose.material3.dynamicLightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.SideEffect
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalView
import androidx.core.view.WindowCompat

/**
 * Material 3 with the wallpaper's colours, light or dark as the system is.
 * The status and navigation bars' icons follow, dark on the light theme's
 * pages, because the window's theme alone would keep them white; over the
 * stream ([overVideo]) they stay white for the dark video.
 */
@Composable
fun DeskLinkTheme(overVideo: Boolean = false, content: @Composable () -> Unit) {
    val ctx = LocalContext.current
    val dark = isSystemInDarkTheme()
    val view = LocalView.current
    val lightBars = !dark && !overVideo
    SideEffect {
        val window = view.context.activity()?.window ?: return@SideEffect
        WindowCompat.getInsetsController(window, view).apply {
            isAppearanceLightStatusBars = lightBars
            isAppearanceLightNavigationBars = lightBars
        }
    }
    val scheme = if (dark) dynamicDarkColorScheme(ctx) else dynamicLightColorScheme(ctx)
    MaterialTheme(colorScheme = scheme, content = content)
}

private tailrec fun Context.activity(): Activity? = when (this) {
    is Activity -> this
    is ContextWrapper -> baseContext.activity()
    else -> null
}

/** The wallpaper's dark colours in both system themes, for the HUD, which sits over video. */
@Composable
fun HudTheme(content: @Composable () -> Unit) {
    MaterialTheme(colorScheme = dynamicDarkColorScheme(LocalContext.current), content = content)
}
