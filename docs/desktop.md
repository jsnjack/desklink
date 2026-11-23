# Host desktop integration

## Session lifecycle (`internal/daemon/session.go`)

On an authenticated WebSocket the host waits for `hello`, then:

1. Wakes the monitors (`ScreenSaver.SimulateUserActivity`, Mutter
   `PowerSaveMode = 0`), unlocks the session (remembering whether logind's
   `LockedHint` said it was locked) and takes a ScreenSaver idle inhibitor.
   These steps log failures and continue.
2. Switches the streamed monitor (`monitor` in the config, otherwise the
   primary one) to the requested size if it has such a mode (below).
3. Records that monitor through Mutter's screen-cast API (15 s timeout). No
   dialog appears, so a session starts with nobody at the desk.
4. Opens the UDP session, starts the pipeline, and sends `session` and `apps`.
5. Serves control messages until the WebSocket closes or the pipeline fails.

A goroutine reads the WebSocket from the moment `hello` arrives, because
coder/websocket answers the client's pings only while reading and setup can
take several seconds. It reads with the connection's context, not
the session's: cancelling a pending read closes the connection, and a setup
error still has to reach the client. If the client leaves during setup, the
session ends quietly; an unexpected control-connection failure is logged as
a warning.

`cleanup` stops the pipeline, stops the screen cast and the UDP session,
releases every held key and button (so Ctrl is not stuck after a dropped
connection), restores the monitor layout, releases the idle inhibitor and
locks the session again if it was locked before. When a new connection
replaces the session (the tablet roamed between networks), the old session
skips that relock and hands it to the new one, because locking between the two
would flash the lock screen and empty GNOME's clipboard. If logind's
`LockedHint` cannot be read, the session counts as locked. Running apps are
left alone. When the daemon stops, it waits for the running session's cleanup
before closing D-Bus and the input devices.

## Screen capture

`desktop/screencast.go` uses `org.gnome.Mutter.ScreenCast`, the API
gnome-remote-desktop uses: `CreateSession`, `RecordMonitor(connector,
cursor-mode = embedded)`, then `Start`, and waits for the stream's
`PipeWireStreamAdded` signal (subscribed before `Start`) for the PipeWire node.
The stream's `Parameters` give the monitor's logical position and size for the
pointer map. `pipewiresrc path=<node>` reads the node from the user's PipeWire
daemon; no file descriptor is passed. Mutter stops the cast when the D-Bus
connection that created it closes, so a crashed daemon leaves nothing behind.

The ScreenCast portal is not used: it asks the user at the desk, and when
GNOME drops its restore token (after a monitor change, for example) the next
remote session would wait for a dialog nobody can answer. desklinkd runs in
the user's own session, which may call Mutter directly; the pairing PIN is
what authorises a device (see [security.md](security.md)).

## Locking after automatic login

For a host that must be reachable after a reboot or power cut, turn on
automatic login (Settings → System → Users → Automatic Login). GDM then opens
the session at boot, the user unit starts with it, and desklinkd locks the
session at once (`autolock.go`): when logind's `Session.Service` is
`gdm-autologin` and the session started less than two minutes ago, it calls
`Session.Lock` every 2 s until `LockedHint` is set, for up to 30 s, because
GNOME Shell may not be listening yet. A client's connection unlocks it as
usual and the session's cleanup locks it again. A daemon restarted later in
the day never locks the desk. Set `lock_after_autologin` to `false` for a
machine that logs in automatically on purpose and should stay open.

With automatic login the login keyring is not unlocked at boot, so apps that
read saved passwords ask for it until the desk is unlocked once with the
password.

## Wake-on-LAN

Clients wake a sleeping or switched-off host with a magic packet to the MAC
that `/health` reports (the interface holding the advertised address). At
startup and in `desklinkd pair`, `wake.go` reads sysfs and says when waking
cannot work: the interface is Wi-Fi (`wireless/`), its MAC was changed
(`addr_assign_type` not 0, so it is not the address the card listens for
while asleep), or Wake-on-LAN is off (`device/power/wakeup` is `disabled`;
ethernet drivers set it when WoL is armed). The desklinkd RPM installs
`/usr/lib/NetworkManager/conf.d/60-desklink-wake-on-lan.conf`, which makes
NetworkManager arm magic-packet wake whenever a wired connection comes up
(a connection with its own `ethernet.wake-on-lan` keeps it), and arms the
wired cards that are already up with `ethtool -s <card> wol g`. Waking from
power-off also needs Wake-on-LAN enabled in the BIOS, and on some boards
"ErP" or deep-sleep power saving turned off.

## Unlocking

logind lets a session's owner call `org.freedesktop.login1.Session.Unlock`
without polkit, so the host looks up its user's graphical session
(`User.Display`) and unlocks that. `Manager.UnlockSessions` is only a
fallback: polkit treats a user service as having no active session and asks
for admin authentication. GNOME Shell reacts to logind's `Unlock` signal by
dismissing the lock screen.

## Resolution matching

`desktop/display.go` plans and `desktop/dbus.go` applies. The host reads
`GetCurrentState` and picks a mode of exactly the requested size on the
streamed monitor: `monitor` from the config when it is connected, otherwise
the primary one. The mode has the highest refresh rate not above the fastest
ladder level of that size, so a later frame-rate step never needs a mode
change. The scale is kept if the mode supports it (otherwise the nearest
supported scale), monitors to the right of or below it move with its new edge,
and `ApplyMonitorsConfig` runs with method 1 (temporary: no `monitors.xml`
change and no confirmation dialog), uncancelled by the client leaving: Mutter
applies it either way. The original layout is saved in `state.json` first
and reapplied on disconnect or at the next start after a crash. The saved
layout is dropped when the switch it was saved for fails outright, and when it
names a connector or mode the monitors no longer have; a switch that timed out
keeps it, since Mutter may still apply it.

If the monitor has no such mode (most 16:9 panels lack 2560×1600), the
desktop's original layout is restored if an earlier level of the session had
switched it, and `vapostproc` scales the capture to the requested size with
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
- A pad probe on `vapostproc`'s input (`frameLimiter`) caps the frame rate at
  the client's request by dropping frames that arrive sooner than the
  interval, judged by their timestamps. `videorate` cannot do this: Mutter's
  screen cast has a variable frame rate (`framerate=0/1`), which videorate
  passes through. `Encoder.SetMaxFPS` changes the cap while running.
- `colorimetry=1:3:5:1` is full-range BT.709.
- `palette-mode` is an AV1 screen-content tool that VCN supports; intra block
  copy is not supported by the driver and is left off.
- Dropping at the appsink would corrupt predicted frames, so it never drops.

`NeedIDR` and `req_idr` force a keyframe, at most one per 150 ms. A `quality`
message with a new size stops the pipeline, switches the monitor mode and
starts a new pipeline on a fresh PipeWire connection, so the old pipeline never
sees the capture change size. Starting and stopping happen without holding the
lock the UDP goroutine takes for keyframes and QP changes, so input keeps
flowing during a restart.

## Adapting to the connection (`video/quality.go`)

`QualityController` judges each telemetry report against the connection's own
history, for manual presets and Auto alike. Queueing delay is the RTT minus
the lowest RTT of the last minute, so a steady 30 ms office VPN path counts as
clean while a growing queue does not. RTT is measured every 2 s but reports
arrive every 250 ms, so delay counts as congestion only after two consecutive
late samples (40 ms of queueing); one late echo is not eight bad reports. A
report is bad at 2 % loss or with that delay, and clean under 0.5 % loss and
15 ms.

- **Quantizer:** a bad report raises it by 2, at most once a second, up to
  `max_qp`; five clean seconds lower it by 1, down to the client's request.
- **Ladder (Auto preset):** `Levels` derives the rungs from the client's
  maximum, for the tablet 2560×1600 at 120 fps, 2560×1600 at 60, 1920×1200 at
  60 and 1280×800 at 60. Once the quantizer has been at its ceiling for 3 s
  and the reports of those 3 s average 2 % loss or mostly show delay, the
  controller steps one level down (at most every 10 s) and restarts the
  quantizer mid-range; averaging over the window catches bursty loss that
  single reports miss. After 20 clean seconds at the requested quantizer it
  probes one level up; if the first second or more of the probe averages
  congestion within 15 s it steps straight back and blocks probing for 2
  minutes, doubling up to 10 minutes with each failed probe.
- **Applying a level:** the UDP goroutine only queues the new level; the
  session loop applies it, with the level's quantizer (setting that first on
  the old, larger pipeline would raise its bitrate). A frame-rate step changes
  the frame limiter in the running pipeline. A size step stops the pipeline,
  switches the monitor mode, remaps the pen and pointer and starts a new
  pipeline, so the client sees one keyframe's pause. Every change is reported
  in a `session` message with `level` and `levels`.

Manual presets have a one-level ladder, so only the quantizer moves. The
clients remember the last level per host and network (the Android network
type, or the laptop's interface name) by its size and frame rate, and send it
as `start`; the host starts at the best rung no larger and no faster
(`LevelFor`), so the office VPN and home Wi-Fi each start where they last
settled even if the ladder changes.

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

Four uinput devices exist for the daemon's lifetime: a keyboard with keys
1–248, a relative mouse (five buttons, wheel and high-resolution wheel), a pen
tablet (`ABS_X`/`ABS_Y` 0..65535, `BTN_TOOL_PEN`, `BTN_TOUCH`) and an
absolute pointer for the laptop client (`ABS_X`/`ABS_Y` with mouse buttons,
which udev and libinput treat as a virtual machine's tablet mouse). The pen
and the pointer span the whole desktop, so the host maps the client's frame
coordinates (`daemon/pointer.go`): it removes the borders `vapostproc` added,
then places the captured monitor, looked up by connector so a resolution change
cannot leave the mapping stale, inside the bounding box of all monitors. This
mapping is untested on a multi-monitor desktop.

Mouse buttons go to the device that moved the pointer last: after an
absolute pointer record (kind 6) left, right and middle clicks are written to
the absolute device, otherwise to the relative mouse. libinput reads each
device separately, so a click on the other device could be handled before the
move and land where the pointer was. A button is always released on the device
that pressed it.

Key repeats from Android are dropped; GNOME repeats held keys itself. The
injector records the sequence number of the packet that last changed each key,
button and the pen. It ignores an event from an older packet than that (UDP
can reorder a key's up before its down), and applies the client's held-input
snapshots (docs/protocol.md) only when they are newer. The numbers reset when a
session ends, since the next session counts from 1 again.

`/dev/uinput` belongs to root, and stock Fedora gives desktop users no access
to it (packages such as Solaar or Steam's device rules add it).
`deploy/60-desklink.rules`, installed by the desklinkd RPM, tags it `uaccess`,
so logind grants the user at the computer an ACL; the RPM applies it to the
existing node at install time with `udevadm trigger`.

## Configuration

`~/.config/desklink/config.json` (or `--config`), all fields optional:

```json
{
  "control_addr": "0.0.0.0:9443",
  "media_addr": "0.0.0.0:9444",
  "advertise_ip": "",
  "allowed_subnets": ["10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8"],
  "pinned_apps": [{"id": "bunk-gui", "name": "Bunk Terminal", "exec": "bunk-gui"}],
  "monitor": "",
  "wayland_display": "wayland-0",
  "min_qp": 16,
  "max_qp": 26,
  "lock_after_autologin": true
}
```

`advertise_ip` goes into the pairing QR code and picks the interface whose MAC
`/health` reports for Wake-on-LAN. Empty means detected: the source address of
the default route, or else the first private address of an interface that is
up and not a container or VM bridge (`lxdbr*`, `docker*`, `virbr*`, …). Set it
when clients reach the host on another address, such as a VPN's. `monitor` is
the connector to stream (`DP-1`, `eDP-1`; `gnome-monitor-config list` shows
them); empty means the primary monitor. Connections and datagrams from
outside `allowed_subnets` are closed or dropped before TLS or decryption. The
listeners bind IPv4 only.
