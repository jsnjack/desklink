# Host desktop integration

## Session lifecycle (`internal/daemon/session.go`)

On an authenticated WebSocket the host waits for `hello`, then:

1. Wakes the monitors (`ScreenSaver.SimulateUserActivity`, Mutter
   `PowerSaveMode = 0`), unlocks the session (remembering whether logind's
   `LockedHint` said it was locked) and takes a ScreenSaver idle inhibitor.
   These steps log failures and continue.
2. Chooses what the client sees (`hello.screen`): its own virtual screen
   (`own`), a mirrored monitor (`mirror`), or by default (`auto`) its own
   screen only when the session was locked as the client connected, and a
   mirrored monitor otherwise (below). If Mutter cannot make a virtual screen,
   it mirrors. No dialog appears either way, so a session starts with nobody
   at the desk.
3. Opens the UDP session, starts the pipeline, and sends `session` and `apps`.
   For a virtual screen it then sets the screen's scale once Mutter has added
   it to the layout.
4. Serves control messages until the WebSocket closes or the pipeline fails.

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
gnome-remote-desktop uses: `CreateSession`, `RecordVirtual` (or
`RecordMonitor(connector)` for the fallback) with `cursor-mode = embedded`,
then `Start`, and waits for the stream's
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

### The session's own screen

A desk monitor streamed to a tablet is the wrong size: a 27-inch 2560×1440
monitor at 100 % shrinks to about 40 % on an 11-inch tablet, with black bars
for the other aspect ratio. A session can therefore get a virtual monitor of
exactly the client's size and rate (the top of its quality ladder, such as
2560×1600 at 120 Hz). Mutter creates it from the stream: the pipeline puts
`video/x-raw(memory:DMABuf),width=…,height=…,max-framerate=…/1` after
`pipewiresrc`, and Mutter adds a "Virtual remote monitor" (`Meta-0`) of that
mode to the right of the others once the format is negotiated. Lower quality
levels keep the monitor's size and let `vapostproc` scale the stream.

Adding a monitor is not quiet: Mutter has no saved configuration for "the
desk's monitors plus a virtual one", so it lays the whole set out from
scratch, moving the desk's monitors for a moment (they flicker, and windows
can hop to another monitor). The session puts them back at once
(`desktop.PlanVirtual`: the layout from before the cast, the virtual monitor
right of the rightmost monitor), polling every 50 ms for the new monitor.
GNOME offers no way to avoid the moment: a configuration Mutter would reuse
can only be saved with the persistent method, which shows "Keep these display
settings?" at the desk. That is why `auto` uses the virtual screen only when
nobody is at the desk.

The session sets the virtual monitor's scale (`hello.scale`, 200 % by default,
the nearest scale Mutter supports), so a 2560×1600 tablet gets a
1280×800-point interface. A `scale` message changes it during the session:
the session lays out the desk as before with the virtual monitor at the new
scale (the desk's monitors keep their place and mode), maps input again and
resends `session` with the applied scale. It makes the virtual monitor primary (GNOME's top
bar, overview and notifications move to it) only when the session was locked
as the client connected. When the cast stops, the virtual monitor disappears
and Mutter returns the desk's monitors to the user's saved layout by itself;
the session reapplies its pre-connection layout only if they differ. Windows
that were on the virtual screen move to the desk. Input maps onto the virtual
monitor's place in the desktop like onto any monitor (below).

A mirrored monitor is `monitor` from the config when it is connected,
otherwise the monitor whose current shape is closest to the client's, the
primary one on a tie (`DisplayState.BestFit`): on a laptop with a 16:9
external monitor and a 16:10 panel, a 16:10 tablet mirrors the panel.

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

## Addresses and discovery

`/health` and the pairing code list every IPv4 address a client may use
(`daemon.addresses`: interfaces that are up, not container or VM bridges,
inside `allowed_subnets`, the advertised one first), so a client paired at
home also knows the office or VPN address. desklinkd also announces
`_desklink._tcp` with mDNS through Avahi's D-Bus API on every interface
(`desktop/mdns.go`), carrying the certificate fingerprint; Avahi withdraws it
when the daemon's D-Bus connection closes. Without avahi-daemon the
announcement is skipped with a warning.

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

## Monitor layout

DeskLink never changes the desk's monitors' modes, positions or scales: GNOME
users see that as screens switching under them. A mirrored monitor is
streamed as it is and `vapostproc` scales it to the stream with black borders
for another shape. A layout saved in `state.json` by an older version (which
switched resolutions) is reapplied once at startup, only if the monitors
differ from it, and retried with fresh state when Mutter reports it stale.

## Encoder (`internal/video`)

```
pipewiresrc path do-timestamp keepalive-time=100 ! [own screen: size caps] ! queue max-size-buffers=2 leaky=downstream !
vapostproc add-borders=true ! video/x-raw(memory:VAMemory),format=P010_10LE,width,height,colorimetry=2:3:5:1 !
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
- `colorimetry=2:3:5:1` is limited-range BT.709, the range every decoder
  assumes. Full range is signalled in the AV1 header, but the Tab S9's
  Qualcomm decoder ignores it and shows the video as limited, which deepens
  blacks and oversaturates colours; at 10 bits limited range still has 877
  levels per channel.
- `palette-mode` is an AV1 screen-content tool that VCN supports; intra block
  copy is not supported by the driver and is left off.
- Dropping at the appsink would corrupt predicted frames, so it never drops.
- A frame's capture time, which clients use to show the delay, is taken at
  the same probe from the buffer's timestamp (running time when PipeWire
  delivered it) and matched to the encoded frame in order: nothing drops
  frames after the probe and there are no B-frames. The encoder's output
  timestamps cannot be used, because `vah265enc` restamps frames by count
  from 1000 hours.

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

## Sound (`desktop/audio.go`, `video/audio.go`)

When `hello.audio` is set, the session moves the desktop's sound to the
client. `RedirectAudio` loads a null sink named `desklink` ("DeskLink") with
`pactl load-module module-null-sink` and makes it the default output;
WirePlumber moves the streams that follow the default along with it, so the
desk's speakers fall silent. The previous default is saved in `state.json`
(`audio_default_sink`). `pulsesrc device=desklink.monitor` captures what
plays there with a 10 ms buffer, `opusenc frame-size=10 bitrate=128000
audio-type=restricted-lowdelay` encodes it, and each frame goes out as one
audio datagram (docs/protocol.md). Cleanup stops the pipeline, makes the
saved output the default again and unloads the sink; at startup the daemon
unloads any `desklink` null sink a crashed run left and restores the saved
default. If the null sink cannot be made, the default output's monitor
(`@DEFAULT_MONITOR@`) is streamed and the desk keeps playing; if capture
fails, the session goes on without sound. pactl talks to PipeWire through
pipewire-pulse; the RPM requires `pulseaudio-utils` and
`gstreamer1-plugins-good` (pulsesrc).

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
D-Bus activation handling. Before launching, the session moves the pointer to
the middle of the screen the client sees: GNOME opens a new window on the
monitor under the pointer, which was otherwise wherever it was last, often a
desk monitor the client does not show.

## Input

Four uinput devices exist for the daemon's lifetime: a keyboard with keys
1–248, a relative mouse (five buttons, wheel and high-resolution wheel), a pen
tablet (`ABS_X`/`ABS_Y` 0..65535, `ABS_PRESSURE` 0..65535, `ABS_TILT_X`/`Y`
±9000 hundredths of a degree with a resolution of 5730 units per radian so
libinput reports degrees, `BTN_TOOL_PEN`, `BTN_TOUCH`) and an
absolute pointer for the laptop client (`ABS_X`/`ABS_Y` with mouse buttons,
which udev and libinput treat as a virtual machine's tablet mouse). The pen
and the pointer span the whole desktop, so the host maps the client's frame
coordinates (`daemon/pointer.go`): it removes the borders `vapostproc` added,
then places the captured monitor, looked up by connector so a resolution change
cannot leave the mapping stale, inside the bounding box of all monitors. This
mapping is untested on a multi-monitor desktop.

GNOME has one pointer for all monitors, so a client's relative motion (the
tablet's trackpad mode, a mouse on the tablet) would move it wherever it is,
often onto a monitor the client does not show, and scrolling would follow it
there. The session therefore keeps the client's own cursor position on its
screen (`daemon/cursor.go`): relative motion, in pixels of the client's
screen whatever size the stream has, moves that position within the frame
and is injected as an absolute pointer record, and a scroll or button
that no motion in the same packet placed is preceded by one, in case someone
at the desk moved the pointer meanwhile. Taps and the pen set the position the
trackpad continues from. Relative motion is not accelerated by the desktop
this way; the clients scale it.

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
