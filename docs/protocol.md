# Wire protocol

`host/internal/net/packet.go` and `host/internal/net/control.go` are the
reference; `android/.../model/Protocol.kt` mirrors them.

## Control plane: TLS 1.3 on port 9443

The host generates a self-signed ECDSA P-256 certificate on first start
(`~/.config/desklink/tls.crt`, `tls.key`). Clients pin it by the lowercase hex
SHA-256 of its DER encoding.

| Endpoint | Auth | Purpose |
|---|---|---|
| `GET /health` | none | `{"name","version","hostname","time_ms","mac"}`; the client stamps tokens with `time_ms` and stores `mac` for Wake-on-LAN |
| `POST /pair` | PIN proof | two-phase pairing, below |
| `GET /ws` | `Authorization: Bearer <token>` | the session WebSocket |

The bearer token is `<peer_id>.<unix seconds>.<hex HMAC-SHA256(auth_key, "<peer_id>.<seconds>")>`
with `auth_key = HKDF(peer_key, salt=nil, info="desklink auth v1")`. The host
accepts timestamps within five minutes of its clock; the client uses the host
time from `/health`, so tablet clock drift does not matter.

### WebSocket messages

Every message is `{"type": "...", "payload": ...}`.

| Type | Direction | Payload |
|---|---|---|
| `hello` | client → host, first message | `{"codec":"av1"\|"hevc","width","height","fps","qp","auto","start","audio"}`; with `auto` the size and rate are the top of the quality ladder and `start` (optional, `{"width","height","fps"}`) the level last used on this network; `audio` (optional) asks for the desktop's sound |
| `session` | host → client | `{"session_id","udp_port","host_nonce","codec","width","height","fps","qp","display","auto","level","levels","audio"}`; `levels` is the ladder as `[{"width","height","fps"}]`; `audio` is true when audio packets follow; resent without `host_nonce` whenever the size, rate or level changes |
| `apps` | host → client | `[{"id","name","exec"}]`, pinned apps first |
| `launch` | client → host | `{"id","exec"}`; must match an entry of `apps` |
| `clipboard` | both | `{"text"}`, at most 256 KB, checked before sending; both ends accept messages up to 2 MiB, which fits 256 KB even when JSON escapes every character |
| `clipboard_request` | client → host | `{}`; the host replies with `clipboard` |
| `quality` | client → host | `{"width","height","qp"}` |
| `req_idr` | client → host | `{}` |
| `error` | host → client | `{"message"}`; before `session` it means the session failed |

The host picks HEVC when the client asks for it or when `vaav1enc` is missing;
`session.codec` is authoritative. A new authenticated connection replaces the
running session.

## Pairing

`desklinkd pair` asks the running daemon (over `$XDG_RUNTIME_DIR/desklinkd.sock`)
for a PIN valid for five minutes and prints
`{"ip","port","pin","fingerprint"}` as JSON and as a QR code.

1. `POST /pair {"phase":"start","client_pub":<65-byte uncompressed P-256>}` →
   `{"pairing_id","host_pub"}`. The host creates an ephemeral key per attempt.
2. Both sides compute `shared = ECDH(...)` and
   `okm = HKDF-SHA256(shared, salt = SHA-256("desklink pair pin v1:" + PIN),
   info = "desklink pair v1|hex(client_pub)|hex(host_pub)|fingerprint", 64)`.
   `peer_key = okm[0:32]`, `confirm = okm[32:64]`.
3. `POST /pair {"phase":"finish","pairing_id","proof":HMAC(confirm,"client"),"name"}`
   → `{"peer_id","proof":HMAC(confirm,"host")}`. The client checks the host
   proof.

The host reveals nothing that depends on the PIN until the client has proven
it, so the PIN cannot be guessed offline from host replies. Five wrong proofs
cancel the PIN; a correct one consumes it. Binary fields are standard padded
base64, as Go encodes `[]byte`.

## Media plane: UDP on port 9444

Every datagram is a 16-byte cleartext header, used as AEAD additional data,
followed by a ChaCha20-Poly1305 ciphertext and its 16-byte tag. Datagrams
never exceed 1280 bytes, so the plaintext is at most 1248 bytes.

| Bytes | Field |
|---|---|
| 0–1 | magic `0x444C` ("DL") |
| 2 | version `0x01` |
| 3 | type: 1 video fragment, 2 input, 3 telemetry, 4 keepalive, 5 audio |
| 4–7 | session ID (uint32) |
| 8–11 | sequence number (uint32), per direction, starting at 1 |
| 12–13 | frame ID (uint16); for audio, the audio packet counter |
| 14 | fragment index, low byte; for keepalives 1 marks an echo |
| 15 | fragment count, low byte |

The nonce is four zero bytes followed by header bytes 4–11. Each session uses
two keys, so both directions can count from 1 without reusing a nonce:
`HKDF(peer_key, salt = host_nonce, info = "desklink media v1|%08x" of the
session ID, 64)` gives host-to-client (first 32 bytes) and client-to-host. The
host ends a session before its sequence number would wrap. The host drops
replayed client datagrams (1024-entry window) and datagrams from any address
other than the WebSocket's. It sends from the address and interface the
client's datagrams arrive on (`IP_PKTINFO`): a host with two interfaces on one
network, such as a dock's ethernet and Wi-Fi, would otherwise answer from the
interface routing prefers, and clients drop datagrams from an address they
did not send to.

**Video fragment (host → client).** The plaintext starts with a 12-byte
prefix: flags (bit 0 keyframe), fragment index high byte, fragment count high
byte, reserved, capture time in Unix microseconds (uint64). The rest is frame
data, at most 1236 bytes. The extra high bytes exist because a 2560×1600
keyframe can need more than 255 fragments. A frame is an AV1 temporal unit
(OBU stream) or an HEVC access unit (Annex B, parameter sets on keyframes).

**Audio (host → client).** Sent only when `hello.audio` was set. One Opus
frame per datagram: capture time in Unix microseconds (uint64), then the
frame: 48 kHz stereo, 10 ms, about 128 kbit/s (some 160 bytes), encoded with
`audio-type=restricted-lowdelay` (2.5 ms lookahead). The header's frame ID
counts audio packets (wrapping at 16 bits) so clients drop late and repeated
ones; there is no Opus header packet, clients configure the decoder for
channel mapping family 0.

**Input (client → host).** One or more 12-byte records:
`kind u8, flags u8, code u16, x i32, y i32`.

| Kind | Meaning |
|---|---|
| 1 key | `code` = Linux `KEY_*`, flags 1 down / 0 up |
| 2 relative move | `x`, `y` in pixels |
| 3 pen | `x`, `y` in 0..65535; flags 0 hover, 1 touching, 2 left proximity |
| 4 mouse button | `code` 1 left, 2 right, 3 middle, 4 side, 5 extra; flags 1 down |
| 5 scroll | `x` horizontal, `y` vertical, in 1/120 of a notch (positive = right, up) |
| 6 absolute pointer | `x`, `y` in 0..65535 over the video frame; the laptop client's mouse and the tablet's tap-to-click touch |
| 7 held | one record per item the client holds: flags 1 key (`code` = `KEY_*`), 2 mouse button (`code` 1–5), 3 pen (`code` bit 0 touching, bit 1 in range); a single record with flags 0 means nothing is held |

A packet of kind-7 records is a complete snapshot, sent every 250 ms after the
input queued before it. The host lifts every key, button or pen contact that
the snapshot does not list, unless a packet with a higher sequence number
changed it. This repairs a lost key-up packet within a quarter of a second
instead of leaving the key repeating; a snapshot never presses anything.
Clients keep each key's press and release in one packet and split longer
input across packets of at most 104 records.

**Telemetry (client → host, every 250 ms).** `last_contiguous_frame_id u16,
loss_pct_x100 u16, need_idr u8`. Loss counts gaps in the host's sequence
numbers since the previous report.

**Keepalive (both, every 2 s).** `timestamp_micros u64`. The receiver echoes
it back with fragment index 1, and the sender computes the round-trip time.
