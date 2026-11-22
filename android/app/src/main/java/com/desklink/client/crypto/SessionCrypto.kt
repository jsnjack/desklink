package com.desklink.client.crypto

import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import com.desklink.client.model.Header
import com.desklink.client.model.Wire
import java.math.BigInteger
import java.nio.ByteBuffer
import java.security.KeyFactory
import java.security.KeyPairGenerator
import java.security.KeyStore
import java.security.MessageDigest
import java.security.interfaces.ECPublicKey
import java.security.spec.ECGenParameterSpec
import java.security.spec.ECPoint
import java.security.spec.ECPublicKeySpec
import javax.crypto.Cipher
import javax.crypto.KeyAgreement
import javax.crypto.KeyGenerator
import javax.crypto.Mac
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.IvParameterSpec
import javax.crypto.spec.SecretKeySpec

/**
 * Pairing, bearer tokens and media keys. Every label and encoding mirrors
 * host/internal/crypto/session.go; SessionCryptoTest checks shared vectors.
 */
object SessionCrypto {
    /** The length of every symmetric key. */
    const val KEY_SIZE = 32
    private const val LABEL_PAIR = "desklink pair v1"
    private const val LABEL_PIN_SALT = "desklink pair pin v1:"
    private const val LABEL_AUTH = "desklink auth v1"
    private const val LABEL_MEDIA = "desklink media v1"
    private const val HMAC = "HmacSHA256"

    /** SHA-256 of [data]. */
    fun sha256(data: ByteArray): ByteArray = MessageDigest.getInstance("SHA-256").digest(data)

    /** Lowercase hex of [b]. */
    fun hex(b: ByteArray): String = b.joinToString("") { "%02x".format(it.toInt() and 0xFF) }

    /** Lowercase hex SHA-256 of a DER certificate, as printed by `desklinkd pair`. */
    fun fingerprint(certDer: ByteArray): String = hex(sha256(certDer))

    /** HMAC-SHA256 of [msg] under [key]. */
    fun hmac(key: ByteArray, msg: ByteArray): ByteArray =
        Mac.getInstance(HMAC).run {
            init(SecretKeySpec(key, HMAC))
            doFinal(msg)
        }

    /** RFC 5869 HKDF-SHA256. A missing salt is a block of zeros, as in Go. */
    fun hkdf(ikm: ByteArray, salt: ByteArray?, info: ByteArray, length: Int): ByteArray {
        val prk = hmac(if (salt == null || salt.isEmpty()) ByteArray(KEY_SIZE) else salt, ikm)
        val out = ByteArray(length)
        var t = ByteArray(0)
        var pos = 0
        var counter = 1
        while (pos < length) {
            t = hmac(prk, t + info + byteArrayOf(counter.toByte()))
            val n = minOf(t.size, length - pos)
            System.arraycopy(t, 0, out, pos, n)
            pos += n
            counter++
        }
        return out
    }

    /** The keys from one pairing: the long-term [peerKey] and the PIN proofs. */
    class PairingKeys(val peerKey: ByteArray, private val confirm: ByteArray) {
        /** The MAC proving to the host that the client knows the PIN. */
        fun clientProof(): ByteArray = hmac(confirm, "client".toByteArray())

        /** The MAC the host must return to prove it knows the PIN. */
        fun hostProof(): ByteArray = hmac(confirm, "host".toByteArray())
    }

    /** Binds the ECDH secret to the PIN, both public keys and the certificate fingerprint. */
    fun derivePairingKeys(shared: ByteArray, pin: String, clientPub: ByteArray, hostPub: ByteArray, fingerprint: String): PairingKeys {
        val salt = sha256((LABEL_PIN_SALT + pin).toByteArray())
        val info = listOf(LABEL_PAIR, hex(clientPub), hex(hostPub), fingerprint).joinToString("|")
        val okm = hkdf(shared, salt, info.toByteArray(), 2 * KEY_SIZE)
        return PairingKeys(okm.copyOfRange(0, KEY_SIZE), okm.copyOfRange(KEY_SIZE, 2 * KEY_SIZE))
    }

    /** "<peer_id>.<unix seconds>.<hex HMAC>", stamped with host time from /health. */
    fun authToken(peerId: String, peerKey: ByteArray, unixSeconds: Long): String {
        val key = hkdf(peerKey, null, LABEL_AUTH.toByteArray(), KEY_SIZE)
        val signed = "$peerId.$unixSeconds"
        return "$signed.${hex(hmac(key, signed.toByteArray()))}"
    }

    /** The two directional media keys of one session. */
    class SessionKeys(val hostToClient: ByteArray, val clientToHost: ByteArray)

    /** Derives the media keys from the peer key, the session ID and the host's nonce. */
    fun deriveSessionKeys(peerKey: ByteArray, sessionId: Long, hostNonce: ByteArray): SessionKeys {
        val info = "%s|%08x".format(LABEL_MEDIA, sessionId and 0xFFFFFFFFL)
        val okm = hkdf(peerKey, hostNonce, info.toByteArray(), 2 * KEY_SIZE)
        return SessionKeys(okm.copyOfRange(0, KEY_SIZE), okm.copyOfRange(KEY_SIZE, 2 * KEY_SIZE))
    }
}

/** An ephemeral P-256 key for one pairing attempt. */
class EphemeralKey {
    private val pair = KeyPairGenerator.getInstance("EC").apply {
        initialize(ECGenParameterSpec("secp256r1"))
    }.generateKeyPair()

    /** The uncompressed SEC 1 point (0x04 || X || Y) that Go's crypto/ecdh uses. */
    fun publicBytes(): ByteArray {
        val w = (pair.public as ECPublicKey).w
        return byteArrayOf(4) + fixed(w.affineX) + fixed(w.affineY)
    }

    /** The ECDH shared secret (the X coordinate) with the host's public key. */
    fun agree(hostPub: ByteArray): ByteArray {
        require(hostPub.size == 65 && hostPub[0].toInt() == 4) { "host key is not an uncompressed P-256 point" }
        val params = (pair.public as ECPublicKey).params
        val point = ECPoint(BigInteger(1, hostPub.copyOfRange(1, 33)), BigInteger(1, hostPub.copyOfRange(33, 65)))
        val hostKey = KeyFactory.getInstance("EC").generatePublic(ECPublicKeySpec(point, params))
        return KeyAgreement.getInstance("ECDH").run {
            init(pair.private)
            doPhase(hostKey, true)
            generateSecret()
        }
    }

    private fun fixed(v: BigInteger): ByteArray {
        val raw = v.toByteArray()
        return when {
            raw.size == 32 -> raw
            raw.size > 32 -> raw.copyOfRange(raw.size - 32, raw.size)
            else -> ByteArray(32 - raw.size) + raw
        }
    }
}

/**
 * ChaCha20-Poly1305 over the media datagrams. The nonce is the header's
 * session ID and sequence number in the last eight of twelve bytes.
 */
class PacketCipher(key: ByteArray) {
    private val keySpec = SecretKeySpec(key, "ChaCha20")
    private val cipher: Cipher = try {
        Cipher.getInstance("ChaCha20/Poly1305/NoPadding")
    } catch (e: java.security.GeneralSecurityException) {
        Cipher.getInstance("ChaCha20-Poly1305")
    }

    private fun nonce(sessionId: Int, seq: Int): ByteArray =
        ByteBuffer.allocate(12).putInt(0).putInt(sessionId).putInt(seq).array()

    /** Encrypts [payload] with [header] as additional data and returns the datagram. */
    @Synchronized
    fun seal(header: Header, payload: ByteArray): ByteArray {
        require(payload.size <= Wire.MAX_PLAINTEXT) { "payload of ${payload.size} bytes exceeds ${Wire.MAX_PLAINTEXT}" }
        val out = ByteArray(Wire.HEADER_SIZE + payload.size + Wire.TAG_SIZE)
        header.write(out)
        cipher.init(Cipher.ENCRYPT_MODE, keySpec, IvParameterSpec(nonce(header.sessionId, header.seq)))
        cipher.updateAAD(out, 0, Wire.HEADER_SIZE)
        cipher.doFinal(payload, 0, payload.size, out, Wire.HEADER_SIZE)
        return out
    }

    /** Returns the header and plaintext, or null if the datagram is malformed or forged. */
    @Synchronized
    fun open(datagram: ByteArray, length: Int): Pair<Header, ByteArray>? {
        if (length > Wire.MAX_DATAGRAM || length < Wire.HEADER_SIZE + Wire.TAG_SIZE) return null
        val header = Header.parse(datagram, length) ?: return null
        return try {
            cipher.init(Cipher.DECRYPT_MODE, keySpec, IvParameterSpec(nonce(header.sessionId, header.seq)))
            cipher.updateAAD(datagram, 0, Wire.HEADER_SIZE)
            header to cipher.doFinal(datagram, Wire.HEADER_SIZE, length - Wire.HEADER_SIZE)
        } catch (e: java.security.GeneralSecurityException) {
            null
        }
    }
}

/**
 * Wraps peer keys with an AES-GCM key that never leaves the Android
 * Keystore, so the stored profile holds only ciphertext.
 */
object KeystoreVault {
    private const val ALIAS = "desklink-peer-wrap"
    private const val PROVIDER = "AndroidKeyStore"
    private const val TRANSFORM = "AES/GCM/NoPadding"
    private const val IV_SIZE = 12
    private const val TAG_BITS = 128

    private fun key(): SecretKey {
        val ks = KeyStore.getInstance(PROVIDER).apply { load(null) }
        (ks.getKey(ALIAS, null) as? SecretKey)?.let { return it }
        return KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, PROVIDER).run {
            init(
                KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                    .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                    .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                    .setKeySize(256)
                    .build()
            )
            generateKey()
        }
    }

    /** Encrypts [secret]; the result is the IV followed by the ciphertext. */
    fun wrap(secret: ByteArray): ByteArray {
        val c = Cipher.getInstance(TRANSFORM).apply { init(Cipher.ENCRYPT_MODE, key()) }
        return c.iv + c.doFinal(secret)
    }

    /** Decrypts a blob from [wrap]. */
    fun unwrap(blob: ByteArray): ByteArray {
        val c = Cipher.getInstance(TRANSFORM)
        c.init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(TAG_BITS, blob, 0, IV_SIZE))
        return c.doFinal(blob, IV_SIZE, blob.size - IV_SIZE)
    }
}
