package com.desklink.client.ui

import com.desklink.client.model.HostProfile

/** The host the main page shows the details of: the one chosen last while it is still saved, else the first. */
fun selectedHost(hosts: List<HostProfile>, chosenId: String?): HostProfile? =
    hosts.firstOrNull { it.id == chosenId } ?: hosts.firstOrNull()

/** A host's address as the main page shows it, with the port only when it is not the default one. */
fun shownAddress(p: HostProfile): String = when {
    p.port == HostProfile.DEFAULT_PORT -> p.ip
    ':' in p.ip -> "[${p.ip}]:${p.port}"
    else -> "${p.ip}:${p.port}"
}

/**
 * The computer's own name for the status line, or null when it is the name
 * the host is saved under, so the line does not repeat the title.
 */
fun reportedName(savedName: String, hostname: String): String? =
    hostname.takeIf { it.isNotBlank() && !it.equals(savedName.trim(), ignoreCase = true) }

/** Whether the main page offers Wake beside Connect: the host did not answer and its MAC is known. */
fun offersWake(p: HostProfile, health: HostHealth?): Boolean = p.mac != null && health is HostHealth.Offline
