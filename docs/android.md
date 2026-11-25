# Android client

Package and application ID `com.desklink.client`; minimum SDK 31, target SDK
35, built for the Galaxy Tab S9 (Snapdragon 8 Gen 2, hardware AV1 and HEVC
Main10 decoding). The APK from `make apk` is `bin/desklink.apk`, signed with
the local debug key.

## Connecting

The tablet pairs only by scanning the QR code that `desklinkd pair` shows
(address, port, PIN, certificate fingerprint and the host's other
addresses). The app pins that fingerprint for the whole exchange, so only the
desktop that showed the code can take part. There is no typed-PIN pairing:
without a pinned certificate, whoever answered could take the client's proof,
test all 10⁶ PINs offline (the PIN only feeds a key derivation it can
recompute) and then pair with the real desktop. A code without a well-formed
fingerprint (64 hex digits) is refused with its own message, and
`ControlClient.pair` refuses to run without a pinned fingerprint. **Scan
QR**, **Pair** or **Pair again** on a host, and **Pair** on a host found
on the network all open the scanner. The code decides which host it
pairs (`planPairing`): the saved host with the code's certificate (the same
machine on any network), else the host whose Pair was tapped, else a saved
host at the code's address and port, else a new host named after the
announcement it was found by. The code's certificate always wins; when the
host found on the network announced another one, or the chosen host had
another one pinned, the result message says so, and the paired host
becomes the one the main page shows. **Add host** saves an address only,
for the health check; pairing it still needs the QR code.

### Main page

On a window at least 840 dp wide (the Tab S9 in landscape) the main page
has two panes: the saved hosts as a list on the left, about a third of the
width, and the chosen host's details on the right. Each list row shows the
host's name and a dot with a short status: "Online · 12 ms" (how long
`/health` took to answer, the TLS handshake included, so it reads as a
comparison of addresses rather than a round trip), "Offline", "Waking up…" or
"Checking…", with "not paired" after it for an unpaired host. Hosts found
on the network follow under **Nearby** with **Pair**, and **Scan QR** and
**Add host** close the list. The details show the name, the full status
(desklinkd's version and the answer time, or why the host did not answer,
plus which side to update when the protocol versions differ), the address
with the port when it is not 9443, and whether it is paired. **Connect**
is the main button; while the host does not answer and its MAC is known,
**Wake** sits beside it, and an unpaired host has **Pair** in its place.
Below come the choices made before a session, the same for every host:
**Quality** (the presets, with a line explaining the selected one),
**Screen** (Auto, Own screen, Mirror, with a line each) and **Sound from
the desk**; then the host's less common actions as text buttons:
**Check**, **Test connection**, **Pair again**, **Share logs** and
**Forget** (in the error colour, confirmed first). The first host is
selected until the user picks another; the choice is saved
(`HostStore.selectedHost`), and adding or pairing a host selects it.

Narrower windows, the Tab S9 in portrait included, show one column at most
720 dp wide: the list, then the chosen host's details below it. With the
usual one or two hosts that keeps Connect one tap away without a separate
details page to go back from. Without saved hosts the page explains how to
pair the first one, with **Scan QR** as the main button, **Add by address**
and any hosts found nearby. The ⚙ button in the top bar opens the settings
(see Settings).

The page is `HostsScreen`, a composable of `HostsUiState` and
`HostsActions` only; `ConnectScreen` binds it to `AppViewModel` and holds
the dialogs. `make screenshots` renders it, the settings, the stats
overlay and the HUD with made-up state on the JVM (Robolectric's native graphics and
Roborazzi, 2560×1600 at 340 dpi, Material's baseline colours, since the app
takes its own from the wallpaper) into `bin/screenshots`; `make
android-check` leaves these tests out and nothing compares the images.

### Settings

The settings page lists, at most 720 dp wide and centred: **Picture**
(Sharper colours; Exact text when still, disabled with "Needs Sharper
colours" while that is off), **Input** (Touch, Tap to click or Trackpad;
Trackpad speed, only in trackpad mode; Keyboard shortcuts with its state and
a button to its accessibility page), **Security** (Fingerprint or PIN to
connect; Lock the desk when I disconnect), **Advanced** (Codec, AV1 or HEVC,
AV1 disabled without a hardware decoder) and **About** (the app's version).
Each row has a title, at most a short line under it, and its control on the
right. Every setting is saved as before; Back or the arrow returns to the
main page.

A host is one machine, recognised by its certificate fingerprint, with the
address that answered last and up to seven others (`HostProfile.addresses`,
newest first, `MAX_ADDRESSES` = 8 in all): those in the pairing QR code, in
every `/health` answer of a paired host, and those mDNS announces once they
answered. Unspecified, loopback, multicast and broadcast IP literals are
never kept or probed. The health check and Connect ask every address for
`/health` at once with the pinned certificate and use the first that answers,
so a LAN address beats a VPN one, and the winner becomes the host's main
address. An address where the session got the WebSocket but no UDP datagram
(HTTPS gets through but a firewall drops UDP) is used by its reconnects only
when no other address answers (`AddressChoice`). An unpaired host has no certificate from a QR code to pin, so for
it only the address that answered is kept, not the addresses or MAC its
`/health` reports. Entries with the same fingerprint (a machine paired on
two networks) are merged into one at startup and after pairing: the oldest
entry survives with its name and remembered levels and takes the new
pairing, MAC and address. While the main page is shown, NSD browses for
`_desklink._tcp`. Anyone on the network can announce any name and
fingerprint, so an announcement proves nothing: a paired host announced at a
new address gets that address only once it answers `/health` there, on the
saved port, with the pinned certificate; an unknown one appears under **Nearby**
with a Pair button that opens the scanner, until NSD has
reported it lost on every network it was found on. Wake-on-LAN sends towards
every address and fails only when no packet could be sent. **Forget** asks
for confirmation, then asks a paired host to forget this tablet (`DELETE
/pair`, up to 8 s) and removes the host here whether or not it answered. Only a 200 confirms the removal: the host answers
401 both for a device it no longer knows and for a token it rejects (its
clock moved, say). After a 401, as when the host did not answer, the message
says only this tablet's profile was removed and which `desklinkd peers
remove` to run on the host.

`AppViewModel` owns the host profiles (`HostStore`, SharedPreferences, peer key
wrapped by an AES-GCM Keystore key), the stream settings and the running
`StreamSession`. A session calls `/health`, stamps a bearer token with the
host's time, opens the WebSocket, sends `hello` on the socket whose open
callback fired, and on `session` derives the media keys and starts
`UdpTransport`. The stream shows only once the first authenticated datagram
from the host arrives, which takes about one round trip because the host
echoes the client's first keepalive at once. If none arrives within 5 s, a
first connection ends with a message that a firewall or VPN may be blocking
UDP, instead of showing a blank stream. The decoder exists while both the
session description and the `SurfaceView` surface are available, and it is
rebuilt when the size or codec changes.

Presets (`StreamPreset`, with a line under the chooser explaining the
selected one): Auto (the default) asks for QP 12 at 120 fps and lets the
host lower the frame rate, then the quantizer, and the size only as a last
resort (docs/desktop.md); Sharp asks for the same with `fixed`, so the host
never changes it; Crisp QP 18 at 120 fps, Balanced QP 20 at 60 fps, Office
Saver QP 24 at 60 fps. Auto stays the default for new installs too: on a
clean link it streams exactly what Sharp does (the same size, rate and
quantizer, so the same bytes and text), and on a poor one it gets softer
where Sharp stalls. Every preset streams at the stream view's size in
physical pixels (`streamSizeFor`: the full screen with the system bars
hidden, each side made even and within 320 to 4096), so the picture reaches
the screen pixel for pixel: 2560×1600 on the
Tab S9, 1920×1200 when Samsung's power saving switches it to FHD+, 1600×2560
in portrait. The size comes from the stream screen's layout, or from the
window's bounds (`MainActivity.reportWindowSize`) if the hello goes out
before that. With Auto the app remembers the level per host and network (its kind,
VPN, Wi-Fi, LAN or cellular, plus its default gateway, so home and office
Wi-Fi differ without the location permission an SSID needs; hence
`ACCESS_NETWORK_STATE`) by its size and frame rate, and starts the next
session there (a level saved before networks were told apart by gateway
serves until the network has its own). It remembers at most one level below
the top (`levelToRemember`), which keeps the full size, so one bad moment
does not blur every later session; the host applies the same limit to a
lower level an older version saved. The stats panel shows the level, such
as "Auto 2/5 · QP 12".
Clipboard text over 256 KB is not sent, since the host would refuse it. The
codec defaults to AV1 when the device has a hardware AV1 decoder.

While the network the tablet's data crosses is metered (mobile data, a
phone's hotspot; `NET_CAPABILITY_NOT_METERED` absent), the main page
shows a slim banner under its top bar with what a
stream costs, from the host's measurements in docs/desktop.md: at QP 12
(Auto and Sharp) about 7 MB a minute of desktop work (6.7 MB measured at
60 fps, some 400 MB an hour), about 0.2 MB a minute on a still screen
(26 kbit/s), and the desktop's sound about 1 MB a minute (128 kbit/s). The
app watches the best network that is not a VPN
(`registerBestMatchingNetworkCallback` with `NET_CAPABILITY_NOT_VPN`), the
one a VPN's packets cross: a VPN app that never calls `setMetered(false)`
counts as metered even on Wi-Fi.

## Video

`HardwareDecoder` picks a hardware decoder for the MIME type, preferring one
that advertises `FEATURE_LowLatency`, and configures `KEY_LOW_LATENCY = 1`,
`KEY_OPERATING_RATE = 120`, realtime priority, an 8 MB input buffer and
Qualcomm's `vendor.qti-ext-dec-low-latency.enable`. When `configure` or
`start` throws, it releases that codec and tries again with fewer of these
options (`DecoderSetup`): without the vendor key, then also without
`KEY_LOW_LATENCY`, then also without operating rate and priority; only when
the last fails does the decoder fail to start. The log names each refused
setup, and the stats overlay the options the decoder went without. A codec
error saying the hardware is taken (`ERROR_INSUFFICIENT_RESOURCE`,
`ERROR_RECLAIMED`) drops no option, since none explains it: the session
releases the renderer, leaves Sharper colours on and tries again after a
second, which counts towards the rebuild limit below, as when a hung
codec's release outlasts the 1 s the session waits for it. It runs
in asynchronous mode on its own thread and renders each output buffer as
soon as it is ready. The refresh rate the panel runs at is under Panel rate
below.

The video is laid out on whole physical pixels (`videoRect`, the
`placeVideo` layout modifier): centred and as large as fits without changing
its shape, except that a stream that fits and would grow by less than 5 %
(`SNAP_GROWTH`) keeps its own size with a thin border, since resampling
blurs text and a few percent gains nothing. A stream of the view's size
therefore lands at 0,0 at 1:1. The input view covers exactly the same
rectangle, so touches, the S-Pen and an uncaptured mouse map onto the frame
as before. The activity uses `adjustNothing`: the soft keyboard covers the
bottom of the video instead of resizing or panning the window, so the video
neither moves nor scales while typing, and the modifier bar sits above the
keyboard through the IME insets (`imePadding`, reported on Android 11 and
later whatever the soft input mode).

`FrameAssembler` (in `UdpTransport.kt`) reassembles fragments and releases
frames strictly in frame order. When the expected frame is still incomplete
and the newest frame is two IDs ahead, it counts the frame as lost, stops
releasing frames until a complete keyframe arrives, and the transport sends a
telemetry report with `NeedIDR = 1` at once. The decoder is never flushed, so
recovery costs about one round trip plus the keyframe's transmission. A
partial frame keeps one parity record per group, indexed by group, and
ignores a repeat; the first record fixes the frame's stride, and a record
with another stride is ignored. A fragment or parity record that arrives
checks only its own group, counting the missing members before it rebuilds
any payload, so recovering one fragment of a keyframe costs about as much as
receiving it whole (as on the laptop, `client.FrameAssembler`). The frames it
skips to reach the keyframe count as lost frames, and each start of a wait
for a keyframe (a lost frame, a decoder that asks, a new decoder) as a
keyframe request; both are totals for the session, across reconnects
(`MediaCounters`), in the stats overlay.

`UdpTransport` receives on a thread of its own, `desklink-receive`, at Java's
highest priority, which ART runs at `THREAD_PRIORITY_URGENT_DISPLAY` (nice
−8): every frame passes through it on its way to the decoder, so the UI,
coroutines on the shared pools and the garbage collector's helpers must not
delay it. It ranks above the decoder thread it feeds (`THREAD_PRIORITY_DISPLAY`)
and below the audio priorities, since it does far more work per packet
(decryption, parity rebuilds) than a thread at audio priority should.
Whether this shortens the delay on the Tab S9 has not been measured.

Before a datagram counts for anything, `UdpTransport` checks its sequence
number against a replay window, the same as the host's (`ReplayWindow`): a
number already seen, or 1024 or more behind the newest, is dropped. So a
duplicated or replayed datagram cannot skew loss, the clock estimate or the
receive deadline, and cannot feed the assembler twice.

A transient codec error asks for a keyframe. Any other error, or a codec that
rejects input, marks the decoder failed, and the session replaces it with a new
one built on the same surface. So does a hung decoder (`HangWatch`): one that
has taken no input for 5 s while a frame waited for it; frames keep coming at
least ten a second while the host streams, so each arrival checks. Without
this, a hung decoder would only fill its queue of 30 frames, clear it and
ask for keyframes for good. A codec that fails to configure or start is released at
once. Hardware decoders are few, so replacing a decoder waits (up to 1 s)
until the old codec is released before the new one is created. A session
replaces a failed or hung decoder at most 10 times within 5 minutes
(`RebuildLimit`); the next failure ends the session with a message to that
effect instead of rebuilding for good behind a frozen picture, while an
occasional failure in a long session never comes near the limit. With
Sharper colours (below) a new decoder also gets a new renderer; recovery is
otherwise the same on both paths.

Each frame is timed from the moment the assembler completed it (when it is
handed to the decoder) to the decoder's output callback, matched by its
presentation time, the host's capture time (`DecodeClock`); the time
includes waiting for an input buffer. The stats show the mean and the 95th
percentile of each second's frames (`DecodeMeter`, nearest rank). Started,
the decoder reports itself (`DecoderReport`, in the log and the stats): its
name, the options it accepted, its vendor parameters
(`MediaCodec.getSupportedVendorParameters`), and whether it is rated for the
stream's size at the session's top frame rate (`DecoderCapability`): not
when `areSizeAndRateSupported` says no; otherwise by its performance points
when it lists any (`getSupportedPerformancePoints`, what Android's media
performance class is certified against), else by the frame rates it measured
at that size (`getAchievableFrameRatesFor`), else "rating unknown".

### Panel rate

The activity asks for the display's highest refresh rate while streaming
(`preferredDisplayModeId`), and the video surface's frame-rate hint is that
same highest rate (`Surface.setFrameRate`, `FRAME_RATE_COMPATIBILITY_DEFAULT`),
not the stream's: so a 60 fps preset, or a capped stream, keeps the panel and
with it touch and S-Pen sampling at 120 Hz. Power saving or Samsung's
"Standard" motion smoothness can still hold the panel at 60 Hz, and frames
above that would be encoded, sent and decoded for nothing. So while a session
runs, `PanelWatch` reads the panel every half second: the current mode's
refresh rate (`Display.getMode()`, not `Display.getRefreshRate()`, which may
report a frame-rate override or the content's rate and would follow the
capped stream down), the highest mode at the same resolution, and
`PowerManager.isPowerSaveMode`. Once the stream is on screen,
`PanelRateJudge` decides the rate the panel shows. When it offers no faster
mode at all (Samsung's "Standard" may hide the 120 Hz modes), that is its
highest mode's rate at once. Otherwise a sample below the highest mode
counts only when power saving is on or the screen was touched within the
last second (`MainActivity.dispatchTouchEvent`, the HUD included), because
Samsung's adaptive refresh rests at 60 Hz on a still screen and comes up at
a touch; three or more such samples over 3 s, none more than 1 s after the
one before, below the highest rate mean the panel is held at the highest of
them. Any sample at the highest rate (within 1 Hz, 119.88 counts as 120)
ends that at once. The main page is never judged. When the highest
rate changes during a session (the setting changed), the activity and the
video surface ask for the new one. While the rate the panel shows is below
the preset's rate, the session sends `max_fps` with it, and `max_fps` 0 once
the panel is free again; the host then runs the
encoder at that rate without changing its quality ladder (docs/desktop.md).
Each new connection of the session sends the cap again once its `session`
arrives, rather than in the hello, so a reconnect needs no new hello field
and an older host simply ignores the message. The cap shows in the stats
overlay, and the log notes every change of the panel's mode, power saving
and the cap.

## Sharper colours

The stream is 4:2:0: luma (brightness) at every pixel, chroma (colour) once
per 2×2 pixels. Interpolating chroma, as the direct path's decoder and
compositor do, blurs the colour edges of coloured text. On a test image of
blue and red code on the dark theme's grey 28, encoded as desklinkd does at
QP 12, the mean error on and next to the letters is 13.01 levels (of 255),
against 1.10 for grey text. With **Sharper colours** on (the default,
`HostStore.sharperColours`; it applies from the next connection) the session
draws through `GuidedRenderer`, which rebuilds chroma at every pixel from the
luma, as a guided filter does: around each chroma sample it fits chroma as a
straight line of luma over 5×5 chroma samples (10×10 pixels, regularised by a
luma variance of (2/255)²), interpolates the line's slope and intercept
bilinearly to each pixel and applies them to that pixel's own luma. On the
test image that cuts the error on coloured letters to 3.83 levels with the
same stream, and leaves grey text at 1.10; undoing the host converter's
code scale as well (below) makes them 3.76 and 1.03. `GuidedChromaTest`
runs the shaders' arithmetic on a decoded crop of it. Where two colours of equal
brightness meet, luma has no edge to follow and the fit can misplace colour:
on 3-pixel stripes of red and green with equal luma it is worse than plain
interpolation (15 against 11 levels).

The decoder then renders into a `SurfaceTexture`'s surface instead of the
`SurfaceView`'s. The renderer runs on its own thread with an OpenGL ES 3
context on the `SurfaceView`'s surface and reads each frame as an external
texture through `GL_EXT_YUV_target` (`__samplerExternal2DY2YEXT`), which
returns the stored Y, Cb and Cr instead of RGB, sampled with `GL_NEAREST` at
pixel centres so that every fetch is one stored sample. `GuidedShaders` has
three passes: per chroma sample, the 2×2 block's mean luma and the sample's
chroma into a half-size RGBA16F texture; per chroma sample, the window's
slope and intercept into a second one; per pixel, the interpolated line, then
BT.709 limited range to R'G'B' (`StreamColor`, with the 10-bit constants
64/876 and 512/896), written into an RGBA8888 buffer unchanged, so that, as
with the direct path's `COLOR_TRANSFER_SRGB`, nothing is re-curved.

The host's converter writes every code 257/256 of the standard one
(docs/desktop.md, Encoder), so read as standard every grey would come back
a level too red and too blue, and black would light the panel faintly
magenta, next to exact tiles that are true grey. `StreamColor` undoes the
scale: luma from black at 64.25, half a code lower because the encoder
truncates, over 879.4 codes; chroma codes 512 to 514 read as no colour,
since the converter writes a grey's chroma as 514 but the encoder rounds
flat areas to 512, and codes beyond are scaled back, joined below 512 by a
ramp so the mapping has no steps. `StreamColorTest` decodes the codes the
host's GPU writes; greys come back exact, colours within 0.75 levels, and
colours within a level of grey are pulled to grey. The direct path, which
the decoder converts, keeps the tint. Measured on a dark-theme image
through the real pipeline (docs/desktop.md, Exact tiles), background
pixels of tiles that hold text, 3 px or more from the letters, frame 11 of
a still picture (worst channel's mean error, pixels off by ≥ 1 and ≥ 2
levels):

| Grey | QP | Before | After |
|---|---|---|---|
| 0 | 12 | R +1.38, 94 %, 60 % | R +0.65, 71 %, 3 % |
| 28 | 12 | B +1.52, 97 %, 70 % | R +0.51, 66 %, 4 % |
| 36 | 12 | B +1.65, 96 %, 72 % | B +0.69, 74 %, 16 % |
| 0 | 25 | R +1.41, 91 %, 53 % | R +0.77, 71 %, 16 % |
| 28 | 25 | R +1.32, 95 %, 65 % | R +0.47, 74 %, 14 % |
| 36 | 25 | B +1.46, 96 %, 73 % | B +0.54, 79 %, 18 % |
| 0 | 38 | B +1.65, 95 %, 66 % | R +1.06, 76 %, 36 % |
| 28 | 38 | B +1.89, 96 %, 84 % | B +0.88, 86 %, 43 % |
| 36 | 38 | B +2.14, 96 %, 85 % | B +1.20, 91 %, 62 % |

In flat tiles, away from text, 2–18 % of pixels were off by one before and
0–17 % after, by two 0–16 % before and 0–8 % after; at QP 38 the encoder
itself lifts flat black to (1, 1, 1). The rest is compression, which exact
tiles over still text remove. The
`SurfaceView`'s buffer gets the stream's size (`setFixedSize`), so each stream
pixel is one buffer pixel and the compositor scales the buffer to the view,
as it does the decoder's buffers; the pinch zoom transforms the view as
before. `SurfaceTexture`'s transform matrix pulls a cropped picture's edges in
by a texel for the sake of filtering; `TextureMapping` takes that out again,
assuming the decoder crops from the top-left corner, so samples stay on texel
centres. A frame is drawn as soon as the decoder releases it (frames that
queued meanwhile are skipped) and queued with swap interval 0, so a newer
frame replaces one the display has not taken yet; drawing adds its own time,
not a frame, to the delay, and the delay in the stats counts from the swap.
Nothing is allocated per frame.

The session draws directly instead, as without the setting, when the
renderer cannot start (no OpenGL ES 3, no `GL_EXT_YUV_target`, a shader that
does not compile, no renderable RGBA16F, an odd stream size), when the decoder
does not start on its surface, when the first frame raises a GL error (a
decoder format the YUV sampler cannot read), when drawing throws or a swap
fails, and when drawing is too slow (`RenderBudget`): each frame's GPU time
(`GL_EXT_disjoint_timer_query`; without it the CPU time to submit the frame)
is compared with half a frame at 120 fps, 4.2 ms, and three seconds in a row
in which most frames took longer give up. The first 30 frames, which include
compiling the shaders, are not judged, and a second with fewer than 30 frames
breaks the run, because a GPU woken from idle draws its first frames slowly.
The reason is logged ("sharper colours off for this session") and shown in
the stats overlay, and the session draws directly until it ends; the next
Connect tries the renderer again.

### Overlay hook

`StreamSession.setVideoOverlay` gives a `VideoOverlay` to the session's
current and later renderers; the session uses it for exact text (below).
Its `draw(OverlayQuads, frameMicros)` runs on the render thread, with the
renderer's GL context current, after the video of every frame, and after
`StreamSession.redrawVideo()`, which draws the last frame again for an
overlay that changed between frames. `frameMicros` is the drawn frame's
presentation time, the host's capture time of that frame (0 when unknown).
`OverlayQuads.draw(texture, left, top, width, height)` draws a
`GL_TEXTURE_2D` over that rectangle in stream pixels from the stream's
top-left corner, blended as premultiplied RGBA and sampled with `GL_NEAREST`,
first row at the top, so a texture of the rectangle's size lands 1:1 on the
stream's pixels; it holds the desktop's sRGB-encoded values, like the video.
The overlay may create and upload textures in `draw`; they die with the
context, and the renderer calls `release()` before its context goes away
(new decoder, fallback, end of session). Overlays are drawn only while
`StreamSession.rendering` is `VideoRendering.Guided`; the direct path has no
GL context to draw them in.

### Exact text

With **Exact text when still** on (the default, `HostStore.exactText`; it
needs Sharper colours and applies from the next connection) the hello asks
for exact tiles, and while the host's `session` says `tiles`, the session
draws them over the video through a `TileOverlay` (protocol and host side in
docs/protocol.md and docs/desktop.md). Once part of the screen has not
changed for 200 ms, its exact pixels replace the video's there, so still text
is pixel for pixel the desktop's, colour included; the stats overlay shows
how many tiles are drawn.

`TileLayer` decides what to draw, without GL, so `TileLayerTest` covers it:
it maps each `tile_frame`'s capture time to the host's frame seq, and the
renderer's `frameMicros` names the frame on screen. A tile is drawn over a
frame only once that frame's `tile_frame` has arrived, the tile is from that
frame or an earlier one, and no `tile_frame` in between listed it. A tile
listed by a frame goes when the renderer draws that frame, or any later one
when frames were skipped; without the frame's `tile_frame` (late) no tile is
drawn over it, and its arrival redraws the frame with them. A tile that
arrives after a newer `tile_frame` listed its part is discarded. The last 512
`tile_frame` times are kept, about four seconds of frames.

`TileOverlay` keeps one RGBA texture of the stream's size with every tile at
its place and transparent elsewhere, drawn as one premultiplied quad, so a
full screen of tiles costs one draw call; each tile is uploaded once (at
most 64, 1 MB, per frame; the rest follow on a redraw), and a tile that goes
is cleared by a scissored `glClear` through a framebuffer on the texture. The
quad is drawn at stream pixels like the video, so the pinch zoom moves and
scales tiles with it. Tiles are decoded on the WebSocket's thread: zlib with
`Inflater` into exactly the size the records declare, then QOI into RGBA
(`TileWire`, `TileCodecTest` decodes the host's vectors).

A new connection or stream size gets a new `TileLayer`, since the host's seq
and tile numbers start afresh. When the renderer is rebuilt (a new surface or
decoder) its texture goes; the session then sends `tiles` on, and the host
invalidates every tile and sends the settled ones again. When the session
falls back to drawing directly, it sends `tiles` off and stops asking for
them: the direct path cannot tell which frame the compositor shows, so an
overlay View could not take a tile away with the frame that changes its part.

## Input

All input goes through `InputView`, a transparent focusable view over the
video, and `InputMapper`. Android batches motion events and hands them to a
view once per frame, so a touch, the S-Pen or a mouse would wait up to a
refresh (8 ms at 120 Hz, 4 ms on average) before going to the host.
`InputView` asks for unbuffered dispatch (`View.requestUnbufferedDispatch`)
for keyboards, the touchscreen, the S-Pen and an uncaptured mouse, a captured
touchpad and a captured mouse (the button, pointer, position and trackball
source classes), and again for each touch-down's gesture, as moonlight found
the per-source request alone not enough for touch streams. Each event then
arrives as the device reports it, and goes out in a datagram of its own: more,
smaller input packets than before.

- **Hardware keyboard:** keys go to the desktop only while the stream is on
  screen (`StreamSession.State.Streaming`) with the HUD closed
  (`AppViewModel.keysToDesktop`); while the session connects or reconnects,
  or the HUD is open, every key stays with Android.
  `MainActivity.dispatchKeyEvent`, `InputView.dispatchKeyEventPreIme` and the
  soft keyboard's key events all go through `InputMapper.onKeyEvent`, which
  applies that rule. A key's
  release and repeats go where its press went (`KeySplit`): Shift pressed
  while the HUD is open and released after it closed is released in Android,
  and a key pressed for the desktop is released there even if the HUD
  opened meanwhile, so neither side keeps a key down. Hardware keyboards report
  the evdev code as the scan code, which is sent unchanged for keys that type
  a character, so the host's layout decides; otherwise `LinuxKeyCodes` maps
  the key code. A function key the keyboard's layout defines keeps its
  function. The Book Cover Keyboard Slim has no Esc key of its own: its
  top-left key sends the scan code of ` (41) and gives Esc only with Fn
  (KEY_FN_ESC, 465), as Samsung's layout `Vendor_04e8.kl` maps them. For
  coding, DeskLink swaps them on that keyboard (`escFirstKeyboard`): the
  plain key sends Esc and Fn with it types `. Volume and power keys stay with Android,
  and so does Back from non-keyboard sources (the system back gesture opens
  the HUD). Android keeps Win (Meta), every Win+key combination and Alt+Tab
  before any app sees them (checked with `getevent` on Android 16: the
  keyboard sends them, the activity never gets them). `KeyCaptureService`, an
  accessibility service the user switches on once ("Keyboard shortcuts" in
  Settings › Input opens its page), receives physical keys before those
  shortcuts and hands them to `KeyCapture`, which takes a press for the
  desktop only while the activity is resumed and focused, the stream is on
  screen and the HUD is closed; otherwise it goes on to Android. `KeyCapture`
  keeps its own `KeySplit`, so a release follows its press there too, even
  when the activity has paused in between. It reads no window content and
  gets no accessibility events. An APK installed from a browser may need
  "Allow restricted settings" in its App info before Android lets the
  service be switched on.
- **Mouse and trackpad:** `requestPointerCapture()` while streaming with the
  HUD closed. Relative mouse motion (`AXIS_RELATIVE_X/Y` with history, on
  presses and releases too), wheels and buttons go straight through. Wheels
  send 120 units a notch (`WheelAccumulator`), and the fraction of a unit
  that a smooth-scrolling Bluetooth mouse reports is kept for the next step
  instead of being dropped; the host turns units into high-resolution and
  classic wheel events. Left, right, middle and Forward reach the desktop.
  The Back (thumb) button opens and closes the HUD instead (`MouseBack`):
  Android reports it as a button, as a Back key made from the button, or
  both, so the key toggles only when no button press came with it, and a
  mouse's Forward key is dropped because its button already went through. A
  captured touchpad reports finger positions, so it runs through its own
  `TouchGestureEngine`, scaled so a full swipe moves 1600 host pixels; a
  physical click suppresses the tap.
- **Uncaptured mouse:** while the HUD is open the pointer is released, so the
  mouse gets Android's own pointer and clicks the HUD; closing the HUD
  captures it again. A mouse over the video without capture (the HUD open,
  or capture lost) is handled as a mouse, not a finger (`AbsoluteMouse`): its
  position goes out as absolute pointer records like the S-Pen's, its
  buttons from Android's button state (every button, a click is a click),
  and its wheel and presses arrive through `onGenericMotionEvent`.
- **Touchscreen**, by the **Touch** setting (Settings › Input):
  - **Tap to click** (default, `DirectTouchEngine`): positions go out as
    absolute pointer records (kind 6, 0..65535 across the video). A tap clicks
    where the finger touched; a second tap within 300 ms and 60 px reuses the
    first position so the desktop sees a double click; a press held 500 ms
    without moving right-clicks there; one finger dragged past the slop holds
    the left button from where it went down (selecting text, moving windows);
    two fingers move the pointer to their centre and scroll naturally, or
    right-click there on a tap; a three-finger tap toggles the HUD. Two
    fingers rarely land together, and a scroll's first finger often moves
    before its second touches down, which used to start a drag and select
    text. So a one-finger drag presses the button only 150 ms after the touch
    (`SECOND_FINGER_MS`), still from where the finger went down; a swipe that
    ends sooner is sent whole as a drag; and a second finger that lands
    during a drag releases the button and scrolls.
  - **Trackpad** (`TouchGestureEngine`): one finger moves the cursor relative
    to where it is (Trackpad speed, shown under Touch in this mode), a tap clicks at the cursor, a double tap
    held down drags with the left button, two fingers scroll or right-click on
    tap, a three-finger tap toggles the HUD.
  A gesture finishes in the mode it started in.

  In both modes, three fingers swiped up, left or right send GNOME's
  touchpad gestures as their shortcuts (`Swipe`): up opens the overview
  (Super), left goes to the next workspace (Super+Page Down) and right to the
  previous one (Super+Page Up). A swipe fires once per gesture, as soon as
  the fingers' centre has travelled 120 px (about 11 mm on the Tab S9) along
  one axis (`SwipeTracker.SWIPE_PX`); the longer axis decides, and a swipe
  down sends nothing. The movement itself never reaches the desktop (three
  moving fingers used to scroll in Tap to click), so a three-finger tap,
  lifted within 400 ms without moving past the slop, still toggles the HUD.
  A captured touchpad does not swipe, since it reports in its own units.

  While the session mirrors a monitor (`session.screen` is `mirror`), two
  fingers also pinch to zoom the video on the tablet, from 1x to 4x, around
  the pinch's centre (`VideoZoom`). The own screen is drawn 1:1 for the
  tablet, so it never zooms. Two moving fingers pinch when the distance
  between them changes by 32 px (about 3 mm, `TwoFingerGesture.PINCH_SLOP`)
  before their centre moves past the slop, or by more than their centre
  moved, as when one finger stays still; otherwise they scroll as before.
  While the video is zoomed, every two-finger movement pans or zooms it and
  none scrolls the desktop, but a two-finger tap still right-clicks; a zoom
  below 1.05x counts as 1x. Nothing about the zoom reaches the host: the
  `SurfaceView` is scaled and moved in a graphics layer clipped to the
  video's area, and the input view stays as it was, so touch, S-Pen and
  uncaptured-mouse positions are mapped back through the zoom
  (`Zoom.absX`/`absY`) and the desktop receives the point shown under the
  finger. The trackpad's relative cursor movement is not scaled by the zoom.
  The zoom resets when a session starts and when it stops mirroring.
- **S-Pen:** hover and touch become absolute pen events normalised to the video
  area, with historical samples batched into one packet; the barrel button is
  a right click. Each touching sample carries its pressure, and a tilt record
  goes before a sample whose tilt moved by half a degree or more since the
  last one sent (`PenAxes` converts Android's tilt and orientation into
  tilt towards x and y, as Chromium does for web pointer events). Android sends HOVER_EXIT just before a hovering pen touches
  down, so "left proximity" is sent only if no pen event follows within 80 ms.
  Taps are held still (`PenTapSteadier`): a pen wobbles a few pixels as it
  lands and lifts, and GTK takes two clicks for a double click only within
  about 5 logical pixels (some 1 mm here), so a double tap on a word would
  often miss. For 100 ms after the pen touches down, a position within 20 px
  (about 2 mm) of where it landed is sent as the touch-down position, the
  lift included; once it moves further or the time is up, positions pass
  through until it lifts, and never snap back. When the last touch stayed a
  tap (it lifted while still held), a touch within 150 ms and 50 px of that
  lift lands where the lift was sent, so the second tap of a double tap
  clicks on the first; after a stroke the next touch lands where it
  touches, so handwriting's quick next stroke is not joined to the last.
  Hover, pressure and tilt pass through unchanged; the first 20 px of a
  stroke drawn within 100 ms are the one thing held back, which matters for
  drawing, not for office work. Fingers keep their own second-tap snap
  (300 ms, 60 px).
- **Soft keyboard:** `InputView` is the text field; the bar of Esc, Tab,
  Ctrl, Alt, Super and arrows sits above it, and Ctrl, Alt and Super latch
  for the next key. Every change the keyboard makes is an edit: backspaces,
  then new text (`TextEdit`). Composing text, a word the keyboard builds and
  may change while it is typed, becomes edits against what it last typed
  (`ComposingText`, counting code points, so an emoji is one backspace).
  `TextRoute` decides how an edit travels. Plain ASCII goes as key taps on a
  US layout, with the other keys over UDP. Any other text (accents, other
  alphabets, emoji, dictated sentences) goes, when the host types text
  (`session.keysyms`), as `type` messages of at most 1000 code points, the
  edit's backspaces in the message as U+0008 so they cannot overtake or trail
  it, and the host types it as keysyms: right on any host layout that has the
  characters, dropped by Mutter where it lacks them (docs/desktop.md). A mixed
  edit such as "café " goes whole as text. Older hosts, and every edit while a
  modifier is latched (its shortcut needs a key), get key taps as before, and
  characters without a US key are lost there.
  The keyboard covers the bottom of the video, which keeps its size and place
  (see Video).

`UdpTransport` sends input on one sender thread, in packets of at most 104
records, and every 250 ms it sends a snapshot of the held keys, buttons and pen
state (`HeldInput`) so the host can lift anything whose release was lost.
Typed text keeps each character's press and release in one packet. When keys
stop reaching the session (the window loses focus, the activity pauses, the
HUD opens or the soft keyboard is shown), `releaseHeld` sends a release for
every held button, the pen and every held key, then the now empty snapshot.
It runs on the same sender thread as input, so it follows everything sent
before it, and a key let go outside DeskLink does not stay down on the
desktop. After the transport closes, further sends are dropped instead of
failing; sends queued before the close still go out.

Text goes over the WebSocket and keys over UDP, so `StreamSession.typeText`
sends a `type` message from the sender thread (`UdpTransport.sendInOrder`):
every key queued before it has left by then. The thread then waits until
OkHttp has written the message to the socket (`awaitWritten`, at most
100 ms; `send` only queues it for OkHttp's writer) and 20 ms longer
(`TEXT_LEAD_MS`, room for a segment Nagle's algorithm holds until the last
one is acknowledged), so keys typed after the text reach the host after it;
the host holds keys back only while it types text it has received
(docs/protocol.md). Input queued meanwhile waits those few milliseconds;
microphone frames do not go through this thread. A clipboard image going to
the host leaves at most one part in OkHttp's queue (Clipboard images), so
the write the thread waits for is not stuck behind a whole image.

## HUD

A pill (☰) on a side edge opens the HUD: a sheet 368 dp wide along the
right edge of the screen, its full height less an 8 dp margin, over the
video. At the top are the host's name and one status line, a dot in the link
grade's colour with the grade, round trip and frame rate ("● Excellent ·
9 ms · 120 fps"; the other numbers are in the stats panel), and ✕, which
closes the sheet. Below them the eight actions are tiles, four to a row,
each an icon over a short label: Keyboard (filled while the soft keyboard
shows), Trackpad (filled while the touchscreen acts as a trackpad), Apps
(Win+A, GNOME's app grid), Overview (Win), Send clip and Get clip (the
clipboard), Stats (filled while the stats overlay shows) and PrtSc (the
Print key, which Android keeps for its own screenshot). Keyboard, Trackpad,
Apps, Overview and PrtSc close the sheet.

The settings follow as one-line rows, as on the settings page, in two
groups. A long press on a row, or the mouse pointer over it, shows its full
explanation above it without changing the setting.

- **Screen**, when the host reports `session.screen`: **Mode**, Own screen
  or Mirror, which saves the other choice and connects again. While
  mirroring, when the host lists more than one monitor in
  `session.monitors`, **Monitor** opens a menu of them by connector with
  their size: a tap switches the session to that monitor at once
  (`mirror`) and saves the choice per host (`HostStore.mirrorMonitor`),
  sent as `hello.monitor`, so the next mirroring session starts on it. On
  the own screen, **Scale** has − and + in 25 % steps from 100 to 200 %
  (saved, sent at once as `scale` and in the next `hello`); 100 % and 200 %
  carry a ✓, and the row's subtitle says that ✓ keeps every app sharp: at
  the fractional scales GNOME draws XWayland apps at 100 % and stretches
  them (`OwnScale.isSharp`). **Primary screen** moves GNOME's top bar and
  overview onto the tablet's screen for this session (`primary`; not saved,
  since beside an unlocked desk someone may be using it; shown only when the
  host reports `primary` in `session`, so a host that cannot move the
  primary shows no switch that does nothing). A 0.27.0 host leaves
  `primary` out while it is false, so with that host the switch shows only
  while it is on: update the host together with the app. **Desk screens
  off** (`desk`) turns the desk's monitors off for the session, so GNOME
  moves every window onto the tablet's screen, and on again, which puts them
  back; it shows only when the host reports `desk_off`, so not for a session
  that found the desk locked, which keeps it dark.
- **Session**: **Microphone** (see Sound), only when the host reports `mic`
  in `session`; **Keep awake after disconnect**, a menu of Off, 1 h, 4 h,
  ∞ (until a client connects again) and Sleep (which suspends the computer
  20 s after the session unless the tablet reconnects), saved and sent as
  `keep_awake` and in the next `hello`; and **Lock the desk when I
  disconnect** (`lock_after`, also in Settings › Security, saved and sent in
  each `hello`).

**Disconnect**, in the error colour, sits at the bottom below a divider, away
from the tiles. Everything between the header and Disconnect scrolls when
the window is too short for it, as in split screen; on the Tab S9 the sheet
fits without scrolling in landscape and portrait. There is no app list:
GNOME's own app grid and search do that job. The sheet is a Material
`Surface`, so every touch on it stays with it; the margin around it and the
rest of the screen reach the desktop. It uses the wallpaper's dark colours
in both system themes (`HudTheme`), since it sits over video. The stream
screen shows the pill and the sheet only while the session streams; while
it connects or reconnects, that screen shows a spinner, the state and
Cancel instead.

The sheet is `HudSheet`, a composable of `HudUiState` and `HudActions`
only. `hudUiState` decides what it shows from the session's state, the link
statistics and the app's settings (`HudStateTest`); `HudSheetTest` drives the
sheet on Robolectric (a tap flips a switch, a long press only explains it, a
menu choice applies); and `StreamHud` binds it to `AppViewModel`, asking for
the microphone permission when needed. `make screenshots` renders it too,
with the Keep awake menu and a long-press explanation open.

Drag the pill to move it: when let go it sticks to the nearer side edge at
that height, and the place is remembered. Only the pill takes touches.

The stats overlay (on by default; the HUD's Stats tile turns it off) sits in
the top-left corner as a small pill (`StatsPill`): a dot coloured by the
link grade and the frame rate, such as "● 120". The grade comes from round
trip and loss (Excellent under 30 ms and 0.5 %, Good under 80 ms and 2 %,
Fair under 150 ms and 5 %, else Poor; the host starts adapting at 2 % loss),
so the dot turns amber and red as the link suffers. A tap on the pill opens
the details panel (`StatsPanel`, fed a `StatsDetails`) and a tap on the
panel closes it. The panel shows the grade and four groups of labelled
values:

- **Link:** frame rate, bitrate, round trip, loss, the delay from capture on
  the host to the decoder releasing the frame to the screen, or with Sharper
  colours to the renderer queueing it (averaged over a second; the screen
  adds up to one refresh, about 8 ms; "–" until a frame has been shown), and
  the data received in this session (kept across reconnects).
- **Picture:** the stream's codec, size and rate, the auto level and QP,
  what the host shows (`session.display`, such as "own 2560x1600 screen at
  200%"), and where the picture lands in physical pixels: the capture size
  (the own screen's mode, the top of the ladder, or the mirrored monitor's
  mode from `session.monitors`), the encoded size, and the rectangle the
  decoder's output fills on the screen with its position, then "1:1" when
  it lands pixel for pixel or the factor the screen scales it with, such as
  "×0.63" (`shownRect`, `shownScale`). That rectangle is the video's place
  with the size `surfaceChanged` reports for the SurfaceView's buffer,
  enlarged and moved by a pinch zoom. With exact tiles, the tiles drawn.
- **Decoder** (Video, Panel rate): decode time mean and 95th percentile
  ("3.1 ms (p95 5.2)"), frames lost and keyframes asked; with Sharper
  colours on, the renderer's last second ("1.3 ms GPU (max 2.1)") or why
  the session draws directly; then the decoder's name, the stream's size at
  the session's top rate with "rated", "not rated" or "rating unknown", and
  the low-latency options the decoder refused, if any.
- **Display:** the panel's rate with ", power saving" while it is on, the
  frame-rate cap the session asks for ("none" without one),
  `Build.SOC_MODEL` and Android's media performance class
  (`Build.VERSION.MEDIA_PERFORMANCE_CLASS`, "none" when the device declares
  none).

Below the groups, `StatsCharts` draws the last minute of FPS, Mbps, round
trip, loss and delay as small line charts, three to a row, each on its own
scale: it names the metric, shows the current value and the top of its
scale (the smallest round number above both the values and a minimum, such
as 2 % for loss, so a quiet link does not fill the chart), and puts the
newest sample at the right edge. Delay has gaps until a frame has been
shown. The history (`StreamSession.history`, 60 samples) comes from
`UdpTransport.samples`, one `LinkStats` a second; the 250 ms loss-only
updates of `stats` are left out, and the two update `stats` atomically
(`MutableStateFlow.update`), so one never overwrites the other's change. It
spans reconnects. The pill and the panel are drawn without a Material
`Surface`, which would swallow touches meant for the desktop around them;
every touch on the pill or the panel, a drag included, stays with them, and
the rest of the screen reaches the desktop.

While a pinch has zoomed a mirrored monitor (see Input), a chip at the top
centre of the screen shows the zoom level, such as "2.0×", and Reset; a tap
on it shows the whole video again. It is drawn the same way as the stats
overlay, so only the chip takes taps, and it disappears at 1x.

## Screen

**Screen** on the main page chooses what a session shows (`hello.screen`):
**Auto** (default) gives the tablet its own screen, of the tablet's size in
pixels at 200 %, when the computer is locked as it connects, and otherwise
mirrors the monitor last chosen in the HUD, or else the one closest to the
tablet's shape, without changing anything at the desk; **Own screen** always
adds the tablet's screen (the desk's monitors flicker once); **Mirror**
never does. While streaming, the HUD's Mode segments (Own screen, Mirror) save the
other choice and connect again.

When the tablet rotates during an own-screen session, the stream view's
size changes (the activity handles orientation changes itself), and once
the new size has held for 0.8 s (`ScreenResizer`, `AppViewModel.followView`;
a rotation can report a size on its way) the app sends `resize` with it. The
host starts the own screen again at that size (docs/desktop.md) and resends
`session`; until then the old stream is letterboxed. The app also checks
when a session starts streaming, in case the tablet turned while it
connected, and reconnects ask for the size the view has then
(`HelloBuilder.size`). A size the host refused is asked for again only after
the view went back to the screen's size. Mirroring ignores rotation: the
landscape stream is letterboxed on a portrait screen, and a pinch zooms it.

## Sound

With **Sound from the desk** on (the default), `hello.audio` asks
for sound and `AudioPlayer` plays it: each Opus packet is decoded by
`MediaCodec` (`audio/opus`, configured with a generated OpusHead, 2.5 ms
pre-skip and 80 ms seek pre-roll) and written to a `PERFORMANCE_MODE_LOW_LATENCY`
`AudioTrack`. The track asks for a 100 ms buffer, room for the most the
player queues and two packets, and if the device refuses it for 120 ms,
then 240 ms; the delay comes from what is queued, not from the buffer's
size. Playback starts once 30 ms is queued. After that `AudioCatchUp` keeps
the queue near 40 ms: a Wi-Fi hiccup holds packets back and then delivers
them at once, and without catching up the queue would stay that much longer
(near 80 ms) for the rest of the session. Every packet is decoded, so the
Opus decoder's state stays whole; a decoded packet is then dropped rather
than played when more than 80 ms would be queued, when the queue has stayed
above 40 ms for half a second and the packet is silence (every sample below
64, about −54 dBFS), which nobody hears, or, when it has stayed above for
2 s, at most one packet of sound every 250 ms, a 10 ms skip each, so music
that never pauses catches up too. Late or repeated packets (by the 16-bit
counter) are dropped. The decoder runs on its own `HandlerThread` at
`THREAD_PRIORITY_AUDIO`, since with so little queued a decode delayed by the
UI would be heard; the log's audio lines count the packets dropped to catch
up and the track's underruns. The setting applies from the next connection.

The HUD's **Microphone** switch (off by default, saved) lets desk apps hear
the tablet as "DeskLink microphone" (docs/desktop.md). The first time it is
turned on, the app asks for `RECORD_AUDIO`; if that is refused the switch
stays off and a message says that the permission is needed to send your
voice to the desk and where to allow it (Apps › DeskLink › Permissions),
since Android stops showing the dialog after repeated refusals. Turning it
on sends `mic {"on":true}` and later hellos ask with `mic` (only while the
permission is still granted, `MicPolicy.ask`); capture starts once
`session.mic` reports the microphone, and turning the switch off stops
capture at once, before the host answers. The switch shows on only while
both the user wants it and the host plays it.

`MicCapture` records with `AudioRecord` at 48 kHz mono from the
`VOICE_COMMUNICATION` source, which applies the platform's echo
cancellation against what the tablet plays (otherwise the desktop's sound
coming out of the tablet would go back into a call) and its noise
suppression; `AcousticEchoCanceler` and `NoiseSuppressor` are also switched
on where the device offers them. It feeds 20 ms chunks of 16-bit PCM to
MediaCodec's `audio/opus` encoder (built in from Android 10, so always there
at the minimum SDK 31) at 32 kbit/s; the encoder emits 20 ms packets, and
its header buffer (flagged codec config, "OpusHead" or Android's "AOPUSHDR")
is never sent (`MicFrames`). Each packet goes out as a type-7 datagram on
the session's `UdpTransport` with the time its chunk was recorded, sent at
once from the microphone's thread rather than through the input sender:
a `type` message holds that thread for at least 20 ms (below), longer than
the host's jitter buffer waits for a late frame. Sealing and sequence
numbers are safe from any thread. One thread does all of it and frees the
recorder and codec when it stops.

Capture runs only while the switch is on, the host reports `mic`, the
permission is granted and the activity is started (`MicPolicy.capture`); it
stops when the session ends or reconnects (and starts again with the new
session), when the switch goes off and when the app goes to the background
(`onStop`), and resumes when it comes back. There is no foreground service:
Android 11 and later give a background app silence from the microphone
unless a foreground service of type `microphone` holds it, and the video,
decoder and input stop in the background anyway, after which the host ends
the session for lack of datagrams. Keeping only the microphone alive would
need the whole UDP session in a foreground service with a notification; the
desk's virtual microphone simply goes quiet while the tablet is away.

## Wi-Fi

While a session streams, the app holds a `WIFI_MODE_FULL_LOW_LATENCY` Wi-Fi
lock (permission `WAKE_LOCK`), so the radio does not doze between packets and
add jitter; Android applies it only while the app is in the foreground with
the screen on, and the app releases it on disconnect.

## Reconnecting

When the WebSocket of a session that has streamed fails (the tablet changed
networks, the host's daemon restarted), or no authenticated datagram has
arrived for 10 s although the WebSocket is up (the host sends a keepalive
every 2 s besides video), `StreamSession` connects again instead of ending.
It makes ten attempts after 0.5, 1, 2, 3, 5, 5, 8, 10, 10 and 15 s, about a
minute in all, with "Reconnecting (n of 10)" on screen and the auto level it
had settled on, but at most one below the top. Each attempt asks every known address for `/health` with the
pinned certificate, as Connect does, opens the WebSocket and UDP against the
address that answered and saves it as the host's main address, so a tablet
that moved from the LAN to the VPN reconnects at the VPN address. An attempt
succeeds when the host's first datagram arrives; without one within 5 s the
next attempt runs. A first connection whose UDP never worked tries the
host's other addresses the same way before it fails, and fails at once when
only that address answers. Each attempt's `hello` carries the scale and keep
awake as last changed from the HUD (`HelloBuilder`), not as the session
started, and a frame-rate cap in force goes out again as `max_fps` once the
attempt's `session` arrives (Panel rate). The WebSocket of an attempt that was given up stays open
until a later connection has replaced its session on the host (or the
session ends), so the host hands the session over instead of ending it and
locking the desk in between. If the app itself did not run for over 3 s
(Android froze it in the background), the 5 s and 10 s deadlines start again
instead of counting that pause as silence.

A rejected token (HTTP 401), a normal close by the host and Disconnect end
the session as before; so does an error message from the host during an
attempt, and an error with reason `replaced` at any time: another device took
the session over, and reconnecting would take the desk back from it. Callbacks and the stored socket belong to one attempt: callbacks from
an attempt that was given up are ignored, and its socket, if it opens late,
is closed without sending `hello`. The Android clipboard is sent when the app
regains focus (Android only lets the focused app read it), and text from the
host replaces the Android clipboard.

## Clipboard images

Every `hello` says the tablet takes clipboard images up to 8 MiB
(`clipboard_images`, `ClipboardImages.DEFAULT_LIMIT`), and the host says in
`session` how large an image it takes; the tablet sends images only to a host
that says so, so an older host keeps getting text as before, and an image
clip goes to it as text (its URI) as it always did. An image travels as
`clipboard` messages of at most 512 KiB of PNG each (docs/protocol.md), so the
WebSocket's pings get through between them. `ImageAssembler` joins the host's
parts, with the same rules and test cases as the host's: a part out of order,
a part count that changes, more than 8 MiB or a result that is not a PNG drops
the image. A complete image from the host is saved as the only file in the
cache's `clipboard/` directory (deleting the one before) and put on the
Android clipboard as a content URI of the app's `FileProvider`
(`${applicationId}.files`, which shares only that directory and grants the
pasting app read access); apps paste images only from a content URI.

When the clip's first item has a URI and the clip offers an `image/*` type,
Send clipboard and the automatic push on focus read it through the
`ContentResolver` off the main thread: a PNG is sent as it is, any other type
is decoded with `BitmapFactory` and encoded as PNG (more than 16 megapixels
counts as too large, rather than risking the memory to decode it). An image
larger than the host's limit is not sent; only Send clipboard says so, as for
long text. The automatic push skips an image it already sent (same URI and
clip timestamp) and one that came from the host (the app's own `FileProvider`
URI). The parts go out one at a time, each once OkHttp's queue for the
WebSocket is empty (`ImagePacer`), so a `type` message sent meanwhile waits
behind at most one part (some 700 KB), not seconds of image on a slow link,
and keys typed after it are not held back that long; a part that waits 30 s,
or a connection that changes, gives the image up. While an earlier image is
still going out, the next is refused with a message, so two images' parts
never interleave.

## Sending files

DeskLink is a share target (`ShareActivity`, `ACTION_SEND` and
`ACTION_SEND_MULTIPLE` for any MIME type): choosing DeskLink in Android's
share sheet sends the shared files to a paired computer's download folder,
without a session and without the main screen. Only `content://` URIs are
taken, since a `file://` URI would let another app make DeskLink send the
app's own private files. Shared text without a file goes as a `.txt` file
named after the share's subject, or `Shared text.txt`. Each file's name and
size come from the sharing app's `OpenableColumns`; the name is cut to its
last path element and, when there is none, becomes `file` with the extension
of its MIME type (`SharedNames`). The screen lists the files and one **Send to
<host>** button per paired host (one entry per machine, as on the host list);
with exactly one paired host it starts at once, and with none it says to pair
first.

Sending asks every address of the host for `/health` with the pinned
certificate at once and uses the first that answers, as Connect does, but
does not wake a sleeping host; if none answers, the screen says so and the
buttons stay for another try. The files then go one after the other, because
the host takes one upload per device at a time, each with a fresh bearer token
from its own `/health`: `POST /files?name=…` with the file streamed from the
`ContentResolver` in 64 KiB writes, never held in memory
(`ControlClient.sendFile`; Content-Length when the size is known, chunked
otherwise), and with `Expect: 100-continue`, so a file the host refuses is
not sent and its answer is not lost to a reset connection. Each file shows its progress (bytes, and percent when the size is
known, updated every 256 KiB) and then "Saved as <name>" with the name the
host chose, or the error: the host's own message for a file that is too
large, a full disk or an upload already running, and our own for a rejected
token (pair again) or a host too old to receive files (404 or 405,
`UploadAnswer`). After a failure or a cancel, the buttons send the files not
saved yet again. The transfer lives in `ShareViewModel`, so rotating the
screen keeps it; Cancel, Back or closing the screen cancels it, which ends the
request. Switching to another app leaves it running while Android keeps the
app alive. Sending does not ask for the fingerprint or PIN even with that
setting on: it does not unlock the desk or give control of it.

## Wake-on-LAN

`/health` reports the MAC of the interface holding the advertised address;
the app stores it. **Connect** on a host that does not answer `/health` sends
magic packets every 10 s and polls `/health` every 2 s for up to 90 s, then
starts the session; the host's status shows "Waking up…" meanwhile. **Wake** sends one
round of packets; it shows beside Connect while the host does not answer
and its MAC is known. A host whose address refuses the connection is on, but
desklinkd is not running there: typically the login screen after a reboot
or after a crash without automatic login (docs/desktop.md, "Staying up").
**Check** and **Connect** say so at once, instead of "no answer" or a
90-second wake: a machine that refuses is already on. A wake that started
because nothing answered still waits the full 90 s once the machine refuses,
because during an automatic login desklinkd starts a few seconds after the
network does, and if its last poll was refused it ends with the same message
instead of "did not wake up". Packets go to UDP port 9 at 255.255.255.255 (the LAN the
tablet is on), the host's /24 broadcast and the host address. Across WireGuard the broadcast only reaches the
LAN if the router forwards it; the FRITZ!Box can instead wake a device from
its own web interface.

## Fingerprint or PIN

Connecting unlocks the desk without its password, so anyone holding the
unlocked tablet could unlock it. With **Fingerprint or PIN to
connect** on (Settings › Security, off by default, `HostStore.appLock`), Connect, which also
wakes a host that does not answer, first shows Android's `BiometricPrompt`
with a strong biometric or the device's PIN, pattern or password allowed
(`BIOMETRIC_STRONG | DEVICE_CREDENTIAL`, permission `USE_BIOMETRIC`). Only
success connects; cancelling does nothing, and any other error shows its
message. The session's own reconnects and the HUD's screen switch do not
ask again, because the Connect that started the session did, and Wake does
not ask, because waking leaves the desk locked. Turning the setting off asks
too, or anyone holding the tablet could switch it off and connect. Without
a screen lock (`KeyguardManager.isDeviceSecure` false) Android has no
credential to ask for: the switch shows off and disabled with a line saying
so, nothing asks, and the saved choice applies again once a screen lock is
set up. `AppLock.asks` decides from the setting, the screen lock and what
triggered the check (Connect, a reconnect, or turning the setting off).

## Connection test and logs

The host's details on the main page hold **Test connection** and **Share logs**.

**Test connection** asks every address of the host
(`HostProfile.candidates()`) for `/health` at once with the pinned
certificate, as Connect does, but waits for all of them, up to 5 s each
(the 3 s connect timeout usually ends an unreachable one first). A dialog
lists each address: answered in N ms with the host name and desklinkd
version, refused (the machine is on without desklinkd), timed out, or the
innermost error message, such as a certificate that does not match. The
quickest answer is marked "fastest", the address Connect would pick. For an
address that answered, the test also sends an authenticated UDP probe to the
host's media port (`udp_port` in `/health`; `UdpProbe`, up to three tries
within 1.5 s) and shows whether the stream can reach it: answered in N ms,
no answer (a firewall or the VPN drops UDP there, so the address cannot
stream; its dot turns red), or not testable with an older host. The test
saves nothing.

The host's details say when desklinkd and the app speak different protocol
versions (`protocol` in `/health`, `ControlProtocol.advice`): which side to
update, because the newer side's features only hide.

**Share logs** reads the app's own recent log, which an app may do without
a permission (`logcat -d -t 2000 --pid=<own pid> -s DeskLink`, given 5 s),
and hands it to Android's share sheet as plain text (`ACTION_SEND`,
`text/plain`). `LogReport` puts the app version, Android version, device
model, the chip (`Build.SOC_MANUFACTURER`, `SOC_MODEL`) with the media
performance class and the hardware AV1 and HEVC decoders, the panel's
resolution, current rate, offered rates and power saving, and the time
first, keeps the newest lines within 100 KB, because the
share sheet's Binder transaction fails at about 1 MB, and says how many
older lines it left out. Only the DeskLink tag is read: the app's own lines
hold no token, pairing key or clipboard text, while nobody checked the
framework's lines in the same process. A bearer token or a Keystore-wrapped
key that a line might still quote (a parse error of the saved hosts quotes
their JSON) is masked anyway. So that the log tells how a stream went, a
session logs its decoder's report when each decoder starts, the panel's mode
and power saving whenever they change, every change of the frame-rate cap,
and every 30 s a line of the stream's numbers ("stream: 118 fps, 22.1 Mbps,
4 ms RTT, 0.0 % loss, 14 ms delay, decode 3.1 ms mean 5.2 ms p95, 0 frames
lost, 1 keyframe requests").

## Commands

```bash
make apk            # bin/desklink.apk
make android-check  # APK, JVM tests, lint
make install        # adb install -r bin/desklink.apk
make screenshots    # the main page, settings and stats overlay as PNGs in bin/screenshots
adb logcat -s DeskLink
```

Devices tested: none yet; the JVM tests cover the protocol, crypto, frame
assembly, the replay window, the receive deadline, held-input release, the
key map, gestures, how touches map through the zoom, the fingerprint-or-PIN
decision, the connection test's rows, the shared log's trimming, clipboard
image parts, the upload URL and answers, shared file names, the stream
size, the video's place and the stats' geometry, rotation debouncing, the
renderer's colour conversion against the host's, its chroma arithmetic on a
decoded frame, the SurfaceTexture crop mapping and when it gives up, the
decoder's setup retries, hang detection, rebuild limit, decode timing and
rating, the panel judgement and frame-rate cap, S-Pen tap steadying, the
main page's host selection and the sound's catch-up. The renderer's speed,
colours and zoom on the Tab S9 have
not been checked on the device, nor have unbuffered input, the panel
judgement against Samsung's motion smoothness settings, the smaller sound
buffer, the receive thread's priority and the redesigned main page, settings
and stats overlay (seen only as `make screenshots` renders them).
