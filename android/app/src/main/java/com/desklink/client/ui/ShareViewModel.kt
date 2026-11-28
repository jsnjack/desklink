package com.desklink.client.ui

import android.app.Application
import android.net.Uri
import android.provider.OpenableColumns
import android.util.Log
import android.webkit.MimeTypeMap
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.viewModelScope
import com.desklink.client.R
import com.desklink.client.crypto.SessionCrypto
import com.desklink.client.model.HostProfile
import com.desklink.client.model.HostStore
import com.desklink.client.model.SharedNames
import com.desklink.client.model.mergeSameMachine
import com.desklink.client.net.ControlClient
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.joinAll
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.io.ByteArrayInputStream
import java.io.IOException
import java.io.InputStream

/** What another app shared: a file by its content URI, or text, sent as a `.txt` file called [Text.name]. */
sealed interface SharedSource {
    /** A file another app shared by its content URI. */
    data class File(val uri: Uri) : SharedSource

    /** Shared text. */
    data class Text(val name: String, val text: String) : SharedSource
}

/** Where one shared item is. */
sealed interface ShareStatus {
    /** Not sent yet. */
    data object Waiting : ShareStatus

    /** [sent] bytes are on their way. */
    data class Sending(val sent: Long) : ShareStatus

    /** The computer [hostName] saved it as [name]. */
    data class Saved(val name: String, val hostName: String) : ShareStatus

    /** Sending failed, with a message for the user. */
    data class Failed(val message: String) : ShareStatus

    /** The user cancelled before it was saved. */
    data object Cancelled : ShareStatus
}

/** One shared item: [size] in bytes, or -1 when unknown. */
data class ShareItem(val id: Int, val source: SharedSource, val name: String, val size: Long, val status: ShareStatus = ShareStatus.Waiting)

/**
 * Sends what another app shared to a paired host (`POST /files`), one file
 * after the other, because the host takes one upload per device at a time.
 * It needs no running session. It lives as long as the share screen: leaving
 * that screen cancels the transfer, and rotating it does not.
 */
class ShareViewModel(app: Application) : AndroidViewModel(app) {
    private val store = HostStore(app)

    /** The paired hosts, one per machine. */
    val hosts: List<HostProfile> = mergeSameMachine(store.profiles()).filter { it.paired }

    private val _loading = MutableStateFlow(true)
    /** Whether the sharing app's file names and sizes are still being read. */
    val loading: StateFlow<Boolean> = _loading

    private val _items = MutableStateFlow<List<ShareItem>>(emptyList())
    /** The shared items and how far each got. */
    val items: StateFlow<List<ShareItem>> = _items

    private val _target = MutableStateFlow<HostProfile?>(null)
    /** The host being reached or sent to, null while none is. */
    val target: StateFlow<HostProfile?> = _target

    private val _reaching = MutableStateFlow(false)
    /** Whether the host's addresses are being asked for /health. */
    val reaching: StateFlow<Boolean> = _reaching

    private val _error = MutableStateFlow<String?>(null)
    /** Why the last attempt could not send at all, such as an unreachable host. */
    val error: StateFlow<String?> = _error

    private var offered = false
    private var job: Job? = null

    private fun str(id: Int, vararg args: Any): String = getApplication<Application>().getString(id, *args)

    /**
     * Takes what was shared, once: the activity offers it again after a
     * rotation, which keeps this view model. The user starts the transfer
     * after reviewing the files and destination.
     */
    fun offer(sources: List<SharedSource>) {
        if (offered) return
        offered = true
        viewModelScope.launch {
            _items.value = withContext(Dispatchers.IO) { sources.mapIndexed(::describe) }
            _loading.value = false
        }
    }

    /** The name and size of a shared item, from the sharing app's OpenableColumns where it offers them. */
    private fun describe(id: Int, source: SharedSource): ShareItem = when (source) {
        is SharedSource.Text -> ShareItem(id, source, source.name, source.text.toByteArray().size.toLong())
        is SharedSource.File -> {
            val resolver = getApplication<Application>().contentResolver
            var display: String? = null
            var size = -1L
            runCatching {
                resolver.query(source.uri, arrayOf(OpenableColumns.DISPLAY_NAME, OpenableColumns.SIZE), null, null, null)?.use { c ->
                    if (c.moveToFirst()) {
                        display = c.getString(0)
                        if (!c.isNull(1)) size = c.getLong(1)
                    }
                }
            }.onFailure { Log.d(TAG, "describe a shared file: ${it.message}") }
            val extension = runCatching { resolver.getType(source.uri) }.getOrNull()
                ?.let { MimeTypeMap.getSingleton().getExtensionFromMimeType(it) }
            ShareItem(id, source, SharedNames.clean(display ?: source.uri.lastPathSegment, extension), size)
        }
    }

    /**
     * Sends every item not saved yet to [host]: it asks each of the host's
     * addresses for /health with the pinned certificate at once and sends to
     * the first that answers, with a fresh token for each file. A host that
     * is asleep is not woken; the message says it did not answer.
     */
    fun send(host: HostProfile) {
        if (job?.isActive == true) return
        _error.value = null
        _target.value = host
        job = viewModelScope.launch {
            try {
                val key = store.peerKey(host)
                val peerId = host.peerId
                if (key == null || peerId == null) {
                    _error.value = str(R.string.msg_not_paired)
                    return@launch
                }
                _reaching.value = true
                val address = reach(host)
                _reaching.value = false
                if (address == null) {
                    _error.value = str(R.string.err_share_unreachable, host.name)
                    return@launch
                }
                for (item in _items.value.filter { it.status !is ShareStatus.Saved }) {
                    sendOne(ControlClient(getApplication(), address, host.port, host.fingerprint), peerId, key, item, host.name)
                }
            } catch (e: CancellationException) {
                _items.update { list ->
                    list.map {
                        if (it.status is ShareStatus.Waiting || it.status is ShareStatus.Sending) it.copy(status = ShareStatus.Cancelled) else it
                    }
                }
                throw e
            } finally {
                _reaching.value = false
                _target.value = null
            }
        }
    }

    private suspend fun sendOne(client: ControlClient, peerId: String, key: ByteArray, item: ShareItem, hostName: String) {
        setStatus(item.id, ShareStatus.Sending(0))
        try {
            val token = SessionCrypto.authToken(peerId, key, client.health().timeMs / 1000)
            var reported = 0L
            val reply = client.sendFile(token, item.name, item.size, { open(item.source) }) { sent ->
                // Every chunk would redraw the screen hundreds of times a second.
                if (sent - reported >= PROGRESS_STEP || sent == item.size) {
                    reported = sent
                    setStatus(item.id, ShareStatus.Sending(sent))
                }
            }
            setStatus(item.id, ShareStatus.Saved(reply.name, hostName))
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            Log.w(TAG, "send a shared file", e)
            setStatus(item.id, ShareStatus.Failed(e.message ?: e.toString()))
        }
    }

    private fun open(source: SharedSource): InputStream = when (source) {
        is SharedSource.Text -> ByteArrayInputStream(source.text.toByteArray())
        is SharedSource.File -> getApplication<Application>().contentResolver.openInputStream(source.uri)
            ?: throw IOException(str(R.string.err_share_open))
    }

    /** Stops the transfer; what was not saved yet is marked cancelled. */
    fun cancel() {
        job?.cancel()
    }

    private fun setStatus(id: Int, status: ShareStatus) = _items.update { list -> list.map { if (it.id == id) it.copy(status = status) else it } }

    /**
     * The first of [p]'s addresses to answer /health with the pinned
     * certificate, or null when none does. The probes run in the view
     * model's scope, so a slow address never delays the answer.
     */
    private suspend fun reach(p: HostProfile): String? {
        val found = CompletableDeferred<String?>()
        val probes = p.candidates().map { addr ->
            viewModelScope.launch {
                runCatching { ControlClient(getApplication(), addr, p.port, p.fingerprint).health() }
                    .onSuccess { found.complete(addr) }
            }
        }
        viewModelScope.launch {
            probes.joinAll()
            found.complete(null)
        }
        return found.await()
    }

    private companion object {
        const val TAG = "DeskLink"

        /** Progress is shown in steps of this many bytes. */
        const val PROGRESS_STEP = 256 * 1024L
    }
}
