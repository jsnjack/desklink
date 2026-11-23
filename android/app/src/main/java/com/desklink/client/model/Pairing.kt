package com.desklink.client.model

/**
 * A host announcing itself on the local network that is not paired yet. The
 * announcement is unauthenticated: anyone on the network can send one, with
 * any name and fingerprint, so it only offers a host to pair by QR code.
 */
data class NearbyHost(val name: String, val ip: String, val port: Int, val fingerprint: String)

/** What a scanned QR code holds. */
sealed interface PairingCode {
    /** A pairing code from `desklinkd pair`, its fingerprint lowercase. */
    data class Valid(val qr: PairingQr) : PairingCode

    /**
     * A pairing code without a well-formed certificate fingerprint. Pairing
     * with it would trust whoever answers, and that one could then try every
     * PIN offline, so the app refuses it.
     */
    data object NoFingerprint : PairingCode

    /** Not a DeskLink pairing code. */
    data object Invalid : PairingCode

    companion object {
        private val HEX_SHA256 = Regex("[0-9a-fA-F]{64}")

        /** Reads and checks the text of a scanned QR code. */
        fun parse(raw: String): PairingCode {
            val qr = runCatching { ProtocolJson.decodeFromString(PairingQr.serializer(), raw) }.getOrNull()
                ?: return Invalid
            if (qr.ip.isBlank() || !usableAddress(qr.ip) || qr.port !in 1..65535 || qr.pin.isBlank()) return Invalid
            if (!HEX_SHA256.matches(qr.fingerprint)) return NoFingerprint
            return Valid(qr.copy(ip = qr.ip.trim(), pin = qr.pin.trim(), fingerprint = qr.fingerprint.lowercase()))
        }
    }
}

/**
 * Where a scanned code's pairing goes. [profile] is the host to pair, at the
 * code's address and port with the code's other addresses; it is new and not
 * saved yet when [isNew]. Pairing always pins [fingerprint], the code's.
 * [announcedDiffers] is set when the host found on the network announced
 * another certificate, [savedDiffers] when the chosen saved host had another
 * one pinned: the app tells the user that the code's certificate wins.
 */
data class PairingPlan(
    val profile: HostProfile,
    val isNew: Boolean,
    val fingerprint: String,
    val announcedDiffers: Boolean,
    val savedDiffers: Boolean,
)

/**
 * Decides which host a scanned, valid code pairs. The saved host with the
 * code's certificate comes first (the same machine on any network), then the
 * host whose Pair button the user tapped ([chosenId]), then a saved host at
 * the code's address and port, and last a new host, named after the
 * announcement the user picked ([announced]) when it announced the code's
 * certificate, or else after the address.
 */
fun planPairing(
    profiles: List<HostProfile>,
    qr: PairingQr,
    chosenId: String? = null,
    announced: NearbyHost? = null,
    newId: () -> String,
): PairingPlan {
    val sameMachine = profiles.firstOrNull { it.fingerprint.equals(qr.fingerprint, ignoreCase = true) }
    val existing = sameMachine
        ?: profiles.firstOrNull { it.id == chosenId }
        ?: profiles.firstOrNull { it.ip == qr.ip && it.port == qr.port }
    val announcedDiffers = announced != null && !announced.fingerprint.equals(qr.fingerprint, ignoreCase = true)
    val name = announced?.takeUnless { announcedDiffers }?.name?.removePrefix("DeskLink on ")?.ifBlank { null } ?: qr.ip
    val base = existing ?: HostProfile(newId(), name, qr.ip, qr.port)
    return PairingPlan(
        profile = base.copy(port = qr.port).withAddresses(qr.ip, qr.addresses),
        isNew = existing == null,
        fingerprint = qr.fingerprint,
        announcedDiffers = announcedDiffers,
        savedDiffers = existing?.fingerprint != null && !existing.fingerprint.equals(qr.fingerprint, ignoreCase = true),
    )
}

/**
 * Whether [addr] can be a host's address. IP literals that cannot reach one
 * machine are refused: unspecified (0.0.0.0/8, ::), loopback, multicast, the
 * limited broadcast and the rest of 240.0.0.0/4. Names pass; they resolve
 * when used.
 */
fun usableAddress(addr: String): Boolean {
    val a = addr.trim().removePrefix("[").removeSuffix("]").lowercase()
    if (a.isEmpty()) return false
    val octets = a.split('.')
    if (octets.size == 4 && octets.all { o -> o.isNotEmpty() && o.length <= 3 && o.all(Char::isDigit) }) {
        val v = octets.map { it.toInt() }
        if (v.any { it > 255 }) return false
        return v[0] != 0 && v[0] != 127 && v[0] < 224
    }
    if (':' in a) {
        val bare = a.substringBefore('%')
        return bare.trimStart('0', ':').isNotEmpty() && bare != "::1" && !bare.startsWith("ff")
    }
    return true
}
