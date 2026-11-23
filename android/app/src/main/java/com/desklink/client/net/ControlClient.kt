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
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.serialization.json.JsonElement
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import java.io.IOException
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
 * only that certificate. Without one (manual pairing) it trusts the first
 * certificate it sees and binds the pairing keys to it, so a substituted
 * certificate makes the PIN proof fail.
 */
class ControlClient(
    private val context: Context,
    private val host: String,
    private val port: Int,
    pinnedFingerprint: String?,
) {
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

    /** The certificate fingerprint seen on the last connection. */
    val observedFingerprint: String? get() = trust.observed

    /** Calls GET /health. */
    suspend fun health(): Health = withContext(Dispatchers.IO) {
        http.newCall(Request.Builder().url("$base/health").build()).execute().use { resp ->
            if (!resp.isSuccessful) throw IOException(context.getString(R.string.err_http, resp.code))
            ProtocolJson.decodeFromString(Health.serializer(), resp.body.string())
        }
    }

    /** Runs the two-step ECDH + PIN pairing against POST /pair. */
    suspend fun pair(pin: String, deviceName: String): PairResult = withContext(Dispatchers.IO) {
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

    /** Opens the control WebSocket with a bearer token in host time. */
    fun openSession(token: String, listener: Listener): ControlSession {
        val request = Request.Builder().url("$base/ws").header("Authorization", "Bearer $token").build()
        val session = ControlSession()
        session.socket = http.newWebSocket(request, object : WebSocketListener() {
            override fun onOpen(webSocket: WebSocket, response: Response) = listener.onOpen()
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
        })
        return session
    }

    /** Callbacks of the control WebSocket, on OkHttp's thread. */
    interface Listener {
        /** The connection is open and authenticated. */
        fun onOpen()

        /** A message arrived. */
        fun onMessage(envelope: Envelope)

        /** The host closed the connection. */
        fun onClosed(reason: String)

        /** The connection failed, with a message for the user; [retryable] when connecting again may help. */
        fun onFailure(message: String, retryable: Boolean)
    }

    companion object {
        private val JSON = "application/json".toMediaType()
    }
}

/** The open control WebSocket. */
class ControlSession {
    internal lateinit var socket: WebSocket

    /** Sends one message. */
    fun send(type: String, payload: JsonElement) {
        socket.send(ProtocolJson.encodeToString(Envelope.serializer(), Envelope(type, payload)))
    }

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
