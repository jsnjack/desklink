# Security model

DeskLink gives a paired tablet full control of the desktop session, including
unlocking it. The design assumes the tablet and its Android lock screen are
trusted, and the network between office and home may not be.

## What protects what

- **Reachability.** Both listeners bind IPv4 and close or drop traffic from
  outside `allowed_subnets` (RFC 1918 and loopback by default) before any TLS
  handshake or decryption. The FRITZ!Box should not forward ports 9443/9444;
  the WireGuard VPN is the way in. Inside those subnets, a client that stops
  sending cannot tie up the control server: every request must arrive,
  body included, within 10 s, `/pair` answers without reading the body
  while no PIN is valid, and at most 8 requests without a token run at once.
- **Pairing.** A pairing needs the PIN shown on the desktop, valid for five
  minutes, used once, and cancelled after five wrong proofs. The host only
  answers PIN-dependent values after the client has proven the PIN, so a
  network attacker gets one guess per attempt (5 in 10⁶). The QR code also
  carries the certificate fingerprint, which the client pins, so pairing by
  QR is safe against an active attacker in the path.
- **Manual PIN entry** trusts the first certificate the app sees. An attacker
  who can intercept the VPN traffic during that pairing could relay the
  exchange and then brute-force the PIN offline from the client's proof. The
  app shows the fingerprint after pairing so it can be compared with the one
  `desklinkd pair` prints; prefer the QR code.
- **Sessions.** The WebSocket requires a bearer token that only the paired
  peer key can produce, valid for five minutes around the host's clock and
  carried inside the pinned TLS connection. Media keys are fresh per session
  (random host nonce), separate per direction, and every datagram is
  authenticated, including its header. Client datagrams are accepted only from
  the WebSocket's IP and only once (replay window). A session whose client
  has sent no authenticated datagram for 10 s ends, so a lost UDP path does
  not keep the session, and a desk it unlocked, open.
- **Screen capture.** desklinkd records the screen through Mutter's own API,
  which GNOME allows any program in the user's session to call; there is no
  per-connection sharing dialog. A paired device can therefore see the screen
  with nobody at the desk, which is the point, and pairing is the only gate.
  GNOME shows its screen-sharing indicator in the top bar while a session runs.
- **Automatic login.** With GDM automatic login the desktop is open from boot
  until desklinkd locks it, normally within a few seconds. Anyone at the
  computer during that window gets the session. The login keyring stays locked
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
has running ends at once. Unpairing and admitting a session share one lock
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
