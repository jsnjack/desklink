# AGENTS.md

> See [AGENTS.universal.md](./AGENTS.universal.md), [AGENTS.go.md](./AGENTS.go.md) and [AGENTS.android.md](./AGENTS.android.md) for shared conventions.
> Refresh: `make standards`

---

## Overview

DeskLink streams a Fedora GNOME (Wayland) desktop to a Galaxy Tab S9 over
the office VPN and sends the tablet's keyboard, mouse, touch and S-Pen input
back. The repository holds three programs:

- `host/` — `desklinkd`, a Go daemon run as a systemd user service. It
  captures the monitor through Mutter's screen-cast API, encodes it on the GPU
  (VA-API AV1 or 10-bit HEVC, CQP) with GStreamer, sends it and the desktop's
  sound (Opus) as encrypted UDP and injects input through `/dev/uinput`.
- `android/` — the Kotlin/Compose client: pairing, host profiles, low-latency
  `MediaCodec` decoding onto a `SurfaceView`, and the input engine.
- `host/cmd/desklink` — the GTK 4 laptop client, in the same Go module as the
  daemon so it shares the protocol and crypto packages.

The control plane is TLS 1.3 HTTPS/WebSocket on port 9443; video and input
use ChaCha20-Poly1305 UDP on port 9444, at most 1280 bytes per datagram.

---

## Where the details are

| Working on | Read |
|---|---|
| packet layout, control messages, pairing and key derivation | [docs/protocol.md](docs/protocol.md) |
| screen capture and the client's own screen, changing the stream during a session, typing text, sound, unlock, staying up (restarts, watchdog, GDM automatic and timed login), Wake-on-LAN, clipboard (text and images), files from clients, notifications, app launching, encoder, exact tiles, the commands' terminal output | [docs/desktop.md](docs/desktop.md) |
| the Android app: decoding, the GPU renderer (Sharp Colour), Exact Text, frame recovery, input gestures, main page, settings, stats overlay, screenshots, share target | [docs/android.md](docs/android.md) |
| the laptop client: connecting, GTK viewer, decoding, input, sending files | [docs/laptop.md](docs/laptop.md) |
| threat model, what pairing protects against, file permissions | [docs/security.md](docs/security.md) |
| how it looks and reads: colours, type, the icon (`docs/brand/`), screens, a session's minimal UI, UX rules, voice, feature names, README structure, terminal output | [docs/design.md](docs/design.md) |

---

## Package map

```
host/cmd/desklinkd/        cobra entry point: daemon (root) and `pair`
host/cmd/desklink/         laptop client entry point: GTK window (root) and `pair`
host/internal/client/      laptop client without GUI: host store, pairing, session, UDP transport
host/internal/viewer/      laptop client GTK 4 / libadwaita window and GStreamer decoder
host/internal/config/      config.json, peers.json, state.json (atomic 0600 writes)
host/internal/crypto/      PIN pairing (ECDH P-256 + HKDF), bearer tokens, session keys
host/internal/net/         packet and tile codecs, UDP media server, TLS control server, file uploads, clipboard image parts, local Unix-socket API
host/internal/desktop/     Mutter screen cast and remote desktop (typing text as keysyms), virtual audio output and microphone, login1/ScreenSaver/Mutter D-Bus, GDM login settings, Wake-on-LAN readiness, clipboard, notifications, download folder, .desktop apps
host/internal/input/       uinput keyboard, mouse, pen and absolute pointer; evdev key names; copy shortcuts
host/internal/video/       go-gst video and Opus audio pipelines (the client's microphone with its jitter buffer too), the scan branch for exact tiles, the quality controller
host/internal/tiles/       exact tiles: settled-tile detection, per-frame invalidation, paced sending
host/internal/daemon/      wiring and one remote session's lifecycle
host/internal/doctor/      checks that turn facts about this computer into ✓, ! or ✗ lines with the fixing command (`pair`, `doctor`); the probes for ports, firewalld and the user unit
host/internal/logging/     slog setup for --debug / --trace
host/deploy/               udev rule, systemd user unit, laptop launcher entry
scripts/headless-gui.sh    private display, bus and runtime dir for GUI tests (from standards)
android/app/.../model/     wire codec, JSON messages, host profiles and settings
android/app/.../crypto/    the same derivations as host/internal/crypto, Keystore wrapping
android/app/.../net/       OkHttp control client, UDP transport, frame assembler, session
android/app/.../video/     hardware MediaCodec decoder, GL renderer rebuilding chroma from luma, exact tile overlay
android/app/.../audio/     desktop sound playback, microphone capture and Opus encoding
android/app/.../input/     Android → evdev key map, gesture engine, input view
android/app/.../ui/        ViewModel, main page, settings, stream HUD and stats, share screen (ShareActivity sends files)
```

---

## Rules that cross packages

- **The host and the clients share byte formats and key derivations.** A change
  to a header field, label or encoding in `host/internal/net/packet.go`,
  `host/internal/net/tiles.go` or `host/internal/crypto/session.go` must be
  made in `Protocol.kt`, `TileCodec.kt` or `SessionCrypto.kt` too; the laptop
  client uses the Go packages directly.
  Frame recovery rules, parity (FEC) recovery included, are duplicated in
  `client/assembler.go` and `FrameAssembler` (Kotlin), with the same test
  cases; so are the clipboard image parts (`net/clipimage.go` and
  `ImageAssembler` in `Protocol.kt`). Both sides assert the same vectors
  (`session_test.go` / `SessionCryptoTest.kt`, `packet_test.go` /
  `ProtocolTest.kt`, `tiles_test.go` / `TileCodecTest.kt`); regenerate them
  on both sides together.
- **The wire carries Linux evdev key codes.** The client translates
  (`LinuxKeyCodes.kt`); the host only validates the range 1..248 and names keys
  for logs (`keymap.go`). Text a key cannot type goes as a `type` message,
  which the host types as keysyms in order with the keys (docs/protocol.md).
- **Every change a session makes to the desktop is undone in
  `session.cleanup`.** Never change the desk's monitors' modes, positions or
  scales: users at the desk see screens switching. Adding the client's own
  screen makes Mutter re-plan every monitor for a moment, so `auto` adds it
  only when the desk was locked. The one exception: an own-screen session
  that found the desk locked, or whose client asked (`desk`), turns the
  desk's monitors off (temporary config), and cleanup turns them on before
  the cast stops (docs/desktop.md).
- **Launch requests must name an app from the scanned list.** Never execute a
  command line sent by the client.

---

## Build and run

```bash
make check          # Go: fmt, vet, build, test, GUI smoke, lint, AGENTS.md size
make android-check  # APK, JVM tests, Android lint
make build          # bin/desklinkd and bin/desklink (CGO: GStreamer, GTK 4, libadwaita)
make smoke          # laptop GUI smoke test through scripts/headless-gui.sh
make apk            # bin/desklink.apk (debug build, for development)
make release-apk    # bin/release/desklink.apk (release build, signed with the app key)
make install        # adb install -r bin/desklink.apk
make screenshots    # the app's screens rendered on the JVM into bin/screenshots (not run by android-check)
make install-host   # ~/.local/bin/desklinkd and the systemd user unit
make install-laptop # ~/.local/bin/desklink and its launcher entry
make rpm            # rpmbuild/RPMS: desklinkd (host) and desklink (laptop) RPMs
make release        # check, build RPMs and release APK, upload to GitHub release v<monova> and the install page
make publish-apk    # put the signed APK on the install page again (retry)
desklinkd pair      # at the desk: the pairing code (QR and JSON), and whether Wake-on-LAN is ready
desklinkd peers     # paired devices; desklinkd peers remove <ID|name> forgets one
desklinkd sessions  # the recent sessions: device, address, duration, screen, lowest quality level, why each ended
desklinkd autologin # whether GDM logs you in by itself (desklinkd needs a session); `on` turns on automatic and timed login
desklinkd doctor    # whether this computer is ready: desk, network, staying up, devices; exits 1 when DeskLink cannot work
```

Prerequisites: Go 1.26+, `sudo dnf install gstreamer1-devel
gstreamer1-plugins-base-devel gtk4-devel libadwaita-devel` for the cgo build,
and at runtime `pipewire-gstreamer gstreamer1-plugins-bad-free wl-clipboard`
with Mesa's VA-API driver; the laptop client also needs
`gstreamer1-plugin-gtk4`. The GTK bindings are the shared set pinned in
AGENTS.go.md, so they come from the common build cache. Android builds use Gradle 9.8.1, AGP 9.4.1 with built-in
Kotlin 2.4.21 and compile SDK 37 (the toolchain shared with the other apps);
the app's minimum SDK is 31 and its target SDK 35. Gradle runs on JDK 25 from
`android/gradle/gradle-daemon-jvm.properties`. The SDK path comes from
`ANDROID_HOME` or the ignored `android/local.properties`.

The version comes from `monova` (0.0.0 before the first commit) and is stamped
into the binary (`main.Version`) and the APK (`-PappVersion`, version code
`major*1,000,000 + minor*1,000 + patch`).

`make release` publishes the pushed `master` commit: `release-preflight`
refuses a dirty tree, another branch or an unpushed commit; then `make check`
and `make android-check` run, `release-artifacts` builds `dist/` and `grm
release jsnjack/desklink` uploads it to the release `v<monova version>`
(creating it, or resuming a partial upload; grm uses its own token). The
release holds two binary RPMs from `packaging/desklink.spec`, which packages
the already built binaries without compiling: `desklinkd` (binary, systemd
user unit with `ExecStart=/usr/bin/desklinkd`, a user preset enabling it
on install and upgrade, the uinput `uaccess` udev rule and a NetworkManager
drop-in arming Wake-on-LAN on wired connections; its scriptlets apply the
udev rule, arm wired cards with ethtool, start the unit in running graphical
sessions and restart it on upgrade) and `desklink` (binary, launcher entry), x86_64 only. It also holds `desklink.apk`, under a
fixed name so the latest-release link stays stable: the R8-minified,
non-debuggable release build from `make release-apk`, signed with
`apksigner` and the personal app key (`~/.android/debug.keystore`, the same
certificate local debug builds use, so either updates the other in place).
Every machine that releases needs that same keystore: `release-apk` refuses
an APK whose certificate differs from `APP_CERT_SHA256` in the Makefile,
because the tablet cannot update to it ("App not installed").
Last, `make publish-apk` replaces `desklink.apk` on the shared install page
https://jsnjack.github.io/android-apps/ (the `apps` release of `jsnjack/android-apps`, labelled
with the version; needs an authenticated `gh`), where the tablet's browser
installs it. There is no F-Droid repository and no GitHub Actions workflow. A local APK installed
over a released one needs a version code at least as high.

`--debug` (`-d`) lowers stderr from warnings to debug; `--trace` also writes
everything to `/tmp/desklinkd.log`, truncated on start; the flags combine.
Session start and end only show with `-d`. Android logs under the logcat tag
`DeskLink`.

---

## Traps

- `go test ./internal/video` and every build need the GStreamer devel files
  because go-gst is cgo. `TestEncoderOnGPU` runs the real VA-API encoders and
  skips without `/dev/dri/renderD128`.
- go-gst v0.0.3 is the generated binding; the v1.x tags are retracted and
  have a different API. GValues must be sized types (`uint32`, not `uint`).
- `vaav1enc` has a single `qp` property on AV1's 0–255 scale; the HEVC-scale
  quantizer 12–38 is multiplied by five (`AV1QIndex`). Every `qp` change
  restarts the sequence with a keyframe, and `min-qp`, `max-qp` and VBR
  `bitrate` give no keyframe-free control either (docs/desktop.md), so the
  quality controller changes `qp` rarely and in large steps.
- `target-usage` on Mesa radeonsi does not follow the usual 1 = quality,
  7 = speed order: 2 is the fastest that keeps scrolling small.
- Never use go-gst's `PadProbeInfo.GetBuffer`, `AppSink.PullSample` or other
  buffer wrappers on the capture path: they hold a reference until Go's
  garbage collector runs, Mutter shares only a few buffers, and the stream
  freezes. `internal/video/buffers.go` reads through C and releases at once.
- Never run a VA element on the capture in a second thread: two
  `vapostproc`s on Mutter's DMA-BUFs in two threads crashed Mesa's VA
  post-processor (0.50.0). The exact-tiles tee comes after the one capture
  queue, with no queue after it (docs/desktop.md).
- Mesa's `vapostproc` writes every P010 code 257/256 of the standard one
  (a grey's chroma 514, not 512), and the tablet's `StreamColor` undoes
  exactly that. `TestConversionKeepsDesktopColours` pins the codes; when a
  driver or converter change fails it, change `StreamColor` with it
  (docs/desktop.md, Encoder).
- Never set `drop=true` on the appsink: a dropped encoded frame corrupts every
  frame that predicts from it.
- Screen capture uses `org.gnome.Mutter.ScreenCast` directly, not the
  portal: the portal's dialog cannot be answered remotely. Do not switch back.
- Never run the laptop client on the desktop session for testing; GUI tests go
  through `scripts/headless-gui.sh` (`make smoke`).
- Mutter has no data-control protocol, so `wl-paste --watch` fails on GNOME;
  the host reads the clipboard after copy shortcuts and on request instead.
- desklinkd reads the pointer apart from the video by defining
  `pw_stream_dequeue_buffer` (`video/cursorhook.c`), which works only when
  the executable exports it: build desklinkd through `make build`, whose
  linker flag does that. A desklinkd built otherwise gives clients the
  pointer in the video (docs/desktop.md).
- Input travels as unacknowledged UDP; the held-input snapshot (input kind 7)
  is what keeps a lost key-up from leaving a key repeating. Clients must keep
  sending it.
