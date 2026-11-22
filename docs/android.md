# Android client

Package and application ID `com.desklink.client`; minimum SDK 31, target SDK
35, built for the Galaxy Tab S9 (Snapdragon 8 Gen 2, hardware AV1 and HEVC
Main10 decoding). The APK from `make apk` is `bin/desklink.apk`, signed with
the local debug key.

## Connecting

`AppViewModel` owns the host profiles (`HostStore`, SharedPreferences, peer key
wrapped by an AES-GCM Keystore key), the stream settings and the running
`StreamSession`. A session calls `/health`, stamps a bearer token with the
host's time, opens the WebSocket, sends `hello`, and on `session` derives the
media keys and starts `UdpTransport`. The decoder exists while both the
session description and the `SurfaceView` surface are available, and it is
rebuilt when the size or codec changes.

Presets: Crisp 2560×1600 QP 18 at 120 fps, Balanced 1920×1200 QP 20 at 60 fps,
Office Saver 1920×1200 QP 24 at 60 fps. The codec defaults to AV1 when the
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
  the evdev code as the scan code, which is sent unchanged; otherwise
  `LinuxKeyCodes` maps the key code. Volume and power keys stay with Android,
  and so does Back from non-keyboard sources (the system back gesture opens
  the HUD). Android keeps some system shortcuts (for example Meta-based ones
  on Samsung) before any app sees them.
- **Mouse and trackpad:** `requestPointerCapture()` while streaming. Relative
  mouse motion (`AXIS_RELATIVE_X/Y` with history), wheels (×120 per notch)
  and buttons go straight through. A captured touchpad reports finger
  positions, so it runs through its own `TouchGestureEngine`, scaled so a full
  swipe moves 1600 host pixels; a physical click suppresses the tap.
- **Touchscreen:** `TouchGestureEngine` — one finger moves the cursor
  (speed setting), a tap clicks, a double tap held down drags with the left
  button, two fingers scroll naturally or right-click on tap, a three-finger
  tap toggles the HUD.
- **S-Pen:** hover and touch become absolute pen events normalised to the video
  area, with historical samples batched into one packet; the barrel button is
  a right click. Android sends HOVER_EXIT just before a hovering pen touches
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

A pill at the right edge opens a panel with `RTT | FPS | Mbps`, the codec,
size, quantizer and how the desktop was matched; Keyboard, Send clipboard and
Get clipboard; the app drawer with the pinned app first and a search field;
and Disconnect. The Android clipboard is sent when the app regains focus
(Android only lets the focused app read it), and text from the host replaces
the Android clipboard.

## Wake-on-LAN

`/health` reports the MAC of the interface holding `advertise_ip`; the app
stores it and "Wake" sends a magic packet to the subnet broadcast and to the
host address on UDP port 9. Across WireGuard the broadcast only reaches the
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
