# Host desktop integration

## Session lifecycle (`internal/daemon/session.go`)

On an authenticated WebSocket the host waits for `hello`, then:

1. Wakes the monitors (`ScreenSaver.SimulateUserActivity`, Mutter
   `PowerSaveMode = 0`), unlocks the session (remembering whether logind's
   `LockedHint` said it was locked) and takes a ScreenSaver idle inhibitor.
   These steps log failures and continue.
2. Switches the primary monitor to the requested size if it has such a mode
   (below).
3. Starts the screen cast with the saved restore token (45 s timeout). If
   GNOME needs to ask, nobody can answer remotely, so the session fails with
   a hint to run `desklinkd pair` at the desk.
4. Opens the UDP session, starts the pipeline, and sends `session` and `apps`.
5. Serves control messages until the WebSocket closes or the pipeline fails.

A goroutine reads the WebSocket from the moment `hello` arrives, because
coder/websocket answers the client's pings only while reading and setup can
wait up to 45 s for the portal. If the client leaves during setup, the session
ends quietly.

`cleanup` stops the pipeline, closes the portal session and the UDP session,
releases every held key and button (so Ctrl is not stuck after a dropped
connection), restores the monitor layout, releases the idle inhibitor and
locks the session again if it was locked before. Running apps are left
alone. When the daemon stops, it waits for the running session's cleanup
before closing D-Bus and the input devices.

## Screen capture

`desktop/portal.go` calls `CreateSession`, `SelectSources` (monitor, embedded
cursor, `persist_mode = 2`, the saved `restore_token`), `Start` and
`OpenPipeWireRemote`. Each `Start` returns a new token, saved to
`state.json`. Request objects are awaited by predicting their path from the
handle token and subscribing to `Response` before the call. Starts are
serialised, so a connection that arrives while the pairing dialog is still
open waits for it instead of racing it for the single-use token.

The first grant must happen at the desk. `desklinkd pair` therefore also
starts and closes a screen cast, which makes GNOME show its sharing dialog
while the user is there; `pair` reports whether it was allowed. The token is
tied to the daemon's app ID (empty for `desklinkd.service`), which is why the
daemon, not the `pair` process, runs the portal flow.

## Unlocking

logind lets a session's owner call `org.freedesktop.login1.Session.Unlock`
without polkit, so the host looks up its user's graphical session
(`User.Display`) and unlocks that. `Manager.UnlockSessions` is only a
fallback: polkit treats a user service as having no active session and asks
for admin authentication. GNOME Shell reacts to logind's `Unlock` signal by
dismissing the lock screen.

## Resolution matching

`desktop/display.go` plans and `desktop/dbus.go` applies. The host reads
`GetCurrentState`, picks a mode of exactly the requested size on the primary
monitor with the highest refresh rate not above the client's frame rate, keeps
the scale if the mode supports it (otherwise the nearest supported scale),
moves monitors to the right of or below it with its new edge, and calls
`ApplyMonitorsConfig` with method 1 (temporary: no `monitors.xml` change and
no confirmation dialog). The original layout is saved in `state.json` first
and reapplied on disconnect or at the next start after a crash. The saved
layout is dropped when the switch it was saved for fails, and when it names a
connector or mode the monitors no longer have.

If the monitor has no such mode (most 16:9 panels lack 2560×1600), the desktop
stays as it is and `vapostproc` scales the capture to the requested size with
black borders; the HUD shows which happened.

## Encoder (`internal/video`)

```
pipewiresrc fd path do-timestamp keepalive-time=100 ! queue max-size-buffers=2 leaky=downstream !
videorate drop-only=true max-rate=<fps> ! vapostproc add-borders=true ! video/x-raw(memory:VAMemory),format=P010_10LE,width,height,colorimetry=1:3:5:1 !
vaav1enc rate-control=cqp qp=<5×QP> key-int-max=120 ref-frames=1 palette-mode=true ! av1parse ! obu-stream/tu !
appsink sync=false max-buffers=8 drop=false
```

The HEVC variant uses `vah265enc rate-control=cqp qpi qpp b-frames=0` and
`h265parse config-interval=-1` to Annex B access units. Notes on the choices:

- `keepalive-time=100` makes PipeWire resend the last frame on a static
  screen, so a forced keyframe is produced within 100 ms.
- `videorate` caps the frame rate at the client's request without adding
  latency, because the monitor may refresh faster (144 Hz) than asked;
  `Encoder.SetMaxFPS` changes the cap while running.
- `colorimetry=1:3:5:1` is full-range BT.709.
- `palette-mode` is an AV1 screen-content tool that VCN supports; intra block
  copy is not supported by the driver and is left off.
- Dropping at the appsink would corrupt predicted frames, so it never drops.

`NeedIDR` and `req_idr` force a keyframe, at most one per 150 ms. A `quality`
message with a new size restarts the pipeline on a fresh PipeWire connection
after switching the monitor mode.

## Adapting to the connection (`video/quality.go`)

`QualityController` judges each telemetry report against the connection's own
history. Queueing delay is the RTT minus the lowest RTT of the last minute, so
a steady 30 ms office VPN path counts as clean while a growing queue does not.
A report is bad at 2 % loss or 40 ms of queueing, and clean under 0.5 % loss
and 15 ms.

- **Quantizer:** a bad report raises it by 2, at most once a second, up to
  `max_qp`; five clean seconds lower it by 1, down to the client's request.
- **Ladder (Auto preset):** `Levels` derives the rungs from the client's
  maximum, for the tablet 2560×1600 at 120 fps, 2560×1600 at 60, 1920×1200 at
  60 and 1280×800 at 60. When the quantizer is at its ceiling and reports stay
  bad for 3 s, the controller steps one level down (at most every 10 s) and
  restarts the quantizer mid-range. After 20 clean seconds at the requested
  quantizer it probes one level up; if that level turns bad within 15 s it
  steps straight back and blocks probing for 2 minutes, doubling up to 10
  minutes with each failed probe.
- **Applying a level:** the UDP goroutine only queues the new level; the
  session loop applies it. A frame-rate step changes `videorate max-rate` in
  the running pipeline. A size step switches the monitor mode, remaps the pen
  and pointer and restarts the pipeline, so the client sees one keyframe's
  pause. Every change is reported in a `session` message with `level` and
  `levels`.

Manual presets have a one-level ladder, so only the quantizer moves. The
clients remember the last level per host and network (the Android network
type, or the laptop's interface name) and send it as `start_level`, so the
office VPN and home Wi-Fi each start where they last settled.

## Clipboard

Mutter does not implement the wlroots or ext data-control protocols, so
`wl-paste --watch` exits at once on GNOME. The host tries it and, when it
fails, reads the clipboard 250 ms after a copy shortcut typed through
DeskLink (Ctrl+C, Ctrl+X, Ctrl+Shift+C, Ctrl+Insert) and on
`clipboard_request`. Copies made with the mouse reach the tablet through
"Get clipboard". Incoming text is written with `wl-copy`. On GNOME
wl-clipboard briefly maps a surface to get focus for each call.

## App drawer

`.desktop` files are read from `/usr/share/applications`, the Flatpak export
directories and `~/.local/share/applications` (later directories override),
skipping `NoDisplay`, `Hidden` and non-applications. `pinned_apps` entries
come first; Bunk Terminal (`bunk-gui`) is the default and takes over the
installed `io.github.jsnjack.BunkGui.desktop` because its command line is
exactly `bunk-gui`. A pinned app takes over an entry with its ID, or the only
entry with exactly its command line; it never guesses between entries that
share an executable such as `flatpak`. Launches go
through `systemd-run --user --scope` (`app-desklink-<id>-<random>.scope`) and
`gio launch`, so apps outlive daemon restarts and get correct `Terminal=` and
D-Bus activation handling.

## Input

Three uinput devices exist for the daemon's lifetime: a keyboard with keys
1–248, a relative mouse (five buttons, wheel and high-resolution wheel) and a
pen tablet (`ABS_X`/`ABS_Y` 0..65535, `BTN_TOOL_PEN`, `BTN_TOUCH`). The pen is
an indirect tablet, which GNOME maps over the whole desktop, so the host maps
the client's frame coordinates (`daemon/pointer.go`): it removes the borders
`vapostproc` added, then places the captured monitor, whose position and size
come from the portal stream, inside the bounding box of all monitors. This
mapping is untested on a multi-monitor desktop.

Key repeats from Android are dropped; GNOME repeats held keys itself. The
injector records the sequence number of the packet that last changed each key,
button and the pen, and applies the client's held-input snapshots
(docs/protocol.md) only when they are newer. Fedora
already gives the logged-in user `/dev/uinput` through a uaccess ACL;
`deploy/60-desklink.rules` is for systems without it and requires membership
of the `input` group.

## Configuration

`~/.config/desklink/config.json` (or `--config`), all fields optional:

```json
{
  "control_addr": "0.0.0.0:9443",
  "media_addr": "0.0.0.0:9444",
  "advertise_ip": "192.168.178.11",
  "allowed_subnets": ["10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8"],
  "pinned_apps": [{"id": "bunk-gui", "name": "Bunk Terminal", "exec": "bunk-gui"}],
  "wayland_display": "wayland-0",
  "min_qp": 16,
  "max_qp": 26
}
```

`advertise_ip` goes into the pairing QR code. Connections and datagrams from
outside `allowed_subnets` are closed or dropped before TLS or decryption. The
listeners bind IPv4 only.
