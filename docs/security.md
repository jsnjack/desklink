# Security model

DeskLink gives a paired tablet full control of the desktop session, including
unlocking it. The design assumes the tablet and its Android lock screen are
trusted, and the network between office and home may not be.

## What protects what

- **Reachability.** Both listeners bind IPv4 and close or drop traffic from
  outside `allowed_subnets` (RFC 1918 and loopback by default) before any TLS
  handshake or decryption. The FRITZ!Box should not forward ports 9443/9444;
  the WireGuard VPN is the way in. Inside those subnets, a client that stops
  sending or reading cannot tie up the control server: every request must
  arrive, body included, within 10 s, and its response must be written
  within 10 s (a WebSocket is exempt from both once upgraded); `/pair`
  answers without reading the body while no PIN is valid; at most 8 pairing
  requests run at once, while `/health`, which reads no body, is not
  counted, so trickled pairing requests cannot keep paired clients from
  finding the host; and one address may hold at most 16 connections open,
  more are closed before TLS.
- **Pairing.** A pairing needs the desk's pairing code, which
  `desklinkd pair` shows as a QR code (the tablet scans it) and as a line of
  JSON (pasted into the laptop client). The code carries the address, a PIN
  valid for five minutes, used once and cancelled after five wrong proofs,
  and the certificate fingerprint, which the client pins for the whole
  exchange, so only the desk that showed the code can take part. The host
  answers nothing that depends on the PIN until the client has proven it,
  so a client gets one guess per attempt (5 in 10⁶). The clients refuse to
  pair with a PIN alone or with a fingerprint taken from an mDNS
  announcement: the PIN only feeds a key derivation that the other side can
  recompute, so whoever answered such a pairing in the desk's place (anyone
  on the network can announce any name and fingerprint) would get the
  client's proof, could test all 10⁶ PINs offline, and could then pair with
  the real desk while the PIN is still valid.
- **Sessions.** The WebSocket requires a bearer token that only the paired
  peer key can produce, valid for five minutes around the host's clock and
  carried inside the pinned TLS connection. Media keys are fresh per session
  (random host nonce), separate per direction, and every datagram is
  authenticated, including its header. Client datagrams are accepted only from
  the WebSocket's IP and only once (replay window). A session whose client
  has sent no authenticated datagram for 10 s ends, so a lost UDP path does
  not keep the session, and a desk it unlocked, open.
- **Ending a session.** However a session ends (the client leaves, a new
  connection replaces it, its device is unpaired, its UDP path is lost), the
  client loses control before anything else is undone: from that moment the
  host drops its input and control messages, even ones already queued;
  `session.cleanup` then closes its UDP session, which waits for input being
  injected and sends nothing more, and lifts every held key, button and pen
  contact. Only then does it restore the monitors and the sound and lock the
  desk again, which can take several seconds. Unlocking at the start runs to
  the end even if the client leaves meanwhile, and a desk that was locked
  counts as locked even when the unlock call fails, since a reply lost on
  the way may hide an unlock that worked; so cleanup locks the desk again
  whenever the session may have unlocked it.
- **The desk during a session.** A session unlocks the desk. If the desk was
  locked as the client connected and the client gets its own screen, the
  session turns the desk's monitors off until it ends, so a passer-by sees
  dark screens instead of the unlocked desktop. This only hides the screen:
  the desk's own keyboard and mouse still work and act on the unlocked
  session, and plugging in a monitor can bring the screens back. If the desk
  was unlocked as the client connected, or the client mirrors a monitor, the
  desk's monitors stay on and show the session. When the session ends, the
  desk locks again before its monitors come back on, so they show the lock
  screen. A session that hands over to a new connection (the tablet roamed)
  does not lock; the new session turns the monitors off again as it starts.
- **Screen capture.** desklinkd records the screen through Mutter's own API,
  which GNOME allows any program in the user's session to call; there is no
  per-connection sharing dialog. A paired device can therefore see the screen
  with nobody at the desk, which is the point, and pairing is the only gate.
  GNOME shows its screen-sharing indicator in the top bar while a session runs.
- **Automatic login.** With `desklinkd autologin on`, GDM logs the user in at
  boot and whenever its login screen has been idle for 10 s (timed login),
  for example after a logout. Each time, the desktop is open until desklinkd
  locks it, normally within a few seconds, and anyone at the computer during
  that window gets the session. Logging out therefore does not keep the desk
  closed: the login screen logs the user back in unless someone types there. The login keyring stays locked
  until the user unlocks the desk with the password once.
- **Actions.** The client can inject input, read and write the clipboard, and
  launch apps from the `.desktop` list; it cannot run arbitrary commands.

## Files

| File | Content | Mode |
|---|---|---|
| `~/.config/desklink/tls.key` | certificate private key | 0600 |
| `~/.config/desklink/peers.json` | peer IDs and 256-bit peer keys | 0600 |
| `~/.config/desklink/state.json` | saved monitor layout | 0600 |
| `$XDG_RUNTIME_DIR/desklinkd.sock` | local pairing API | 0600 in a 0700 directory |

On Android the peer key is stored encrypted with a Keystore AES-GCM key, and
backups exclude the app's data.

To revoke a device, run `desklinkd peers` to list the paired devices (ID,
name, when paired, when last connected) and `desklinkd peers remove
<ID, ID start or name>`: the device can no longer connect, and a session it
has running ends: its input and control messages stop at once (see
**Ending a session**), and the desk is locked again if the session unlocked
it. Nothing the device asked for outlasts the unpairing: if it asked to keep
the computer awake after its session (`keep_awake`), that ends too, even
when the session ended long before, and a session that the unpairing ends
does not start keeping it awake. Unpairing and admitting a session share one lock
(`daemon/sessioncontrol.go`), and admission checks the device is still paired
with the same key, so a connection that authenticated just before the device
was unpaired is refused instead of starting. If saving `peers.json` fails,
the daemon still refuses the device and ends its session, but the command
fails with an error saying the removal was not saved, and `desklinkd peers`
marks the device until running the command again succeeds; any later
successful save (a new pairing, a session start) also completes it. A daemon
that stops with such a removal unsaved tries once more and otherwise logs an
error, because after a restart `peers.json` would let the device connect
again. The command talks to the daemon over the local
socket, so only the desktop user can use it. A client's **Forget** asks the
host to do the same for that client: `DELETE /pair` with the client's bearer
token removes only the client's own pairing.
