# Host desktop integration

## Session lifecycle (`internal/daemon/session.go`)

On an authenticated WebSocket the host waits for `hello`, then:

1. Wakes the monitors (`ScreenSaver.SimulateUserActivity`, Mutter
   `PowerSaveMode = 0`), unlocks the session and takes a ScreenSaver idle
   inhibitor. These steps log failures and continue.
2. Switches the primary monitor to the requested size if it has such a mode
   (below).
3. Starts the screen cast with the saved restore token (45 s timeout). If
   GNOME needs to ask, nobody can answer remotely, so the session fails with
   a hint to run `desklinkd pair` at the desk.
4. Opens the UDP session, starts the pipeline, and sends `session` and `apps`.
5. Serves control messages until the WebSocket closes or the pipeline fails.

`cleanup` stops the pipeline, closes the portal session and the UDP session,
releases every held key and button (so Ctrl is not stuck after a dropped
connection), restores the monitor layout and releases the idle inhibitor.
Running apps are left alone.

## Screen capture

`desktop/portal.go` calls `CreateSession`, `SelectSources` (monitor, embedded
cursor, `persist_mode = 2`, the saved `restore_token`), `Start` and
`OpenPipeWireRemote`. Each `Start` returns a new token, saved to
`state.json`. Request objects are awaited by predicting their path from the
handle token and subscribing to `Response` before the call.

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
and reapplied on disconnect or at the next start after a crash.

If the monitor has no such mode (most 16:9 panels lack 2560×1600), the desktop
stays as it is and `vapostproc` scales the capture to the requested size with
black borders; the HUD shows which happened.

## Encoder (`internal/video`)

```
pipewiresrc fd path do-timestamp keepalive-time=100 ! queue max-size-buffers=2 leaky=downstream !
vapostproc add-borders=true ! video/x-raw(memory:VAMemory),format=P010_10LE,width,height,colorimetry=1:3:5:1 !
vaav1enc rate-control=cqp qp=<5×QP> key-int-max=120 ref-frames=1 palette-mode=true ! av1parse ! obu-stream/tu !
appsink sync=false max-buffers=8 drop=false
```

The HEVC variant uses `vah265enc rate-control=cqp qpi qpp b-frames=0` and
`h265parse config-interval=-1` to Annex B access units. Notes on the choices:

- `keepalive-time=100` makes PipeWire resend the last frame on a static
  screen, so a forced keyframe is produced within 100 ms.
- `colorimetry=1:3:5:1` is full-range BT.709.
- `palette-mode` is an AV1 screen-content tool that VCN supports; intra block
  copy is not supported by the driver and is left off.
- Dropping at the appsink would corrupt predicted frames, so it never drops.

`QualityController` raises the quantizer by 2 (at most once a second, up to
`max_qp`) when telemetry reports 2 % loss or an RTT of 80 ms, and lowers it by
1 after five clean seconds (under 0.5 % loss and 40 ms) down to the requested
value. `NeedIDR` and `req_idr` force a keyframe, at most one per 150 ms.
A `quality` message with a new size restarts the pipeline on a fresh PipeWire
connection after switching the monitor mode.

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
installed `io.github.jsnjack.BunkGui.desktop` by executable name. Launches go
through `systemd-run --user --scope` (`app-desklink-<id>-<random>.scope`) and
`gio launch`, so apps outlive daemon restarts and get correct `Terminal=` and
D-Bus activation handling.

## Input

Three uinput devices exist for the daemon's lifetime: a keyboard with keys
1–248, a relative mouse (five buttons, wheel and high-resolution wheel) and a
direct pen digitizer (`ABS_X`/`ABS_Y` 0..65535, `BTN_TOOL_PEN`, `BTN_TOUCH`).
Key repeats from Android are dropped; GNOME repeats held keys itself. Fedora
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
