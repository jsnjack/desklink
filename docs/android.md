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
pairing QR**, **Pair** or **Pair again** on a host, and **Pair** on a host
found on the network all open the scanner. The code decides which host it
pairs (`planPairing`): the saved host with the code's certificate (the same
machine on any network), else the host whose Pair was tapped, else a saved
host at the code's address and port, else a new host named after the
announcement it was found by. The code's certificate always wins; when the
host found on the network announced another one, or the chosen host had
another one pinned, the result message says so. **Add host** saves an
address only, for the health check; pairing it still needs the QR code.

A host is one machine, recognised by its certificate fingerprint, with the
address that answered last and up to seven others (`HostProfile.addresses`,
newest first, `MAX_ADDRESSES` = 8 in all): those in the pairing QR code, in
every `/health` answer of a paired host, and those mDNS announces once they
answered. Unspecified, loopback, multicast and broadcast IP literals are
never kept or probed. The health check and Connect ask every address for
`/health` at once with the pinned certificate and use the first that answers,
so a LAN address beats a VPN one, and the winner becomes the host's main
address. An unpaired host has no certificate from a QR code to pin, so for
it only the address that answered is kept, not the addresses or MAC its
`/health` reports. Entries with the same fingerprint (a machine paired on
two networks) are merged into one at startup and after pairing: the oldest
entry survives with its name and remembered levels and takes the new
pairing, MAC and address. While the host list is shown, NSD browses for
`_desklink._tcp`. Anyone on the network can announce any name and
fingerprint, so an announcement proves nothing: a paired host announced at a
new address gets that address only once it answers `/health` there, on the
saved port, with the pinned certificate; an unknown one appears as "Found on
this network" with a Pair button that opens the scanner, until NSD has
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
recovery costs about one round trip plus the keyframe's transmission. A
partial frame keeps one parity record per group, indexed by group, and
ignores a repeat; the first record fixes the frame's stride, and a record
with another stride is ignored. A fragment or parity record that arrives
checks only its own group, counting the missing members before it rebuilds
any payload, so recovering one fragment of a keyframe costs about as much as
receiving it whole (as on the laptop, `client.FrameAssembler`).

Before a datagram counts for anything, `UdpTransport` checks its sequence
number against a replay window, the same as the host's (`ReplayWindow`): a
number already seen, or 1024 or more behind the newest, is dropped. So a
duplicated or replayed datagram cannot skew loss, the clock estimate or the
receive deadline, and cannot feed the assembler twice.

A transient codec error asks for a keyframe. Any other error, or a codec that
rejects input, marks the decoder failed, and the session replaces it with a new
one built on the same surface. A codec that fails to configure or start is
released at once. Hardware decoders are few, so replacing a decoder waits (up
to 1 s) until the old codec is released before the new one is created.

## Input

All input goes through `InputView`, a transparent focusable view over the
video, and `InputMapper`:

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
  accessibility service the user switches on once ("Win and Alt+Tab" in the
  settings card opens its page), receives physical keys before those
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
- **Touchscreen**, by the **Touch** setting:
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
Typed text keeps each character's press and release in one packet. When keys
stop reaching the session (the window loses focus, the activity pauses, the
HUD opens or the soft keyboard is shown), `releaseHeld` sends a release for
every held button, the pen and every held key, then the now empty snapshot.
It runs on the same sender thread as input, so it follows everything sent
before it, and a key let go outside DeskLink does not stay down on the
desktop. After the transport closes, further sends are dropped instead of
failing; sends queued before the close still go out.

## HUD

A pill (☰) on a side edge opens a panel in three groups. At the top, the
host's name, the link grade with round trip and delay, and FPS, bitrate,
codec, size and auto level. Then a grid of icon tiles: Keyboard, Trackpad
(highlighted while the touchscreen acts as a trackpad), Apps (Win+A, GNOME's
app grid), Overview (Win), Send clipboard, Get clipboard, Stats and PrtSc (the
Print key, which Android keeps for its own screenshot); then
Screen (Own screen or Mirror, which reconnects), how the desktop was matched,
and on the own screen Scale with − and + in 25 % steps from 100 to 200 %
(saved, sent at once as `scale` and in the next `hello`) and a Primary screen
switch, which moves GNOME's top bar and overview onto the tablet's screen for
this session (`primary`; not saved, since beside an unlocked desk someone may
be using it); then Keep awake
after disconnect (Off, 1 h, 4 h, or ∞ until a client connects again; saved, sent as `keep_awake` and
in the next `hello`). Disconnect stays at the bottom and the middle scrolls
when the panel is taller than the screen. There is no app list: GNOME's own
app grid and search do that job. Drag the pill to move it: when let go
it sticks to the nearer side edge at that height, and the place is
remembered. Only the pill takes touches.

The stats overlay (on by default) sits in the top-left corner as a small dot
coloured by the quality grade; a tap on it expands it and another collapses
it. Expanded it shows the grade from round trip and loss (Excellent under 30 ms and 0.5 %, Good under
80 ms and 2 %, Fair under 150 ms and 5 %, else Poor; the host starts adapting
at 2 % loss), FPS, Mbps, round trip, loss, the delay from capture on the host
to the decoder releasing the frame to the screen (averaged over a second;
the screen adds up to one refresh, about 8 ms), the data received in this session
(kept across reconnects), and the stream format and auto level. Below
those, `StatsCharts` draws the last minute of FPS, Mbps, round trip, loss and
delay as small line charts, two to a row, each on its own scale: it names
the metric, shows the current value and the top of its scale (the smallest
round number above both the values and a minimum, such as 2 % for loss, so a
quiet link does not fill the chart), and puts the newest sample at the right
edge. Delay has gaps until a frame has been shown. The history
(`StreamSession.history`, 60 samples) comes from `UdpTransport.samples`, one
`LinkStats` a second; the 250 ms loss-only updates of `stats` are left out,
and the two update `stats` atomically (`MutableStateFlow.update`), so one
never overwrites the other's change. It spans reconnects. It is drawn
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
networks, the host's daemon restarted), or no authenticated datagram has
arrived for 10 s although the WebSocket is up (the host sends a keepalive
every 2 s besides video), `StreamSession` connects again instead of ending.
It makes ten attempts after 0.5, 1, 2, 3, 5, 5, 8, 10, 10 and 15 s, about a
minute in all, with "Reconnecting (n of 10)" on screen and the auto level it
had settled on. Each attempt asks every known address for `/health` with the
pinned certificate, as Connect does, opens the WebSocket and UDP against the
address that answered and saves it as the host's main address, so a tablet
that moved from the LAN to the VPN reconnects at the VPN address. An attempt
succeeds when the host's first datagram arrives; without one within 5 s the
next attempt runs. The WebSocket of an attempt that was given up stays open
until a later connection has replaced its session on the host (or the
session ends), so the host hands the session over instead of ending it and
locking the desk in between. If the app itself did not run for over 3 s
(Android froze it in the background), the 5 s and 10 s deadlines start again
instead of counting that pause as silence.

A rejected token (HTTP 401), a normal close by the host and Disconnect end
the session as before; so does an error message from the host during an
attempt. Callbacks and the stored socket belong to one attempt: callbacks from
an attempt that was given up are ignored, and its socket, if it opens late,
is closed without sending `hello`. The Android clipboard is sent when the app
regains focus (Android only lets the focused app read it), and text from the
host replaces the Android clipboard.

## Wake-on-LAN

`/health` reports the MAC of the interface holding the advertised address;
the app stores it. **Connect** on a host that does not answer `/health` sends
magic packets every 10 s and polls `/health` every 2 s for up to 90 s, then
starts the session; the card shows "Waking up…" meanwhile. **Wake** sends one
round of packets. A host whose address refuses the connection is on, but
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

## Commands

```bash
make apk            # bin/desklink.apk
make android-check  # APK, JVM tests, lint
make install        # adb install -r bin/desklink.apk
adb logcat -s DeskLink
```

Devices tested: none yet; the JVM tests cover the protocol, crypto, frame
assembly, the replay window, the receive deadline, held-input release, the
key map and gestures.
