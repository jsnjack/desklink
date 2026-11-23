# Android client

Package and application ID `com.desklink.client`; minimum SDK 31, target SDK
35, built for the Galaxy Tab S9 (Snapdragon 8 Gen 2, hardware AV1 and HEVC
Main10 decoding). The APK from `make apk` is `bin/desklink.apk`, signed with
the local debug key.

## Connecting

A host is one machine, recognised by its certificate fingerprint, with the
address that answered last and a list of others (`HostProfile.addresses`):
those in the pairing QR code, in every `/health` answer, and those mDNS
discovery finds. The health check and Connect ask every address for
`/health` at once with the pinned certificate and use the first that answers,
so a LAN address beats a VPN one, and the winner becomes the host's main
address. Entries with the same fingerprint (a machine paired on two networks)
are merged into one at startup and after pairing: the oldest entry survives
with its name and remembered levels and takes the new pairing, MAC and
address. While the host list is shown, NSD browses for `_desklink._tcp`: a
known host found at a new address gets it added; an unknown one appears as
"Found on this network" with a Pair button, its certificate pinned from the
announcement, until NSD has reported it lost on every network it was found
on. Wake-on-LAN sends towards every address and fails only when no packet
could be sent. **Forget** asks for confirmation, then asks a paired host to
forget this tablet (`DELETE /pair`, up to 8 s) and removes the host here
whether or not it answered; if it did not, the message says which
`desklinkd peers remove` to run on it.

`AppViewModel` owns the host profiles (`HostStore`, SharedPreferences, peer key
wrapped by an AES-GCM Keystore key), the stream settings and the running
`StreamSession`. A session calls `/health`, stamps a bearer token with the
host's time, opens the WebSocket, sends `hello`, and on `session` derives the
media keys and starts `UdpTransport`. The decoder exists while both the
session description and the `SurfaceView` surface are available, and it is
rebuilt when the size or codec changes.

Presets: Auto (the default: the host adapts size and frame rate from
2560×1600 at 120 fps down, see docs/desktop.md), Crisp 2560×1600 QP 18 at
120 fps, Balanced 1920×1200 QP 20 at 60 fps, Office Saver 1920×1200 QP 24 at
60 fps. With Auto the app remembers the level per host and network (its kind,
VPN, Wi-Fi, LAN or cellular, plus its default gateway, so home and office
Wi-Fi differ without the location permission an SSID needs; hence
`ACCESS_NETWORK_STATE`) by its size and frame
rate, and starts the next session there (a level saved before networks were
told apart by gateway serves until the network has its own); the HUD shows
the level as
"Auto 2/4". Clipboard text over 256 KB is not sent, since the host would
refuse it. The codec defaults to AV1 when the
device has a hardware AV1 decoder.

## Video

`HardwareDecoder` picks a hardware decoder for the MIME type, preferring one
that advertises `FEATURE_LowLatency`, and configures `KEY_LOW_LATENCY = 1`,
`KEY_OPERATING_RATE = 120`, realtime priority, an 8 MB input buffer and
Qualcomm's `vendor.qti-ext-dec-low-latency.enable`. It runs in asynchronous
mode on its own thread and renders each output buffer as soon as it is ready.
The surface gets a frame-rate hint, and the activity asks for the display's
highest refresh rate while streaming.

`FrameAssembler` (in `UdpTransport.kt`) reassembles fragments and releases
frames strictly in frame order. When the expected frame is still incomplete
and the newest frame is two IDs ahead, it counts the frame as lost, stops
releasing frames until a complete keyframe arrives, and the transport sends a
telemetry report with `NeedIDR = 1` at once. The decoder is never flushed, so
recovery costs about one round trip plus the keyframe's transmission.

A transient codec error asks for a keyframe. Any other error, or a codec that
rejects input, marks the decoder failed, and the session replaces it with a new
one built on the same surface.

## Input

All input goes through `InputView`, a transparent focusable view over the
video, and `InputMapper`:

- **Hardware keyboard:** `MainActivity.dispatchKeyEvent` hands every key to
  the mapper before Android, unless the HUD is open. Hardware keyboards report
  the evdev code as the scan code, which is sent unchanged for keys that type
  a character, so the host's layout decides; otherwise `LinuxKeyCodes` maps
  the key code. A function key the keyboard's layout defines keeps its
  function: the Book Cover Keyboard Slim's Esc key has the scan code of `
  (41), which Samsung's layout makes Escape. Volume and power keys stay with Android,
  and so does Back from non-keyboard sources (the system back gesture opens
  the HUD). Android keeps Win (Meta), every Win+key combination and Alt+Tab
  before any app sees them (checked with `getevent` on Android 16: the
  keyboard sends them, the activity never gets them). `KeyCaptureService`, an
  accessibility service the user switches on once ("Win and Alt+Tab" in the
  settings card opens its page), receives physical keys before those
  shortcuts and hands them to `KeyCapture`, which takes them for the desktop
  only while the activity is resumed and focused, a session runs and the HUD
  is closed; otherwise they go on to Android. It reads no window content and
  gets no accessibility events. An APK installed from a browser may need
  "Allow restricted settings" in its App info before Android lets the
  service be switched on.
- **Mouse and trackpad:** `requestPointerCapture()` while streaming. Relative
  mouse motion (`AXIS_RELATIVE_X/Y` with history), wheels (×120 per notch)
  and buttons go straight through. A captured touchpad reports finger
  positions, so it runs through its own `TouchGestureEngine`, scaled so a full
  swipe moves 1600 host pixels; a physical click suppresses the tap.
- **Touchscreen**, by the **Touch** setting:
  - **Tap to click** (default, `DirectTouchEngine`): positions go out as
    absolute pointer records (kind 6, 0..65535 across the video). A tap clicks
    where the finger touched; a second tap within 300 ms and 60 px reuses the
    first position so the desktop sees a double click; a press held 500 ms
    without moving right-clicks there; one finger dragged past the slop holds
    the left button from where it went down (selecting text, moving windows);
    two fingers move the pointer to their centre and scroll naturally, or
    right-click there on a tap; a three-finger tap toggles the HUD.
  - **Trackpad** (`TouchGestureEngine`): one finger moves the cursor relative
    to where it is (speed setting), a tap clicks at the cursor, a double tap
    held down drags with the left button, two fingers scroll or right-click on
    tap, a three-finger tap toggles the HUD.
  A gesture finishes in the mode it started in.
- **S-Pen:** hover and touch become absolute pen events normalised to the video
  area, with historical samples batched into one packet; the barrel button is
  a right click. Each touching sample carries its pressure, and a tilt record
  goes before a sample whose tilt moved by half a degree or more since the
  last one sent (`PenAxes` converts Android's tilt and orientation into
  tilt towards x and y, as Chromium does for web pointer events). Android sends HOVER_EXIT just before a hovering pen touches
  down, so "left proximity" is sent only if no pen event follows within 80 ms.
- **Soft keyboard:** `InputView` is the text field. Committed text is typed as
  key taps on a US layout (the host layout is assumed to match); the bar of
  Esc, Tab, Ctrl, Alt, Super and arrows sits above it, and Ctrl, Alt and Super
  latch for the next key.

`UdpTransport` sends input on one sender thread, in packets of at most 104
records, and every 250 ms it sends a snapshot of the held keys, buttons and pen
state (`HeldInput`) so the host can lift anything whose release was lost.
Typed text keeps each character's press and release in one packet. After the
transport closes, further sends are dropped instead of failing.

## HUD

A pill (☰) on a side edge opens a panel with `RTT | FPS | Mbps`, the codec,
size, quantizer and how the desktop was matched; Keyboard, Use as trackpad /
Tap to click (switches the Touch setting at once), Send clipboard, Get
clipboard, Use own screen / Mirror a monitor and Show/Hide stats, wrapping
onto as many lines as they need; the app drawer with the pinned app first
and a search field; and Disconnect. Drag the pill to move it: when let go
it sticks to the nearer side edge at that height, and the place is
remembered. Only the pill takes touches.

The stats overlay (on by default) sits in the top-left corner as a small dot
coloured by the quality grade; a tap on it expands it and another collapses
it. Expanded it shows the grade from round trip and loss (Excellent under 30 ms and 0.5 %, Good under
80 ms and 2 %, Fair under 150 ms and 5 %, else Poor; the host starts adapting
at 2 % loss), FPS, Mbps, round trip, loss, the delay from capture on the host
to the decoder releasing the frame to the screen (averaged over a second;
the screen adds up to one refresh, about 8 ms), the data received in this session
(kept across reconnects), and the stream format and auto level. It is drawn
without a Material `Surface`, which would swallow touches meant for the
desktop; only the dot or the expanded box takes taps.

## Screen

**Screen** in the settings chooses what a session shows (`hello.screen`):
**Auto** (default) gives the tablet its own 2560×1600 screen at 200 % when the
computer is locked as it connects, and otherwise mirrors the monitor closest
to the tablet's shape without changing anything at the desk; **Own screen**
always adds the tablet's screen (the desk's monitors flicker once); **Mirror**
never does. While streaming, the HUD's **Use own screen** / **Mirror a
monitor** button saves the other choice and connects again.

## Sound

With **Play the desktop's sound here** on (the default), `hello.audio` asks
for sound and `AudioPlayer` plays it: each Opus packet is decoded by
`MediaCodec` (`audio/opus`, configured with a generated OpusHead, 2.5 ms
pre-skip and 80 ms seek pre-roll) and written to a `PERFORMANCE_MODE_LOW_LATENCY`
`AudioTrack`. Playback starts once 30 ms is queued; while more than 80 ms is
queued new audio is dropped, so sound cannot drift behind the picture. Late
or repeated packets (by the 16-bit counter) are dropped. The decoder runs on
its own `HandlerThread`. The setting applies from the next connection.

## Wi-Fi

While a session streams, the app holds a `WIFI_MODE_FULL_LOW_LATENCY` Wi-Fi
lock (permission `WAKE_LOCK`), so the radio does not doze between packets and
add jitter; Android applies it only while the app is in the foreground with
the screen on, and the app releases it on disconnect.

## Reconnecting

When the WebSocket of a session that has streamed fails (the tablet changed
networks, the host's daemon restarted), `StreamSession` connects again instead
of ending: ten attempts after 0.5, 1, 2, 3, 5, 5, 8, 10, 10 and 15 s, about a
minute in all, with "Reconnecting (n of 10)" on screen and the auto level it
had settled on. A rejected token (HTTP 401), a normal close by the host and
Disconnect end the session as before; so does an error message from the host
during an attempt. Each attempt's WebSocket callbacks carry a generation
number, so late callbacks from a dead connection are ignored. The Android clipboard is sent when the app regains focus
(Android only lets the focused app read it), and text from the host replaces
the Android clipboard.

## Wake-on-LAN

`/health` reports the MAC of the interface holding the advertised address;
the app stores it. **Connect** on a host that does not answer `/health` sends
magic packets every 10 s and polls `/health` every 2 s for up to 90 s, then
starts the session; the card shows "Waking up…" meanwhile. **Wake** sends one
round of packets. Packets go to UDP port 9 at 255.255.255.255 (the LAN the
tablet is on), the host's /24 broadcast and the host address. Across WireGuard the broadcast only reaches the
LAN if the router forwards it; the FRITZ!Box can instead wake a device from
its own web interface.

## Commands

```bash
make apk            # bin/desklink.apk
make android-check  # APK, JVM tests, lint
make install        # adb install -r bin/desklink.apk
adb logcat -s DeskLink
```

Devices tested: none yet; the JVM tests cover the protocol, crypto, frame
assembly, key map and gestures.
