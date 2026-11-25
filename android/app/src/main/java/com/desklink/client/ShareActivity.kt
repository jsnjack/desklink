package com.desklink.client

import android.content.ContentResolver
import android.content.Intent
import android.net.Uri
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.viewModels
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.dynamicDarkColorScheme
import androidx.compose.material3.dynamicLightColorScheme
import androidx.compose.ui.platform.LocalContext
import androidx.core.content.IntentCompat
import androidx.core.view.WindowCompat
import com.desklink.client.model.SharedNames
import com.desklink.client.ui.ShareScreen
import com.desklink.client.ui.SharedSource
import com.desklink.client.ui.ShareViewModel

/**
 * The share target: "DeskLink" in Android's share sheet sends what another
 * app shares to a paired computer's download folder, without a session.
 * Leaving the screen cancels a transfer still running.
 */
class ShareActivity : ComponentActivity() {
    private val vm: ShareViewModel by viewModels()

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        WindowCompat.setDecorFitsSystemWindows(window, false)
        vm.offer(shared(intent))
        setContent {
            val dark = isSystemInDarkTheme()
            val ctx = LocalContext.current
            MaterialTheme(colorScheme = if (dark) dynamicDarkColorScheme(ctx) else dynamicLightColorScheme(ctx)) {
                ShareScreen(vm, onClose = ::finish)
            }
        }
    }

    /**
     * What the intent shares: its streams, or else its text. Only content
     * URIs are taken: a file URI would let another app make DeskLink send
     * the app's own private files.
     */
    private fun shared(intent: Intent): List<SharedSource> {
        val uris = when (intent.action) {
            Intent.ACTION_SEND -> listOfNotNull(IntentCompat.getParcelableExtra(intent, Intent.EXTRA_STREAM, Uri::class.java))
            Intent.ACTION_SEND_MULTIPLE -> IntentCompat.getParcelableArrayListExtra(intent, Intent.EXTRA_STREAM, Uri::class.java).orEmpty()
            else -> emptyList()
        }.filter { it.scheme == ContentResolver.SCHEME_CONTENT }
        if (uris.isNotEmpty()) return uris.map { SharedSource.File(it) }
        val text = intent.getCharSequenceExtra(Intent.EXTRA_TEXT)?.toString()
        if (text.isNullOrEmpty()) return emptyList()
        return listOf(SharedSource.Text(SharedNames.forText(intent.getStringExtra(Intent.EXTRA_SUBJECT)), text))
    }
}
