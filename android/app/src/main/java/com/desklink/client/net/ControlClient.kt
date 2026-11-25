package com.desklink.client.net

import android.annotation.SuppressLint
import android.content.Context
import com.desklink.client.R
import com.desklink.client.crypto.EphemeralKey
import com.desklink.client.crypto.SessionCrypto
import com.desklink.client.model.B64
import com.desklink.client.model.Envelope
import com.desklink.client.model.Health
import com.desklink.client.model.PairRequest
import com.desklink.client.model.PairResponse
import com.desklink.client.model.ProtocolJson
import com.desklink.client.model.UploadReply
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlinx.coroutines.withContext
import kotlinx.serialization.json.JsonElement
import okhttp3.Call
import okhttp3.Callback
import okhttp3.HttpUrl
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.BufferedSink
import java.io.IOException
import java.io.InputStream
import kotlin.coroutines.resume
import kotlin.coroutines.resumeWithException
import java.net.ConnectException
import java.security.cert.CertificateException
import java.security.cert.X509Certificate
import java.util.concurrent.TimeUnit
import javax.net.ssl.SSLContext
import javax.net.ssl.X509TrustManager

/** A pairing that succeeded: what the profile stores. */
class PairResult(val peerId: String, val peerKey: ByteArray, val fingerprint: String)

/** A pairing the host or the client refused, with a message for the user. */
class PairingException(message: String) : IOException(message)

/**
 * HTTPS and WebSocket client for one host. With a known fingerprint it trusts
 * only that certificate. Without one it trusts any certificate, which is only
 * good for showing whether an unpaired host answers: [pair] refuses to run
 * without a pinned fingerprint, because whoever answered could then test
 * every PIN offline against the client's proof.
 */
class ControlClient(
    private val context: Context,
    private val host: String,
    private val port: Int,
    pinnedFingerprint: String?,
) {
    private val pinned = pinnedFingerprint
    private val trust = PinningTrustManager(pinnedFingerprint) { fp -> context.getString(R.string.err_fingerprint_mismatch, fp) }
    private val http: OkHttpClient = OkHttpClient.Builder()
        .sslSocketFactory(SSLContext.getInstance("TLSv1.3").apply { init(null, arrayOf(trust), null) }.socketFactory, trust)
        // The pinned fingerprint replaces hostname checks; hosts are addressed by IP.
        .hostnameVerifier { _, _ -> true }
        .connectTimeout(3, TimeUnit.SECONDS)
        .readTimeout(10, TimeUnit.SECONDS)
        .pingInterval(5, TimeUnit.SECONDS)
        .build()

    private val base = "https://$host:$port"

    /** Calls GET /health. */
    suspend fun health(): Health = withContext(Dispatchers.IO) {
        http.newCall(Request.Builder().url("$base/health").build()).execute().use { resp ->
            if (!resp.isSuccessful) throw IOException(context.getString(R.string.err_http, resp.code))
            ProtocolJson.decodeFromString(Health.serializer(), resp.body.string())
        }
    }

    /**
     * Runs the two-step ECDH + PIN pairing against POST /pair, with the
     * certificate pinned to the fingerprint from the pairing QR code.
     */
    suspend fun pair(pin: String, deviceName: String): PairResult = withContext(Dispatchers.IO) {
        if (pinned.isNullOrBlank()) throw PairingException(context.getString(R.string.err_pair_needs_fingerprint))
        val key = EphemeralKey()
        val clientPub = key.publicBytes()
        val start = postPair(PairRequest(phase = "start", clientPub = B64.encode(clientPub)))
        val hostPub = B64.decode(start.hostPub ?: throw protocolError())
        val fingerprint = trust.observed ?: throw protocolError()
        val keys = SessionCrypto.derivePairingKeys(key.agree(hostPub), pin, clientPub, hostPub, fingerprint)
        val finish = postPair(
            PairRequest(phase = "finish", pairingId = start.pairingId, proof = B64.encode(keys.clientProof()), name = deviceName)
        )
        val hostProof = B64.decode(finish.proof ?: throw protocolError())
        if (!java.security.MessageDigest.isEqual(hostProof, keys.hostProof())) {
            throw PairingException(context.getString(R.string.err_host_proof))
        }
        PairResult(finish.peerId ?: throw protocolError(), keys.peerKey, fingerprint)
    }

    private fun protocolError() = PairingException(context.getString(R.string.err_pair_protocol))

    private fun postPair(req: PairRequest): PairResponse {
        val body = ProtocolJson.encodeToString(PairRequest.serializer(), req).toRequestBody(JSON)
        http.newCall(Request.Builder().url("$base/pair").post(body).build()).execute().use { resp ->
            val parsed = ProtocolJson.decodeFromString(PairResponse.serializer(), resp.body.string())
            if (!resp.isSuccessful) throw PairingException(parsed.error ?: context.getString(R.string.err_http, resp.code))
            return parsed
        }
    }

    /**
     * Asks the host to forget this device (DELETE /pair) with a bearer token
     * in host time. Only 200 confirms it: the host answers 401 both when it
     * no longer knows the device and when it rejects the token (its clock
     * moved, say), so a 401 throws and the caller reports the removal as
     * unconfirmed.
     */
    suspend fun forget(token: String) = withContext(Dispatchers.IO) {
        val request = Request.Builder().url("$base/pair").delete().header("Authorization", "Bearer $token").build()
        http.newCall(request).execute().use { resp ->
            when (ForgetAnswer.of(resp.code)) {
                ForgetAnswer.REMOVED -> Unit
                ForgetAnswer.UNCONFIRMED -> throw IOException(context.getString(R.string.err_forget_unconfirmed))
                ForgetAnswer.UNSUPPORTED -> throw IOException(context.getString(R.string.err_forget_unsupported))
                ForgetAnswer.FAILED -> throw IOException(context.getString(R.string.err_http, resp.code))
            }
        }
    }

    /**
     * Sends a file to the host (`POST /files`) with a bearer token in host
     * time, streaming it from [open] without holding it in memory. [size] is
     * its length, or -1 when unknown (the body is then chunked); [progress]
     * gets the bytes sent so far, on OkHttp's thread. Returns the host's
     * reply, which names the file as saved; throws with a message for the
     * user when the host refused it or the connection failed. Cancelling the
     * coroutine cancels the request.
     */
    suspend fun sendFile(token: String, name: String, size: Long, open: () -> InputStream, progress: (Long) -> Unit): UploadReply {
        val body = object : RequestBody() {
            override fun contentType() = OCTET_STREAM

            override fun contentLength() = size

            // The stream is opened for one write; a retried request could not reread it.
            override fun isOneShot() = true

            override fun writeTo(sink: BufferedSink) {
                open().use { input ->
                    val buf = ByteArray(UPLOAD_CHUNK)
                    var sent = 0L
                    while (true) {
                        val n = input.read(buf)
                        if (n < 0) break
                        sink.write(buf, 0, n)
                        sent += n
                        progress(sent)
                    }
                }
            }
        }
        // The host refuses a file it will not take before reading it and then
        // closes the connection; a body still being sent would turn its answer
        // into a connection reset. With 100-continue OkHttp waits for its go.
        val request = Request.Builder().url(uploadUrl(host, port, name)).post(body)
            .header("Authorization", "Bearer $token").header("Expect", "100-continue").build()
        val call = uploads.newCall(request)
        val (code, text) = suspendCancellableCoroutine { cont ->
            cont.invokeOnCancellation { call.cancel() }
            call.enqueue(object : Callback {
                override fun onFailure(call: Call, e: IOException) = cont.resumeWithException(e)

                override fun onResponse(call: Call, response: Response) {
                    val read = runCatching { response.use { it.code to it.body.string() } }
                    read.fold({ cont.resume(it) }, { cont.resumeWithException(it) })
                }
            })
        }
        val reply = runCatching { ProtocolJson.decodeFromString(UploadReply.serializer(), text) }.getOrNull()
        val answer = UploadAnswer.of(code)
        if (answer == UploadAnswer.SAVED && reply != null) return reply
        throw IOException(uploadError(answer, code, reply?.error))
    }

    // The host's own message says best why it refused, except for a token it
    // rejected or an endpoint it lacks, which it answers in plain text.
    private fun uploadError(answer: UploadAnswer, code: Int, hostMessage: String?): String = when (answer) {
        UploadAnswer.REJECTED -> context.getString(R.string.err_upload_rejected)
        UploadAnswer.UNSUPPORTED -> context.getString(R.string.err_upload_unsupported)
        UploadAnswer.BUSY -> hostMessage ?: context.getString(R.string.err_upload_busy)
        UploadAnswer.TOO_LARGE -> hostMessage ?: context.getString(R.string.err_upload_too_large)
        UploadAnswer.DISK_FULL -> hostMessage ?: context.getString(R.string.err_upload_disk_full)
        UploadAnswer.SAVED, UploadAnswer.FAILED -> hostMessage ?: context.getString(R.string.err_http, code)
    }

    // Uploads wait longer for the reply than other requests: the host answers
    // once it has moved the file into place. Writes keep the usual timeout,
    // which applies to each write, not to the whole body.
    private val uploads: OkHttpClient by lazy { http.newBuilder().readTimeout(UPLOAD_REPLY_TIMEOUT_S, TimeUnit.SECONDS).build() }

    /**
     * Opens the control WebSocket with a bearer token in host time. The
     * listener's [Listener.onOpen] gets this socket, so the first message
     * goes out on it even before this call has returned.
     */
    fun openSession(token: String, listener: Listener): ControlSession {
        val request = Request.Builder().url("$base/ws").header("Authorization", "Bearer $token").build()
        return ControlSession(http.newWebSocket(request, object : WebSocketListener() {
            override fun onOpen(webSocket: WebSocket, response: Response) = listener.onOpen(ControlSession(webSocket))
            override fun onMessage(webSocket: WebSocket, text: String) {
                val env = runCatching { ProtocolJson.decodeFromString(Envelope.serializer(), text) }.getOrNull() ?: return
                listener.onMessage(env)
            }
            override fun onClosed(webSocket: WebSocket, code: Int, reason: String) = listener.onClosed(reason)
            override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                // A rejected token will be rejected again; anything else may be a dropped link.
                val rejected = response?.code == 401
                val detail = if (rejected) context.getString(R.string.err_pairing_rejected) else t.message ?: t.toString()
                listener.onFailure(detail, retryable = !rejected)
            }
        }))
    }

    /** Callbacks of the control WebSocket, on OkHttp's thread. */
    interface Listener {
        /** The connection [session] is open and authenticated. */
        fun onOpen(session: ControlSession)

        /** A message arrived. */
        fun onMessage(envelope: Envelope)

        /** The host closed the connection. */
        fun onClosed(reason: String)

        /** The connection failed, with a message for the user; [retryable] when connecting again may help. */
        fun onFailure(message: String, retryable: Boolean)
    }

    companion object {
        private val JSON = "application/json".toMediaType()
        private val OCTET_STREAM = "application/octet-stream".toMediaType()
        private const val UPLOAD_CHUNK = 64 * 1024
        private const val UPLOAD_REPLY_TIMEOUT_S = 60L
    }
}

/**
 * The URL of `POST /files` on [host]:[port] for a file called [name]. OkHttp
 * percent-encodes the name, so spaces, `&`, `/`, `+` and non-ASCII letters
 * reach the host as they are.
 */
fun uploadUrl(host: String, port: Int, name: String): HttpUrl =
    HttpUrl.Builder().scheme("https").host(host).port(port).addPathSegment("files").addQueryParameter("name", name).build()

/** What the host's answer to `POST /files` says. */
enum class UploadAnswer {
    /** 200: the host saved the file. */
    SAVED,

    /** 401: the host did not accept this device's token. */
    REJECTED,

    /** 409: another file from this device is still being received. */
    BUSY,

    /** 413: the file is larger than the host takes. */
    TOO_LARGE,

    /** 507: the host's disk is full. */
    DISK_FULL,

    /** 404 or 405: the host is too old to receive files. */
    UNSUPPORTED,

    /** Any other status. */
    FAILED;

    companion object {
        /** The answer an HTTP status code gives. */
        fun of(code: Int): UploadAnswer = when (code) {
            200 -> SAVED
            401 -> REJECTED
            409 -> BUSY
            413 -> TOO_LARGE
            507 -> DISK_FULL
            404, 405 -> UNSUPPORTED
            else -> FAILED
        }
    }
}

/**
 * Whether [e] says the machine refused the connection: it is on, but nothing
 * listens on the port, so desklinkd is not running there. OkHttp wraps the
 * socket's ConnectException ("ECONNREFUSED" on Android, "Connection refused"
 * on a JVM), so the causes and suppressed exceptions are searched too. A
 * timeout or an unreachable host is no refusal: the machine may be asleep.
 */
fun isConnectionRefused(e: Throwable): Boolean {
    val seen = HashSet<Throwable>()
    val queue = ArrayDeque(listOf(e))
    while (queue.isNotEmpty()) {
        val t = queue.removeFirst()
        if (!seen.add(t)) continue
        val message = t.message.orEmpty()
        if (t is ConnectException && ("ECONNREFUSED" in message || "Connection refused" in message)) return true
        t.cause?.let(queue::add)
        queue.addAll(t.suppressed)
    }
    return false
}

/** What the host's answer to DELETE /pair says about its pairing with this device. */
enum class ForgetAnswer {
    /** 200: the host forgot this device. */
    REMOVED,

    /**
     * 401: the host no longer knows the device, or it rejected the token
     * (its clock moved, say); either way it may still hold the pairing.
     */
    UNCONFIRMED,

    /** 404 or 405: the host is too old to forget a device remotely. */
    UNSUPPORTED,

    /** Any other status. */
    FAILED;

    companion object {
        /** The answer an HTTP status code gives. */
        fun of(code: Int): ForgetAnswer = when (code) {
            200 -> REMOVED
            401 -> UNCONFIRMED
            404, 405 -> UNSUPPORTED
            else -> FAILED
        }
    }
}

/** One control WebSocket. */
class ControlSession(private val socket: WebSocket) {
    /** Sends one message. */
    fun send(type: String, payload: JsonElement) {
        socket.send(ProtocolJson.encodeToString(Envelope.serializer(), Envelope(type, payload)))
    }

    /** The bytes queued to be sent; OkHttp closes the socket when this would pass 16 MiB. */
    fun queued(): Long = socket.queueSize()

    /** Closes the connection normally; the host restores the desktop. */
    fun close() {
        socket.close(1000, "client disconnect")
    }
}

// Trusts exactly one certificate, the host's self-signed one, by its SHA-256 fingerprint.
@SuppressLint("CustomX509TrustManager")
private class PinningTrustManager(private val pinned: String?, private val mismatch: (String) -> String) : X509TrustManager {
    /** The fingerprint of the last certificate checked. */
    @Volatile
    var observed: String? = null

    override fun checkServerTrusted(chain: Array<out X509Certificate>, authType: String) {
        val fp = SessionCrypto.fingerprint(chain.firstOrNull()?.encoded ?: throw CertificateException("empty chain"))
        if (pinned != null && !pinned.equals(fp, ignoreCase = true)) {
            throw CertificateException(mismatch(fp))
        }
        observed = fp
    }

    override fun checkClientTrusted(chain: Array<out X509Certificate>, authType: String) =
        throw CertificateException("client certificates are not used")

    override fun getAcceptedIssuers(): Array<X509Certificate> = emptyArray()
}
