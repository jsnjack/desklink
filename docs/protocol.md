# Wire protocol

`host/internal/net/packet.go` and `host/internal/net/control.go` are the
reference; `android/.../model/Protocol.kt` mirrors them.

## Control plane: TLS 1.3 on port 9443

The host generates a self-signed ECDSA P-256 certificate on first start
(`~/.config/desklink/tls.crt`, `tls.key`). Clients pin it by the lowercase hex
SHA-256 of its DER encoding.

| Endpoint | Auth | Purpose |
|---|---|---|
| `GET /health` | none | `{"name","version","hostname","time_ms","mac","addresses"}`; the client stamps tokens with `time_ms`, stores `mac` for Wake-on-LAN and merges `addresses` (every address the host may be reached on) into the host |
| `POST /pair` | PIN proof | two-phase pairing, below |
| `DELETE /pair` | bearer token | the host forgets the requesting device's pairing and ends its session; 401 if the token is not accepted, which also happens when the host's clock jumped between `/health` and this request, so clients treat 401 as unconfirmed, not as already removed; 500 if the host could not save the removal, although the device can no longer connect |
| `GET /ws` | `Authorization: Bearer <token>` | the session WebSocket |
| `POST /files?name=<file name>` | bearer token | a file for the desk's download folder, below; no session needed |

The host reads each request, body included, within 10 s, and writes its
response within 10 s (`POST /files` is the exception, below); a client that stops reading loses the connection. It
serves at most 8 `/pair` requests at a time; more get 503. `/health` is not
counted. While no PIN is valid, `/pair` answers at once (403, or 429 after
too many wrong PINs) without waiting for the body. An upgraded WebSocket has
no read or write timeout. One address may hold at most 16 connections open;
the host closes more right after accepting them, before TLS.

The bearer token is `<peer_id>.<unix seconds>.<hex HMAC-SHA256(auth_key, "<peer_id>.<seconds>")>`
with `auth_key = HKDF(peer_key, salt=nil, info="desklink auth v1")`. The host
accepts timestamps within five minutes of its clock; the client uses the host
time from `/health`, so tablet clock drift does not matter.

### Sending a file: `POST /files`

The query parameter `name` is the file's name (UTF-8, URL-encoded) and the
body is the file itself, `Content-Type: application/octet-stream`, with a
`Content-Length` when the client knows it and chunked otherwise. Clients send
`Expect: 100-continue`: the host answers a file it refuses before reading any
of it and then closes the connection, so a body already on its way would turn
that answer into a connection reset; the host's go (`100 Continue`) comes when
it starts reading. The host (`net/upload.go`):

- checks the token as for `/ws` (401, plain text, otherwise);
- takes one file per device at a time: a second one while the first is being
  received gets 409;
- refuses a file larger than `max_upload_bytes` (default 2 GiB; 0 refuses
  every file with 403), by its `Content-Length` before reading, or with 413
  as soon as more arrives;
- writes the body to disk as it arrives, under a hidden temporary name in the
  download folder (`.desklink-upload-*.part`, mode 0600), and gives the body
  as long as it keeps coming: 30 s without a byte ends the upload, whatever
  the 10 s request timeout says. The reply then has its own 10 s;
- cleans the name (`net.SanitizeFileName`: path separators become `_`,
  control and invisible formatting characters go, other spaces become plain
  spaces, leading dots and spaces go, at most 200 bytes keeping the
  extension, `file` when nothing is left) and moves the file to that name,
  or to `name (1).ext`, `name (2).ext`… when it is taken; it never replaces a
  file;
- removes the partial file when the body breaks off, stalls or exceeds the
  limit.

Every answer closes the connection and is JSON except the 401:
`{"name","size"}` with 200 (`name` is what the file was saved as), or
`{"error"}` with 400 (the body broke off), 403, 408 (the body stopped
arriving), 409, 413, 500, or 507 (the disk is full). A host before this
endpoint answers 404 or 405. The saved file then shows a notification at
the desk (docs/desktop.md).

### WebSocket messages

Every message is `{"type": "...", "payload": ...}`.

| Type | Direction | Payload |
|---|---|---|
| `hello` | client → host, first message | `{"codec":"av1"\|"hevc","width","height","fps","qp","auto","start","fixed","audio","screen","scale","keep_awake","lock_after","clipboard_images","monitor","mic","tiles"}`; `width` and `height` are the client's stream view in physical pixels (the tablet's screen, the laptop's window or monitor), each side even and from 320 to 4096, so the stream shows pixel for pixel; the host makes the own screen that size, and takes 2560 for a width or 1600 for a height outside that range; with `auto` the size and rate are the top of the quality ladder and `start` (optional, `{"width","height","fps"}`) the level last used on this network, in either orientation (the host turns it to the screen's), where the host starts at most one level below the top, and at the top when `start` has the top's rate or more, whatever its size, since the size follows the view; `fixed` (optional, overrides `auto`) keeps `qp`, the size and the rate for the whole session whatever the connection does, and lets `qp` go below the host's `min_qp` down to 8 (the Sharp preset), while without it `qp` is clamped to `min_qp`..`max_qp` and adapts (docs/desktop.md); older hosts ignore `fixed` and treat the hello as a manual preset; `audio` (optional) asks for the desktop's sound; `screen` (optional) is `auto` (default), `own` or `mirror` and `scale` (optional, default 2) the own screen's scale (docs/desktop.md); `keep_awake` (optional) how long the computer stays awake after the session: 0 off (default), minutes up to 1440, -1 until a client connects again, or -2 to suspend the computer 20 s after the session unless a paired client's connection is admitted first (its session then cancels the suspend; one whose session never starts schedules it again); `lock_after` (optional) locks the desk when the session ends with no session taking over, also when it was unlocked as the client came, and also if desklinkd restarts meanwhile; a reconnect that takes over finds the desk as it was; `clipboard_images` (optional) the largest clipboard image, in bytes of PNG, the client takes (both clients send 8 MiB); absent or 0, the host sends it clipboard text only; `monitor` (optional) the connector a mirroring session shows, the client's last choice for this host, used when that monitor is on; `mic` (optional) asks for the desk's virtual microphone, which plays the client's microphone packets (type 7); `tiles` (optional) asks for exact tiles (below); `tiles_udp` (optional) says the client takes each frame's tile invalidation as UDP packets (type 8) instead of `tile_frame` messages; older hosts ignore it |
| `session` | host → client | `{"session_id","udp_port","host_nonce","codec","width","height","fps","qp","display","auto","level","levels","audio","screen","scale","keep_awake","primary","desk_off","lock_after","clipboard_images","monitors","mirroring","keysyms","mic","tiles"}`; `levels` is the ladder as `[{"width","height","fps"}]`; `audio` is true when audio packets follow; `screen` is `own` or `mirror`, what the session shows; `scale` the own screen's applied scale (Mutter rounds to the scales it supports), absent when mirroring; `keep_awake` the session's keep-awake setting; `desk_off` says whether the desk's monitors are off: present, true or false, whenever the client can switch them with `desk`, absent when mirroring, for a session that found the desk locked, and from older hosts; `primary` says whether the own screen is GNOME's primary monitor (top bar, overview): present, true or false, whenever the session can move the primary with `primary`, that is with its own screen in the layout beside another monitor that is on, and absent otherwise (mirroring, or the desk's monitors off) and from hosts before the `primary` message, so clients offer the choice only when it is present; `lock_after` the session's lock-after setting; `clipboard_images` the largest clipboard image the host takes (`max_clipboard_image_bytes`, default 8 MiB), absent when images are off and from hosts before clipboard images, so clients send images only when it is present and the image fits; `monitors` (absent when empty) lists the monitors that are on as `[{"connector","width","height","refresh","primary"}]`, mode size in physical pixels, refresh in Hz, `primary` only when true, leaving out the session's own screen, so an own-screen session with the desk's monitors off lists none; `mirroring` is the connector the session mirrors, absent for the own screen; `keysyms` is true when the host types `type` messages (its screen cast belongs to a Mutter remote desktop session), absent otherwise and from older hosts, to which clients send text as key taps; `mic` says whether the desk's virtual microphone plays the client's microphone: hosts that have one always include it, true or false, so clients offer their switch only when it is present; `tiles` is true while the host sends exact tiles, absent from hosts before them; `tiles_udp` is true while the tile invalidations go as UDP packets (type 8), which the host does when the hello had `tiles` and `tiles_udp`; absent otherwise and from older hosts, which send `tile_frame` messages; `fps` is the rate the stream runs at, the level's or the client's `max_fps`, whichever is lower; resent without `host_nonce` whenever the size, rate, level, mirrored monitor or own screen's size changes |
| `apps` | host → client | `[{"id","name","exec"}]`, pinned apps first |
| `launch` | client → host | `{"id","exec"}`; must match an entry of `apps` |
| `clipboard` | both | `{"text"}`, at most 256 KB, checked before sending; both ends accept messages up to 2 MiB, which fits 256 KB even when JSON escapes every character. Or one part of a PNG image, `{"text":"","image","part","parts"}`, below |
| `clipboard_request` | client → host | `{}`; the host replies with `clipboard` |
| `quality` | client → host | `{"width","height","qp"}` |
| `keep_awake` | client → host | `{"minutes"}`, a `hello.keep_awake` value; the host answers with `session` |
| `lock_after` | client → host | `{"lock"}`, as `hello.lock_after`; the host answers with `session`, which reports it as `lock_after` |
| `scale` | client → host | `{"scale"}`, 1 to 3: the own screen's new scale, applied at once (one that rounds to the current scale changes nothing); the host answers with `session` |
| `desk` | client → host | `{"off"}`: true turns the desk's monitors off during an own-screen session, so GNOME moves every window to the client's screen; false turns them on again, which puts the windows back (docs/desktop.md). Only the client that turned them off can turn them on: a session that found the desk locked keeps them off until it ends. The host answers with `session`, or with `error` when refused |
| `primary` | client → host | `{"primary"}`: true makes the own screen GNOME's primary monitor, false gives the primary back to the desk (docs/desktop.md); it lasts until the session ends, and the host answers with `session`, or with `error` when mirroring or when the desk's monitors are off |
| `mirror` | client → host | `{"connector"}`: a mirroring session shows another monitor, one that is on (`session.monitors`); the host answers with `session`, or with `error` for the own screen, a connector that is not on, or a monitor Mutter cannot record, and then keeps mirroring the one it had |
| `resize` | client → host | `{"width","height"}`: the own screen's new size in physical pixels, as `hello` gives it, when the client's view changed (the tablet rotated, the laptop's window was resized or went full screen); clients send it once the size has held for a moment. The host starts the own screen again at that size (docs/desktop.md) and answers with `session`, or with `error` while mirroring or for a size outside 320 to 4096; a size it has already changes nothing |
| `type` | client → host | `{"text"}`: text for the host to type as keysyms, at most 1000 characters (code points; `MaxTypeChars`, `TypeText.MAX_CHARS`), so longer text goes in several messages, which the host types in order. Only to a host whose `session` has `keysyms`; any other answers with `error`, as does one for longer text. Newline and carriage return press Return ("\r\n" once), tab presses Tab and backspace (U+0008) BackSpace; other control characters are skipped. Ordering with keys below |
| `mic` | client → host | `{"on"}`: true creates the desk's virtual microphone ("DeskLink microphone", docs/desktop.md) and makes it the default source, false removes it; it lasts until the session ends, and the host answers with `session`, preceded by `error` when it could not create the microphone. Clients capture only while they asked for it and `session.mic` is true |
| `tiles` | client → host | `{"on"}`: turns exact tiles on or off; on while on starts over, for a client that lost its tiles: the next invalidation invalidates every tile and settled parts are sent again. The host answers with `session`; a session whose hello did not ask for tiles keeps them off |
| `max_fps` | client → host | `{"fps"}`, 1 to 120: the stream runs at most at that rate, the rate the client's screen shows while something holds it below its highest (power saving, say); 0 (always sent, never left out) lifts the cap. The ladder in `session.levels` stays as the hello asked for it, so the level a client remembers keeps its rate; `session.fps` is the rate the stream runs at, the level's or the cap, whichever is lower (docs/desktop.md). The cap lasts until the session ends, so the tablet sends it again once each new connection's `session` arrives. The host answers with `session`, or with `error` for a value outside 0 to 120; hosts before it ignore the message. The laptop client does not send it: it asks for its monitor's rate in the hello |
| `req_idr` | client → host | `{}` |
| `error` | host → client | `{"message","reason"}`; before `session` it means the session failed. `reason` (optional) names an error clients act on: `replaced` means another device took the session over, before the session's cleanup; the client ends the session and must not reconnect by itself, or it would take the desk back and two reconnecting devices would take it from each other in turn. A reconnect of the same device is not told |

**Clipboard images.** A side sends an image only to one that announced a
limit the PNG fits (`hello.clipboard_images`, `session.clipboard_images`), so
an older client never sees an image message, which it would take for empty
text. The PNG goes as `clipboard` messages of at most 512 KiB of it each
(`net.ClipboardImagePart`), sent back to back in order: `image` is the part's
bytes in standard padded base64, `part` counts from 0 (Go leaves it out when
0) and `parts` is how many the image has; `parts` above 0 marks an image
part. One large message would hold back the WebSocket's pings and pongs
while it crosses a slow link, long enough for the tablet to give up on the
connection; between parts they get through, and every part fits the 2 MiB
message limit. The receiver (`net.ImageAssembler`, `ImageAssembler` in
Protocol.kt, the same test cases) starts a new image at part 0, dropping an
unfinished one; drops the image on a part out of order, a changed `parts`,
more parts or bytes than its limit allows, or a result that does not start
with the PNG signature; and ignores parts until the next part 0. A side never
interleaves two images' parts. Text keeps working in between. A clipboard
that offers a PNG and no plain text is synced as the image
(`net.ClipboardPrefersImage`): copying a spreadsheet cell offers both, and
its text is what the other side wants; copying an image in a browser offers
the PNG and HTML that only links to it.

**Text and keys.** Keys travel over UDP and `type` over the WebSocket, so
either could reach the host first. The host reads `type` messages into a
queue of their own (64 at most; a client that sends more is disconnected),
typed by their own goroutine, so they never wait behind a slow control
message such as a resize; a text that comes while a resize or mirror switch
replaces the screen waits for the new screen's cast. An input record with
keys that arrives while text the host read before it is still to be typed
(or within 5 ms of its last keysym) waits for that text, at most 1 s, and so
does every input record after it until the waiting ones are injected, which
keeps the client's order without holding up the host's UDP receiving. So
keys sent after the text come out after it. Clients send a `type` message
only after the keys sent before it, hold later input until the message is
written, and 20 ms longer, so it arrives before them. The tablet also puts
the backspaces of an edit into the message with the text that follows them
(docs/android.md); on both clients a clipboard image going to the host
leaves at most one part queued ahead of a `type` message.

### Exact tiles

With `hello.tiles`, the host sends the exact pixels of parts of the screen
that stopped changing (docs/desktop.md), and the client draws them over the
video at 1:1 until a video frame changes that part (docs/android.md). The
stream is cut into tiles of 64×64 stream pixels, numbered row by row from the
top-left corner; the last column and row are cut to the stream's edge. Two
binary WebSocket messages carry them, told apart by their first byte; all
fields are big-endian (`net/tiles.go`, `TileCodec.kt`; `tiles_test.go` and
`TileCodecTest.kt` assert the same bytes):

| Message | Bytes |
|---|---|
| `tile_frame` | kind 1 (1), flags (1, bit 0 `all`, bit 1 `bitmap`), stream width (2), height (2), frame seq (4), capture time in Unix microseconds (8), count (2), then count tile numbers (2 each); with `bitmap` (UDP packets only) count is the grid's tiles divided by 8, rounded up, and count bytes follow in which bit i mod 8 of byte i / 8 marks tile i |
| `tile_data` | kind 2 (1), flags (1, zero), stream width (2), height (2), count (2, 1 to 64), then per tile: the seq of the frame it is exact for (4), its number (2), the length of its QOI ops (2); then one zlib stream (RFC 1950) of every tile's QOI ops in that order |

Every video frame the host encodes while tiles are on gets an
invalidation, in order, in the `tile_frame` layout. The seq numbers the
host's encoded frames (from 1, never reset within a session); the capture
time is the frame's, as its video fragments carry it, which is also the
time the client's decoder presents it at, so the client names the frame on
screen without tracking frame IDs. The list holds the tiles the client may
have (sent or on their way) whose pixels this frame changed; `all` says that
every tile from an earlier frame is invalid, which the host sends when it
could not check the frame (its scan was skipped), when its pipeline
restarted (new size or scale), after `tiles` turned them on, and over UDP
after a frame it could not send.

**Where the invalidation goes.** To a client whose hello has `tiles_udp`,
the host sends each frame's invalidation in UDP packets of type 8 (the
media plane, below), one just before the frame's fragments and one after
its parity, and `session.tiles_udp` says so; it sends that client no
`tile_frame`. To any other client it sends a `tile_frame` message after the
frame. A client never gets both for one frame, so an older tablet keeps
working with a newer host, and a newer tablet takes `tile_frame` from an
older host.

Up to 0.54, every client got `tile_frame`, queued after the frame's
datagrams had gone: the encoder hands over frame N before the scan of N
starts in the same thread, and the scan finished about 2.3 ms later (1 ms
to download the frame, 1.3 ms to hash it, measured on the desk's GPU at
2560×1600); the message then waited behind any `tile_data` on the TCP
connection (up to 128 KB in the writer, more in the socket). The tablet
decodes a frame in about 3 ms, so it often showed a frame before its
`tile_frame` arrived, and then drew no tile over it: the whole tiled area
switched to video for one frame, keepalive frames of a still screen
included. Now the host holds each frame until its invalidation is known
and sends them together (docs/desktop.md).

**Drawing from two transports.** `tile_data` comes over TCP and the
invalidations over UDP, so they arrive in either order, and an
invalidation may come twice, out of order, late or not at all. The client
(`TileLayer` on the tablet):

- keeps, per tile number, the newest invalidation that listed it (or had
  `all`), and discards a tile whose seq is older than that: tile data older
  than the newest invalidation of its tile is never drawn;
- takes a drawn tile away at the first frame of any invalidation that
  arrives later and is newer than the tile, so an invalidation arriving
  after the tile's data still removes it, and an older one arriving late
  changes nothing;
- ignores an invalidation it already has (the second copy, or a record
  repeated in a later packet);
- draws tiles over a frame only when the invalidations of that frame and of
  every frame since drawing started have arrived, and otherwise draws no
  tile over that frame, so a frame whose invalidation was lost hides tiles
  for that one frame instead of showing stale ones.

Over UDP, drawing starts at an invalidation with `all`. The host makes sure
the first one that reaches the client has it: the tracker starts with
`all`, and a frame it could not send (before the client's first datagram)
makes the next invalidation `all`. A lost packet's invalidation usually
arrives with one of the next three frames, whose packets repeat it. When 8
newer invalidations arrived without it, the client counts it as `all` (any
tile from before it may have changed) and sends `tiles` on, so the host
starts over and sends every settled tile again, since it still counts the
dropped tiles as sent; it asks once, until an `all` newer than its request
arrives. A client that saw 8 invalidations without any `all` asks the same
way. Over the WebSocket every `tile_frame` arrives in order, after the
`tile_data` of the tiles it invalidates, so drawing starts at the first one
the client sees; the host's first may come before the `session` message
that made the client ready for them.

The host counts a tile as on the client from the frame it was captured
from until an invalidation lists it, and queues `tile_data` only for tiles
still valid. Both hold with two transports: a tile and the invalidation
that lists it reach the client in either order and the tile goes either
way, and a client that lost an invalidation drops every older tile and asks
the host to start over, which makes the host count none as sent.

A tile's pixels are the QOI ops of its RGB pixels
(https://qoiformat.org/qoi-specification.pdf) without the header and end
marker, alpha 255 throughout, starting afresh for each tile. QOI keeps text
on a flat background to about 0.2 bytes a pixel, and one zlib stream over a
message's tiles makes a batch of 16 text tiles about 40 % smaller than tiles
compressed one by one; Android decodes zlib natively (`Inflater`) and QOI in
a few lines.

Clients ignore messages and records for another stream size than the one
they decode.

Tiles stay on the WebSocket: a tile is useful only whole and exact, so lost
datagrams would need retransmission and acknowledgements, and TCP's
congestion control keeps a full-screen settle (300–600 KB of text) from
flooding the link. An invalidation is small, matters only until its frame
is shown, and repeats in the next packets, so it goes with the frame.

The host picks HEVC when the client asks for it or when `vaav1enc` is missing;
`session.codec` is authoritative. A new authenticated connection replaces the
running session once its `hello` arrives, if its device is still paired at
that moment; otherwise it gets `error` and the running session continues.

## Pairing

`desklinkd pair` asks the running daemon (over `$XDG_RUNTIME_DIR/desklinkd.sock`)
for a PIN valid for five minutes and prints the pairing code
`{"ip","port","pin","fingerprint","addresses"}` as a QR code, which the
tablet scans, and as a line of JSON, which the laptop client takes pasted;
`addresses` lists the host's other addresses (other interfaces, VPNs).
Clients pair only from such a code and pin its `fingerprint` for every
pairing request, never with a PIN alone or with a fingerprint from an mDNS
announcement: a client's proof lets whoever answered test every PIN offline
(docs/security.md).

**Discovery.** desklinkd announces `_desklink._tcp` with mDNS through Avahi
("DeskLink on <hostname>", the control port, TXT `fp=<fingerprint>` and
`v=1`). An announcement proves nothing; clients match its fingerprint to a
paired host and still pin the certificate when they connect.

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
| 3 | type: 1 video fragment, 2 input, 3 telemetry, 4 keepalive, 5 audio, 6 parity, 7 microphone, 8 tiles, 9 probe |
| 4–7 | session ID (uint32) |
| 8–11 | sequence number (uint32), per direction, starting at 1 |
| 12–13 | frame ID (uint16); for audio and microphone packets, their packet counter |
| 14 | fragment index, low byte; for keepalives 1 marks an echo |
| 15 | fragment count, low byte |

A probe (type 9, session ID 0) checks the media port before any session and
is not encrypted: the client sends the header, its device ID's length (one
byte) and bytes, a 16-byte nonce and HMAC-SHA256(pairing key,
"desklink-udp-probe" | device ID | nonce); the host answers only a probe
whose HMAC checks out, from the address it arrived on, with the header, the
nonce and HMAC-SHA256(pairing key, "desklink-udp-probe-reply" | nonce). The
reply is smaller than the request, and a source address gets at most 8 at
once and 4 a second (`net/probe.go`; `ProtocolTest.kt` and `probe_test.go`
assert the same bytes). `/health` reports the media port as `udp_port` and
the control protocol's number as `protocol` (2 since the probe, 3 since exact
tiles' invalidations go over UDP; absent means 1), which clients compare
with their own to say which side to update.

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

**Pacing.** The host sends a frame's datagrams in groups of 16 with a 250 µs
pause between groups (`pacingBurst`, `pacingGap` in `net/udp.go`), so a
2560×1600 keyframe of about 200 datagrams leaves over about 3 ms instead of in
one burst that can overflow a Wi-Fi access point's or VPN's queue. Most
frames fit one group and are not delayed. Each group is numbered and sent
under the session lock and the pauses are outside it, so input, audio and
keepalives wait for at most one group, and sequence numbers still leave in
order: clients estimate loss from gaps in them, and an audio packet numbered
ahead of video still on its way would look like loss.

**Parity (host → client).** After a frame's fragments the host sends
⌈n/8⌉ parity packets (`net.ParityFor`). Fragment i belongs to group
i mod stride, stride being the number of groups, so a burst of up to stride
consecutive lost datagrams hits each group at most once. The header's frame ID
is the frame's; the plaintext is group, stride, fragment count and the XOR of
the members' payload lengths (four uint16), then the XOR of the members'
complete video payloads (prefix and data, zero-padded). A client that misses
exactly one fragment of a group rebuilds its payload from the parity and the
others (whose prefixes it recreates from the frame's keyframe flag, capture
time, index and count) and parses it like a received fragment; a frame of one
fragment comes back from its parity alone. To make room, a fragment carries
at most 1228 data bytes. Clients that do not know type 6 ignore it, so old
clients and hosts work with new ones.

**Video fragment (host → client).** The plaintext starts with a 12-byte
prefix: flags (bit 0 keyframe), fragment index high byte, fragment count high
byte, reserved, capture time in Unix microseconds (uint64). The rest is frame
data, at most 1236 bytes. The extra high bytes exist because a 2560×1600
keyframe can need more than 255 fragments. A frame is an AV1 temporal unit
(OBU stream) or an HEVC access unit (Annex B, parameter sets on keyframes).

**Tiles (host → client).** Sent only to a client whose hello had `tiles`
and `tiles_udp`: which exact tiles a video frame changed (Exact tiles,
above). Each frame's packet goes just before its first fragment, so it
normally arrives first, and again after its last parity packet, so one lost
datagram rarely loses it; both take sequence numbers like every packet. The
header's frame ID is the frame's. The plaintext is records in the
`tile_frame` layout, back to back: the frame's own, then those of up to
three frames before it, newest first, as many as fit in 1248 bytes. A
record lists its tiles, or holds a bitmap when that is shorter: a 2560×1600
stream has 1000 tiles, a 125-byte bitmap, so every record fits, and a
4096×4096 stream's 512-byte bitmap fits twice. A frame whose own record
would not fit (more than about 9800 tiles) goes as `all` without a list,
and the host starts its tiles over. Clients that do not know type 8 ignore
it. `packet_test.go` and `ProtocolTest.kt` seal the same datagram.

**Audio (host → client).** Sent only when `hello.audio` was set. One Opus
frame per datagram: capture time in Unix microseconds (uint64), then the
frame: 48 kHz stereo, 10 ms, about 128 kbit/s (some 160 bytes), encoded with
`audio-type=restricted-lowdelay` (2.5 ms lookahead). The header's frame ID
counts audio packets (wrapping at 16 bits) so clients drop late and repeated
ones; there is no Opus header packet, clients configure the decoder for
channel mapping family 0.

**Microphone (client → host).** Sent only while the client asked for the
microphone (`hello.mic` or `mic`) and `session.mic` is true. The same layout
as audio, sealed with the client-to-host key and numbered from the client's
one sequence counter: capture time in Unix microseconds of the client's
clock (the host does not compare it with its own), then one Opus frame of
48 kHz mono, 20 ms, about 32 kbit/s (some 80 bytes). The header's frame ID
counts microphone packets per UDP transport, wrapping at 16 bits; capture
that pauses (the app in the background) keeps counting. 20 ms is the usual
frame for speech and what Android's MediaCodec Opus encoder produces; it
halves the packets, and with them the header, tag and VPN overhead, of
10 ms frames for 10 ms more delay. The host reads each frame's duration from
its TOC byte, so other Opus frame sizes play too. It keeps the frames in a
jitter buffer (docs/desktop.md); a frame whose turn has passed is dropped.
`ProtocolTest.kt` seals the same datagram as `packet_test.go`.

**Input (client → host).** One or more 12-byte records:
`kind u8, flags u8, code u16, x i32, y i32`.

| Kind | Meaning |
|---|---|
| 1 key | `code` = Linux `KEY_*`, flags 1 down / 0 up |
| 2 relative move | `x`, `y` in pixels |
| 3 pen | `x`, `y` in 0..65535; flags 0 hover, 1 touching, 2 left proximity; `code` the pressure, 1..65535 while touching, 0 when not reported (the host then uses full pressure) |
| 4 mouse button | `code` 1 left, 2 right, 3 middle, 4 side, 5 extra; flags 1 down |
| 5 scroll | `x` horizontal, `y` vertical, in 1/120 of a notch (positive = right, up) |
| 6 absolute pointer | `x`, `y` in 0..65535 over the video frame; the laptop client's mouse and the tablet's tap-to-click touch |
| 7 held | one record per item the client holds: flags 1 key (`code` = `KEY_*`), 2 mouse button (`code` 1–5), 3 pen (`code` bit 0 touching, bit 1 in range); a single record with flags 0 means nothing is held |
| 8 pen tilt | the tilt of the pen records that follow: `x` towards +x, `y` towards +y, in hundredths of a degree (−9000..9000); sent before a pen record when the tilt changed; older hosts ignore it |

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

**Liveness.** The host ends a session, sending `error` on the WebSocket
first, when no authenticated datagram has arrived from the client for 10 s,
counted from when it sent `session`. Replays and datagrams that fail to
decrypt do not count. Telemetry and held-input snapshots every 250 ms keep a
working session well inside that.

**Delay.** Clients show the time from the host capturing a frame to their
decoder handing it to the screen. A frame's capture time is in the host's
clock, so each client estimates how far its clock is ahead: the smallest
"arrival minus send time" of the host's last 16 keepalives, minus half the
smallest of its last 16 round trips (`client/delay.go`, `HostClock.kt`, the
same tests on both sides). This assumes the path is about as fast both ways;
on an asymmetric link the figure is off by half the difference.
