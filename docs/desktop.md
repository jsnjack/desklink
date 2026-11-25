# Host desktop integration

## Session lifecycle (`internal/daemon/session.go`)

On an authenticated WebSocket the host waits for `hello`, then:

1. Wakes the monitors (`ScreenSaver.SimulateUserActivity`, Mutter
   `PowerSaveMode = 0`), unlocks the session (remembering whether logind's
   `LockedHint` said it was locked) and takes a ScreenSaver idle inhibitor.
   These steps log failures and continue. Unlocking and the inhibitor run to
   the end, within 5 s each, even if the client leaves meanwhile, so a lost
   reply cannot leave the desk unlocked without the duty to lock it again.
2. Chooses what the client sees (`hello.screen`): its own virtual screen
   (`own`), a mirrored monitor (`mirror`), or by default (`auto`) its own
   screen only when the session was locked as the client connected, and a
   mirrored monitor otherwise (below). If Mutter cannot make a virtual screen,
   it mirrors. No dialog appears either way, so a session starts with nobody
   at the desk.
3. Opens the UDP session, starts the pipeline (with the scan branch for exact
   tiles when the client asked for them), and sends `session` and `apps`.
   For a virtual screen it then sets the screen's scale once Mutter has added
   it to the layout, and turns the desk's monitors off if the session was
   locked as the client connected. Right after `session` it shows a
   notification at the desk, "<device> connected and unlocked this computer"
   or "<device> connected" (see "Notifications").
4. Serves control messages until the WebSocket closes, the pipeline fails or
   the client's UDP traffic stops. Clients send telemetry and held input every
   250 ms and keepalives every 2 s, so when no authenticated datagram has come
   from the client for 10 s (`mediaTimeout`, counted from when `session` was
   sent), the UDP path is lost: the host sends `error` and ends the session,
   and `cleanup` releases held input and locks the desk again as usual.

A goroutine reads the WebSocket from the moment `hello` arrives, because
coder/websocket answers the client's pings only while reading and setup can
take several seconds. It reads with the connection's context, not
the session's: cancelling a pending read closes the connection, and a setup
error still has to reach the client. If the client leaves during setup, the
session ends quietly; an unexpected control-connection failure is logged as
a warning.

`cleanup` first takes control from the client: it cancels the session's
context, so `HandleInput` and the control loop drop whatever the client
still sends or has queued; closes the UDP session, which waits for a
datagram being handled and sends nothing more; and releases every held key,
button and pen contact (so Ctrl is not stuck after a dropped connection,
and no late key-down from the old client survives with a sequence number
that would make the next session's held-input snapshots look stale). A key,
button or pen contact counts as released only once its release is written
to uinput, on the device that got the press: a release that failed is sent
again by the next held-input snapshot, by cleanup or by the next session's
first snapshot, instead of leaving a key repeating. Then
it gives the desk back before anything that can hang: it locks the session
again if it was locked before, and turns the desk's monitors back on if the
session turned them off (they come back showing the lock screen, never the
unlocked desktop). Only then does it stop the video and sound pipelines,
waiting at most 5 s for each (`pipelineStopWait`): on 2026-11-24 a teardown
after a resume never returned, and with the old order the desk stayed dark
with every window on the tablet's screen while the next connection waited
for the cleanup. A teardown it stops waiting for goes on in the background,
tracked by the watchdog (see "Staying up"). Last it stops the screen cast,
restores the monitor layout and releases the idle inhibitor. Each of these
phases has its own 10 s for its D-Bus calls. When a new connection
replaces the session (the tablet roamed between networks), the old session
skips that relock and hands it to the new one, because locking between the two
would flash the lock screen and empty GNOME's clipboard. `sessionControl`
(`daemon/sessioncontrol.go`) keeps that duty safe: a new connection replaces
the running session only after its `hello` arrives, so one that drops or
stalls before then leaves the old session in control; and if a replacement
still fails before it took the relock over, the last session to end locks the
desk. From the `hello` on, the new connection reads its WebSocket, so it
answers the client's pings while it waits for the old session's cleanup:
on 2026-11-25 it read nothing during that wait, and the tablet gave up with
"sent ping but didn't receive pong". A client that leaves during the wait
gets no session, so the desk is not unlocked only to be locked again. A
client that sends more control messages than its session takes (64 waiting)
is disconnected, since a reader waiting for room would answer no pings and
miss the client leaving; clients send them only when the user acts. A
session that locks the desk as it finishes stays in control until the lock
is done, so a connection that comes meanwhile waits for it instead of
unlocking the desk under the lock, and a stopping daemon waits for it too.
When another device's connection replaces the session (the laptop connects
while the tablet streams), the old connection hears so at once, before the
cleanup: an `error` with reason `replaced` naming the new device. Its client
then ends the session instead of reconnecting, which would take the desk
back. The control reader reads with the connection's context, not the
session's, so the connection outlasts the replaced session long enough to
say so. The session's own messages to the client get a context of their own
too (`session.send`), and it sends nothing once it has ended: coder/websocket
closes a connection whose write's context ends during the write, which a
clipboard image going out in parts as the session is taken over would
otherwise do, and the notice would be lost. An image stops between parts once
the session has ended, and a message that waits behind another for longer
than its own deadline gives up without writing (`ControlConn.Send`). A
reconnect of the same device is not told. A resize or scale change
that finds the old video pipeline still stopping after `pipelineStopWait`
ends the session instead of starting another encoder on the same capture,
which could put two encoders' frames on one stream. If logind's
`LockedHint` cannot be read, the session counts as locked; a locked session
also counts as locked when the unlock call fails, since a reply lost on the
way may hide an unlock that worked, and locking a desk that stayed locked
changes nothing. Running apps are
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

The screen-cast session belongs to a remote desktop session
(`org.gnome.Mutter.RemoteDesktop`), which types the client's text (Input,
below): `RemoteDesktop.CreateSession`, its `SessionId` property, then
`ScreenCast.CreateSession` with `remote-desktop-session-id` set to it. Mutter
then starts and stops the cast only through the remote desktop session
(`Start`, `Stop`; the screen-cast session's own answer "Must be started from
remote desktop session"), so `Capture` calls those, and the two sessions
end together: stopping either from GNOME's indicator closes both. If
Mutter makes no remote desktop session, or refuses a cast in it, the session
gets the plain screen cast of before, logs a warning and cannot type text
(`keysyms` stays unset). Mutter cannot stop a remote desktop session that
never started; one left by a cast that failed before `Start` goes with
desklinkd's bus connection and holds nothing until then (only `Start` makes
its virtual devices and the indicator's handle).

GNOME's screen-sharing indicator looks the same either way: GNOME Shell
shows one indicator for every handle that is not a recording, and its stop
button stops each (`js/ui/status/remoteAccess.js`). Mutter gives the remote
desktop session a handle of its own besides the cast's, which changes
nothing visible, and animations stay on (`disable-animations` is not set).

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
2560×1600 at 120 Hz). Clients ask for their stream view's size in physical
pixels (the tablet's screen in either orientation, the laptop's window or
monitor), so the own screen reaches the client's screen pixel for pixel.
Mutter creates it from the stream: the pipeline puts
`video/x-raw(memory:DMABuf),width=…,height=…,max-framerate=…/1` after
`pipewiresrc`, and Mutter adds a "Virtual remote monitor" (`Meta-0`) of that
mode to the right of the others once the format is negotiated. Lower quality
levels keep the monitor's size and let `vapostproc` scale the stream. A
portrait screen gets the landscape ladder turned on its side (`ladder`), so
its rungs correspond to the landscape ones, and a `start` level remembered in
the other orientation is turned before the host looks it up (`turnedLike`).

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

When the session was locked as the client connected, nobody is at the desk,
so the session turns the desk's monitors off instead of putting them back
(`DisplayLayout.Only`): it applies, with the temporary method, a
configuration whose only logical monitor is the virtual one, at 0,0 and
primary. Mutter disables every monitor that a configuration leaves out, and
it accepts a configuration only if the layout starts at 0,0 and has one
primary. GNOME then moves every window onto the virtual screen, so a window
left on the desk, such as a terminal that moved there when an earlier
session's virtual monitor went away, shows up on the client. If Mutter
refuses the configuration, the session falls back to the desk's layout with
the virtual monitor primary to its right. When the desk was unlocked,
someone may be there, and its monitors stay on as they were.

The session sets the virtual monitor's scale (`hello.scale`, 200 % by default
and also when the hello asks for one outside 1 to 3, the nearest scale Mutter
supports), so a 2560×1600 tablet gets a 1280×800-point interface. A `scale`
message changes it during the session (`desktop.RescaleVirtual`): the desk's
monitors stay as they are at that moment, including changes made at the desk
during the session, and only the virtual monitor gets the new scale, right of
the rightmost desk monitor; desk monitors the session turned off stay off,
with the virtual monitor alone at 0,0. A scale that rounds to the current one
changes nothing. Otherwise the session maps input again and restarts the
pipeline, because Mutter's cast of the virtual monitor stops delivering frames
after a scale change. Either way it resends `session` with the applied scale.
It makes the virtual monitor primary (GNOME's top bar, overview and
notifications move to it) only when the session was locked as the client
connected. Beside an unlocked desk the virtual monitor is an extra monitor
with only the wallpaper until a window moves onto it, also when the client
asked for its own screen: someone at the desk keeps the top bar. A `primary`
message moves the primary during the session (`desktop.MovePrimary`): onto
the virtual monitor, or back onto the desk monitor that had it before the
session, or the first desk monitor if that one has gone. Nothing else moves,
so windows stay where they are. The session counts the move as its own
change: if the desk was still laid out as the session arranged it, the
arrangement now includes the move, so the end of the session restores the
layout from before the session, its primary included, as it would have
(below). With the desk's monitors off the virtual monitor stays primary, the
session refuses to move it and reports no `primary`, so clients show no
switch. A primary set this way survives a scale
change and a resize, and so does the client's choice to give it back: a
session that places its screen again uses the client's last choice, and
before any choice the locked-desk default.

A `resize` message gives the own screen a new size when the client's view
changed, as when the tablet rotates (`session.resizeOwnScreen`). Mutter
takes a virtual monitor's mode from the stream's caps only as the cast
starts, so the session stops the pipeline and the cast and starts both again
at the new size: if the session's desk monitors are off, it first turns them
on, as at the end of a session (`turnDeskOn`), so Mutter is never left
without a monitor; then it stops the cast, waits until the virtual monitor
has left the layout, starts a new virtual cast and pipeline, and places the
new screen (Mutter names it `Meta-1` and so on) with the same scale, the
desk's monitors off again if they were (found locked or turned off by the
client) and the client's primary choice. The quality ladder starts again for
the new size on the same rung from the client's quantizer, input maps onto
the new screen, relative motion is measured on the new size, and the session
resends `session`. The new quality controller keeps the old one's RTT
baseline and the lockout of a failed upward move, and counts reports only
2 s after the restart (`QualityController.Follow`), since the new pipeline
starts with a keyframe whose burst looks like congestion. Windows on the old
screen move to the primary monitor as it goes: with the desk's monitors off
they all come back to the new screen, but beside an unlocked desk they land
on the desk. A size the screen has already changes nothing; a mirroring
session refuses the message. Turning the desk's monitors on and stopping the
old cast run to the end even when the client leaves or another session takes
over meanwhile (godbus would not even send a call whose context has ended,
and a cast the session no longer knows would keep its virtual monitor, and
the desk dark, until desklinkd restarts); no new screen starts then. If the
new cast or pipeline cannot start, the session ends and its cleanup gives
the desk back. Each request restarts the screen, so clients wait until their
view's size has held for a moment before they send one.

On a desk the session found locked this means the desk's monitors come on
for the moment the screen is replaced, about a second, often less than a
monitor needs to show a picture again: they show the unlocked desktop that
the session otherwise keeps dark, with every window on it. Mutter cannot be
left without a monitor, and starting the new virtual monitor first would
make Mutter lay out every monitor again too, so the desk would flash either
way. Someone at the desk can glimpse the desktop during a rotation of the
tablet or a resize of the laptop's window; the desk's keyboard and mouse
work on the unlocked session all along, dark screens or not
(docs/security.md).

A `desk` message turns the desk's monitors off during the session, as for a
desk the session found locked (`DisplayLayout.Only`), so GNOME moves every
window onto the client's screen: on Wayland nothing outside GNOME Shell can
move another program's windows, so this is the way to bring windows left on
the desk to the client. Turning them on again applies the layout from before
the session with the virtual monitor to its right, and GNOME puts the windows
back on the monitors they came from. Only a session whose client turned the
monitors off turns them on: a session that found the desk locked keeps them
dark, because the desk is unlocked while it runs. Placing the screen again
keeps them off, and the end of the session turns them on as for a locked
desk (`session.giveDeskBack`). Input maps onto the virtual monitor's place in the
desktop like onto any monitor (below); with the desk's monitors off, that
place is the whole desktop.

At the end (`session.giveDeskBack`, before the pipeline stops), a session
that turned the desk's monitors off first turns them on again while the
virtual monitor still exists: the pre-connection layout, its primary included, with the virtual
monitor to its right, so Mutter is never left without a monitor
(`session.closeOwnScreen` does the rest once the pipeline has stopped). It skips
that step if Mutter has already closed the cast by itself (someone stopped
it from GNOME's screen-sharing indicator, the desk locked, or its PipeWire
stream failed), which `Capture.Open` sees because the cast's stream object
has left the bus. It asks for the stream, not the session: a screen-cast
session of a remote desktop session stays on the bus when only its stream
fails (Mutter 51, `on_stream_closed`).
Mutter frees the virtual monitor as soon as the cast closes but updates its
monitor list only later, when idle; until then it still reports the virtual
monitor, and a configuration applied in between crashes gnome-shell (Mutter
51, `meta_monitor_get_gamma_lut_size` on the freed monitor). Mutter turns
the desk's monitors on by itself, and the steps below run once the virtual
monitor has left the layout. Then it
stops the cast; the virtual monitor disappears and Mutter returns the desk's
monitors to the user's saved layout by itself. The session reapplies its
pre-connection layout only if that differs and the desk's monitors were still
laid out as the session arranged them just before the cast stopped
(`desktop.DeskUnchanged`); for a desk the session turned off, that means
every desk monitor was still off. If someone changed them during the
session, for example by plugging in a monitor, the session leaves the layout
to GNOME, which configures the connected monitors once the virtual one has
gone (below). If no desk monitor is on once the cast has stopped, the session
applies the pre-connection layout whatever changed. Mutter puts each window
back on the monitor it was opened on or last moved to by hand when that
monitor is on again, so windows from the desk return to it; the others move
to the desk's primary monitor when the virtual screen goes.

If desklinkd dies during a session, its D-Bus connection closes and Mutter
stops the cast. Destroying the virtual monitor makes Mutter configure the
remaining monitors itself: the user's saved configuration for the connected
monitors, otherwise a left-to-right layout of all of them, so the desk's
monitors come back on, though not in a pre-connection layout that was never
saved (Mutter 51: `on_virtual_monitor_destroyed`,
`meta_monitor_manager_ensure_configured`). A daemon that hangs without
exiting keeps the cast running, and the desk dark, until it is stopped.

A mirrored monitor is the hello's `monitor`, the client's last choice for
this host, when that monitor is on; otherwise `monitor` from the config when
it is connected, otherwise the monitor whose current shape is closest to the
client's, the primary one on a tie (`DisplayState.BestFit`): on a laptop with
a 16:9 external monitor and a 16:10 panel, a 16:10 tablet mirrors the panel.
Every `session` message lists the monitors that are on (`monitors`, without
the session's own screen) and the one mirrored (`mirroring`), for the
clients' monitor pickers. A `mirror` message switches a mirroring session to
another monitor that is on (`session.switchMirror`): the new cast starts
before the old one stops, so a monitor Mutter cannot record leaves the
session as it was; then the pipeline restarts on the new cast, input maps
onto the new monitor's place, and the session resends `session`. Mirroring
ignores the client's rotation: the stream keeps its size, and the client
letterboxes it.

## Staying up

desklinkd runs in the user's GNOME session, so two things keep it reachable:
systemd restarts the daemon while the session runs, and GDM opens a session
whenever there is none.

The user unit (`host/deploy/desklinkd.service`) restarts desklinkd after any
exit (`Restart=always`), waiting 2 s at first and up to 30 s between
attempts (`RestartSteps`, `RestartMaxDelaySec`), and never gives up
(`StartLimitIntervalSec=0`). It is `Type=notify`: desklinkd sends `READY=1`
once its control server and local socket listen (`daemon/watchdog.go`), then
asks its own `/health` every 10 s, pinned to its certificate, and sends
`WATCHDOG=1` each time that answers and no tracked operation has stalled:
a session's cleanup, a pipeline teardown cleanup stopped waiting for, the
start of a GStreamer pipeline (GStreamer changes its state synchronously,
where cancelling the session cannot reach), or the handling of a client's
control message or of a quality level change, that has run more than 30 s,
or a session's setup that has run more than 2 min (`daemon/stall.go`). A
session notices that it has ended only in its control loop, so a setup step
or a message whose handling hangs would otherwise keep the session, its
screen cast and its unlocked desk forever, with the next connection waiting
for it. Every setup step that waits on another process has a deadline of its
own: calls to Mutter's display configuration get 10 s when their caller gave
none (`boundedDisplay`), waking the display 5 s, moving the sound to the
DeskLink output 10 s. So an ordinary timeout fails the session and runs its
cleanup, and only a wait that nothing can cancel reaches the 2 min. A daemon
that hangs is
therefore killed (SIGABRT, so Go prints every goroutine's stack to the
journal, which names the call that hung) and restarted within
`WatchdogSec=30`. Its screen cast closes with its D-Bus connection, so Mutter
removes the virtual monitor and turns the desk's monitors on by itself
("If desklinkd dies during a session" above). A session that unlocked a
locked desk records the login session in `state.json` (`relock_session`)
before it unlocks the desk (when the desk is locked, its lock state cannot
be read, or the relock was handed over) and until the desk is locked again;
the state store keeps a change in memory only once the file is written, so
a save that failed is not mistaken for done when it is retried. So the next start locks a desk that a
killed or crashed run left open; a record from an earlier login is dropped
instead, so a session someone just opened is never locked. The probe goes to the control server's address, or to
127.0.0.1 when it listens on every address; if `allowed_subnets` leaves that
address out, the watchdog only checks that the process runs. The unit is
`PartOf` and `Requisite=graphical-session.target`: it stops with the session,
and a restart after the session ended fails instead of leaving a daemon that
would serve the next login with the old session's environment. The next
login starts it again. `ConditionUser=!gdm-greeter` and `!@system` keep it
out of the login screen's own session: the RPM's preset enables the unit for
every user, GDM's greeter user included, and a desklinkd started there
fought the user's for ports 9443 and 9444.

When the session ends (gnome-shell crashed, someone logged out) or the
computer boots, GDM shows its login screen and desklinkd is not running.
`desklinkd autologin on` makes GDM log the user in by itself, with both of
GDM's mechanisms, because neither covers everything:

- **Automatic login** (`AutomaticLoginEnable`, `AutomaticLogin`) logs in at
  once, but only once per GDM start (`did_automatic_login` in GDM 51's
  `gdm-manager.c`), so after a crash the login screen would wait.
- **Timed login** (`TimedLoginEnable`, `TimedLogin`, `TimedLoginDelay`, 10 s
  unless already set) logs in whenever the seat's login screen has been idle
  that long, at boot and after a session ended alike. Typing or picking
  another user on the login screen stops the countdown.

Both open the session through PAM's `gdm-autologin` service, so desklinkd
locks it at once (`autolock.go`): when logind's `Session.Service` is
`gdm-autologin` and the session started less than two minutes ago, it calls
`Session.Lock` every 2 s until `LockedHint` is set, for up to 30 s, because
GNOME Shell may not be listening yet. GNOME fades every new lock to black
(`ScreenShield.activate` always passes `fadeToBlack`), and gsd-power then
turns the monitors off, so someone at the desk after a reboot would find a
dark screen. desklinkd therefore moves its uinput pointer one pixel and back,
1 s and 3 s after the lock took (`showLockScreen`): GNOME counts that as user
activity, as it would a key press, lifts the black lightbox without
unlocking, and the lock screen stays until GNOME's usual idle blanking. No
D-Bus call does this in GNOME 51: `org.gnome.ScreenSaver` has no
`SimulateUserActivity`, and Mutter's `IdleMonitor.ResetIdletime` refuses
unless `MUTTER_DEBUG_RESET_IDLETIME` is set. A client's connection unlocks it as
usual and the session's cleanup locks it again. A daemon started later in the
day never locks the desk. Set `lock_after_autologin` to `false` for a machine
that logs in automatically on purpose and should stay open.

`desklinkd autologin on|off` writes `/etc/gdm/custom.conf`, the file GNOME
Settings' Automatic Login switch writes, through the hidden root command
`desklinkd gdm-login --user NAME on|off` run with `pkexec`: one
administrator's password, asked by GNOME's dialog in the session or in the
terminal over SSH. If pkexec fails, the error prints the same command for
`sudo`. `desktop.SetGDMLogin` changes only those keys of the `[daemon]`
section, in place, so the file keeps its other settings, comments, mode and
SELinux label; `off` turns off only what names this user. GDM reads the file
when it starts, so a change applies from the next boot.

Without either, desklinkd does not run until someone logs in, so a client
finds the computer on but refusing connections on port 9443, and the app says
so (docs/android.md, Wake-on-LAN). The daemon warns at startup when neither is
on, or when only automatic login is (a crash would then leave the desk at the
login screen); `desklinkd pair` prints an "Automatic login:" line, and
`desklinkd autologin` alone explains the setting.

## Addresses and discovery

`/health` and the pairing code list every IPv4 address a client may use
(`daemon.addresses`: interfaces that are up, not container or VM bridges,
inside `allowed_subnets`, the advertised one first), so a client paired at
home also knows the office or VPN address. desklinkd also announces
`_desklink._tcp` with mDNS through Avahi's D-Bus API on every interface
(`desktop/mdns.go`), carrying the certificate fingerprint; Avahi withdraws it
when the daemon's D-Bus connection closes. Without avahi-daemon the
announcement is skipped with a warning.

## Keeping awake after a session (`daemon/keepawake.go`)

A running session keeps the computer awake with an idle inhibitor. So that a
job started from the client (a build, an agent in a terminal) can go on after
disconnecting, the client may ask (`hello.keep_awake`, or `keep_awake` during
the session) to keep the computer from suspending for 1 to 1440 minutes, or
until a client connects again. When the last session ends, the host takes
`org.gnome.SessionManager`'s inhibitor with only the suspend flag: the desk
still locks and the screen still blanks, only GNOME's automatic suspend
waits. The next session, the time running out, the daemon stopping, or
unpairing the device that asked for it releases it; a session that was
replaced leaves it to the one that took over, and a session ended by
unpairing its device does not take it.

`keep_awake` -2 (Sleep) does the opposite: when the last session ends, the
host asks logind to suspend the computer (`Manager.Suspend`, which logind
allows the user of an active local session without polkit) after 20 s
(`suspendGrace`). A paired device's connection that is admitted within that
time stops the countdown at once (`keepAwake.deferSuspend`): waiting for a
replaced session's cleanup, the hello and unlocking the desk can take longer
than the grace period. Its session's start then cancels the suspend, so a
client that lost its network and reconnects keeps the computer running; a
connection whose session never starts (its client left during the wait)
schedules it again with a fresh 20 s. A client that does not reconnect finds
the computer asleep, and Wake-on-LAN brings it back.

`lock_after` (in `hello` or during the session) locks the desk when the
session ends even when it was unlocked as the client came, as for a desk
the session found locked, but only when no session takes over
(`sessionControl.leave`). A session that takes over, such as the same tablet
reconnecting after a network change, is not handed it: it would count the
desk as found locked, turn the desk's monitors off and keep them off, and
pick the own screen for `auto`. A desk the session found locked is still
handed over as before. When the session ends with nobody taking over, the
desk locks; a client that reconnects after that finds a locked desk, and its
new session treats it like any desk it finds locked (own screen for `auto`,
desk monitors off): that is what locking it means. `lock_after` is recorded
in `state.json` like the relock of a desk found locked (`noteRelockOwed`), so
a desklinkd that dies before the session ends locks the desk on its next
start (`relockLeftover`); turning it off during the session drops the
record, and a session that a successor replaces drops it too, since the
successor records its own if its client asks.

## Session history (`desklinkd sessions`)

Each session that started is recorded when it ends in `sessions.json`, the
newest 100: the device, its address, when it started and ended, what it
showed (`session.display`), the lowest quality level the stream went down
to (counting the level it started at) and why it ended (client left, failed with the error, taken over by
another device, reconnected, device unpaired, desklinkd stopped).
`desklinkd sessions` lists them newest first. The journal keeps only
warnings by default, so this is where to look for which sessions ran and
whether the picture was degraded.

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
(`User.Display`) and unlocks that. GNOME Shell reacts to logind's `Unlock`
signal by dismissing the lock screen. There is no fallback to
`Manager.UnlockSessions`: it unlocks every session on the machine, other
users' included, and the end of the session locks only the user's own again.
If the session cannot be found or unlocked, it stays as it was (the stream
shows the lock screen, where the password can be typed from the client) and
the session warns why. logind's properties are read with
`Properties.Get` under the caller's deadline, since godbus's `GetProperty`
takes no context.

## Monitor layout

DeskLink never changes the desk's monitors' modes, positions or scales: GNOME
users see that as screens switching under them. The exception is a session
with its own screen that found the desk locked, which turns the desk's
monitors off until it ends (above). A mirrored monitor is
streamed as it is and `vapostproc` scales it to the stream with black borders
for another shape. A layout saved in `state.json` by an older version (which
switched resolutions) is reapplied once at startup, only if the monitors
differ from it, and retried with fresh state when Mutter reports it stale.

## Encoder (`internal/video`)

```
pipewiresrc path do-timestamp keepalive-time=100 ! [own screen: size caps] ! queue max-size-buffers=2 leaky=downstream !
vapostproc add-borders=true ! video/x-raw(memory:VAMemory),format=P010_10LE,width,height,colorimetry=2:3:7:1 !
vaav1enc rate-control=cqp qp=<5×QP> key-int-max=1024 ref-frames=1 palette-mode=true target-usage=2 ! av1parse ! obu-stream/tu !
appsink sync=false max-buffers=8 drop=false
```

The HEVC variant uses `vah265enc rate-control=cqp qpi qpp b-frames=0` and
`h265parse config-interval=-1` to Annex B access units. Notes on the choices:

- `keepalive-time=100` makes PipeWire resend the last frame on a static
  screen, so a forced keyframe is produced within 100 ms.
- `key-int-max=1024` is the longest GOP the encoders take: losses are
  repaired by forced keyframes, which clients ask for in every telemetry
  report until one arrives, so the periodic keyframe is only a backstop
  (every 17 s at 60 fps, 102 s on a static screen). At 120 frames it was the
  stream's heaviest load: in a minute of mixed desktop work at 60 fps it sent
  30 keyframes, 9.5 of the 13 MB; on a static screen a 272 KB keyframe every
  12 s (195 kbit/s, now 26).
- `target-usage=2` is the speed preset. On Mesa's radeonsi driver (RX 7800
  XT) it encodes a 2560×1600 frame, conversion included, in about half the
  time of the default 4: scrolling 5–8 ms against 13–14 ms (95th
  percentile 8–11 ms against 30–37), video 7–7.6 ms against 10.3–10.9, so
  120 fps fits. It costs 10–25 % more bytes while scrolling, 3 % on video, and
  0.1–0.3 dB on still text. 1 sent ten times the bytes while scrolling; 6
  and 7 take twice as long as 4.
- Rate control stays CQP. VBR looked attractive because changing `min-qp`
  does not restart the sequence, but on this driver `min-qp` and `max-qp`
  take effect only when the encoder reconfigures (a `qp` or `bitrate`
  change, which sends a keyframe), `cpb-size` has no effect, and a VBR
  keyframe uses the quantizer the screen had refined to: up to 695 KB at a
  floor of index 40, and 540 KB in the minute of work above, where CQP at
  index 60 sent the same bytes (6.6 against 6.7 MB). So the encoder offers
  no keyframe-free quality control besides the frame rate, and CQP keeps
  keyframe sizes predictable.
- A pad probe on `vapostproc`'s input (`frameLimiter`) caps the frame rate at
  the client's request by dropping frames that arrive sooner than the
  interval, judged by their timestamps. `videorate` cannot do this: Mutter's
  screen cast has a variable frame rate (`framerate=0/1`), which videorate
  passes through. `Encoder.SetMaxFPS` changes the cap while running.
- `colorimetry=2:3:7:1` is limited range with the BT.709 matrix and
  primaries and the sRGB transfer. Limited range is the range every decoder
  assumes. Full range is signalled in the AV1 header, but the Tab S9's
  Qualcomm decoder ignores it and shows the video as limited, which deepens
  blacks and oversaturates colours; at 10 bits limited range still has 877
  levels per channel. The transfer matches the captured desktop's, sRGB, so
  `vapostproc` converts only the matrix and range. With the BT.709 transfer
  (`2:3:5:1`, before 0.22) it re-encoded the sRGB values through the BT.709
  curve, which lifted every mid-tone (grey 128 became 10-bit luma 528
  instead of 504) and made the stream look washed out on every client.
  `TestConversionKeepsDesktopColours` checks the luma of flat colours on the
  GPU. The encoders signal only the range in the bitstream, so clients
  assume BT.709 for the rest.
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

## Exact tiles (`internal/tiles`, `video/scan.go`)

Text in the AV1 stream is close to the desktop but not exact, and coloured
text loses colour detail to 4:2:0 before compression. When the client asks
(`hello.tiles`, docs/protocol.md), the host sends the exact pixels of every
64×64 tile of the stream that has not changed for 200 ms (`SettleDelay`), and
says, for every encoded frame, which of the tiles it sent that frame changed;
the video still carries every change and the first picture.

**Where the pixels come from.** pipewiresrc offers no damage information
(1.6 turns only the cursor position into metadata), so the host compares
pixels. With tiles asked for, the pipeline splits the capture:

```
pipewiresrc ... ! [size caps] ! tee name=split ! queue max-size-buffers=2 leaky=downstream ! <encoder as above>
split. ! queue name=scanqueue max-size-buffers=1 leaky=downstream ! vapostproc add-borders=true !
  video/x-raw,format=BGRx,width,height,pixel-aspect-ratio=1/1 ! appsink name=scan sync=false max-buffers=1 drop=false
```

Each branch has its own leaky queue, so a slow scan never delays the encoder
(with a queue on only one branch the other stalled in tests). The scan
branch's `vapostproc` scales and letterboxes as the encoder's does, so a tile
lands 1:1 on the stream's pixels, and downloads BGRx to system memory;
`TestScanBranchOnGPU` checks that the scanned pixels are the source's and
carry the timestamps of the frames the encoder encodes. A probe at the scan
queue drops frames while tiles are off (`Encoder.SetScanning`) and caps the
branch at the encoder's rate. The appsink callback maps each frame through
C and releases it at once (the buffer trap in AGENTS.md); the tracker keeps
only hashes and copies of settling tiles.

**Settling and judging frames (`tiles.Tracker`).** Each scan hashes every
tile's red, green and blue bytes (xxHash64's round over four lanes; the
padding byte is ignored) and notes which changed. A tile unchanged for the
settle delay is copied out of the scan. Scans and encoded frames meet by the
source timestamp, which both branches see on the same buffer; the session
reports each encoded frame (`Frame.PTS`) after sending it. For every encoded
frame, in order, the tracker sends a `tile_frame` listing the tracked tiles
whose hash differs in that frame's scan, then starts tracking the tiles
captured from that scan, numbered with that frame's seq. A frame whose scan
was skipped (the scan queue dropped it, or 16 frames wait) invalidates every
tile (`all`), as do the first frame of a pipeline and timestamps that go
backwards. A tile captured from a frame the encoder never encoded goes back
to settling and is captured again from the next scan. A change after a
capture but before its frame was encoded is still reported, by the frame
that carries it, so the client never keeps a tile over a frame that changed
it. A moving cursor is part of the frames (`cursor-mode` embedded), so the
tiles under it change and are resent once it rests.

**Sending (`tiles.Pacer`, `tiles.Writer`).** The pacer sends the most
recently settled tiles first and, among them, the smallest first, at most 16
tiles (48 KB of QOI ops) per `tile_data` message, within 2 MB/s
(`BudgetBytesPerSecond`, 16 Mbit/s) with a 64 KB burst, and only while fewer
than 128 KB wait for the WebSocket. A tile whose part changed before it went
out is dropped; one checked and queued under the tracker's lock stays ahead
of any `tile_frame` that invalidates it. A tile whose QOI ops exceed 1.5
bytes a pixel (photographs, gradients, which the video shows well) is not
sent and waits for its next change. The writer puts messages on the
WebSocket in order on its own goroutine, so neither GStreamer's threads nor
the pacer wait for the network; if 512 messages or 4 MB pile up it drops
them all, and everything after until the tracker's next `tile_frame` with
`all`. A `tiles` message from the client restarts the tracker; the session
logs the tile totals at its end.

**Cost, measured** on this desk (i9-11900KF, RX 7800 XT) at 2560×1600: the
scan costs about 1.45 ms of one core per frame, nearly all of it reading the
downloaded 16 MB once, so about 9 % of a core at 60 frames a second and 1.5 %
on a still screen (10 keepalive frames a second); the encoder's latency was
unchanged (median 11.3 ms with and without the branch) and no encoded frame
missed its scan. A full screen of settled text (the research's desktop image:
documents, a terminal, small text) is 399 KB in 63 messages, sent within
190 ms of settling; a sparse desktop is 12 KB. QOI and zlib for a full
screen take about 30 ms of CPU.

## Adapting to the connection (`video/quality.go`)

`QualityController` judges each telemetry report against the connection's own
history, for manual presets and Auto alike. Queueing delay is the RTT minus
the lowest RTT of the last minute, so a steady 30 ms office VPN path counts as
clean while a growing queue does not. RTT is measured every 2 s but reports
arrive every 250 ms, so delay counts as congestion only after two consecutive
late samples (40 ms of queueing); one late echo is not eight bad reports. A
report is bad at 2 % loss or with that delay, and clean under 0.5 % loss and
15 ms.

What blurs text is a smaller size, not a coarser quantizer. Measured on a
synthetic 2560×1600 desktop at 200 % through the real conversion and encoder
(AV1, decoded with dav1d; luma PSNR of 100 % text and of a document):

| Stream | 100 % text | Document | Keyframe | Video, 2 s of motion |
|---|---|---|---|---|
| 2560×1600, index 60 (QP 12, Auto and Sharp) | 43.4 dB | 46.7 dB | 336 KB | 3.6 Mbit/s |
| 2560×1600, index 90 (QP 18, the old floor) | 41.8 dB | 44.0 dB | 272 KB | 2.4 Mbit/s |
| 2560×1600, index 125 (QP 25, middle step) | 37.7 dB | 39.5 dB | ~200 KB | ~1 Mbit/s |
| 2560×1600, index 190 (QP 38, ceiling) | 32.2 dB | 34.9 dB | 139 KB | 0.5 Mbit/s |
| 2560×1600, index 220 | 30.4 dB | 30.2 dB | 118 KB | 0.4 Mbit/s |
| 1920×1200 at index 90, scaled up | 19.6 dB | 23.6 dB | 197 KB | |

Every full-size row keeps all of the text's edge detail; the coarse ones add
faint ringing. Past index 190 document and terminal text lose 4–5 dB a step
against about 2 dB before it, so 38 is the default `max_qp`. Scrolling costs
about 2 KB a frame at any quantizer and typing 50–300 bytes; keyframes and
video are the load. Every `qp` change makes the encoder start a new sequence
with a keyframe; a frame-rate change costs nothing.

- **Stepping down:** while the reports stay congested the controller sheds
  load in a fixed order. With Auto, first the frame rate at the full size,
  after 1 s of congestion per step (2560×1600 at 120, 60, then 30 fps); then
  the quantizer, after 2 s per step, in two large steps from the floor to the
  middle of the range and to `max_qp` (12 → 25 → 38; one keyframe each, where
  the old controller sent one every second, 2 at a time); only then the
  size, after 3 s at the ceiling and at most every 10 s, keeping the
  quantizer at the ceiling (1920×1200 at 30 fps, then 1280×800). A window
  counts as congested when its reports average 2 % loss or 75 % of them show
  delay, which catches bursty loss that single reports miss; each window
  starts after the last change and after the first congested report of the
  episode, so a brief burst long after the last loss does nothing.
- **Grace:** the reports in the second after a quantizer change and the two
  seconds after a size change (a pipeline restart) are not counted: they
  measure that change's own keyframe, which used to look like congestion,
  step the quality down again and fail every probe up.
- **Stepping up, sharpening when still:** after clean periods the
  controller undoes the steps in reverse order. A smaller size probes back
  up first, after 15 clean seconds, at the ceiling, where the full-size
  keyframe is small (139 KB). Then the quantizer returns to the floor in one
  step once the link has been clean for 5 s and the screen still for 1 s
  (no encoded frame above 128 bytes; an unchanged 2560×1600 frame is about
  35 bytes, a typed character or a blinking cursor 50–300), or after 20
  clean seconds if the screen never stills. Its keyframe then competes with
  nothing, and the frames after it refine a still screen to the floor's
  quality: in a minute of desktop work with congestion from 37 to 43 s,
  100 % text was at 37.7 dB on the middle step and back at 43.3 dB after
  the return at 48 s. The quantizer then stays at the floor while the user
  works: going coarse again whenever the screen moves would send a keyframe
  at the start of every scroll, while staying fine costs 15 % more while
  scrolling, nothing while typing, 22 % on a window switch and 48 % on video.
  Last, the frame rate probes back up, after 15 clean seconds per step.
- **Failed upward moves:** an upward move is watched for 15 s; if any
  second of reports after its grace averages congestion, it is undone at
  once and the next upward move waits for a lockout of 1 minute, doubling
  with each failure in a row up to 10 minutes; a move that holds resets it.
- **Applying a level:** the UDP goroutine only queues the new level; the
  session loop applies it, with the level's quantizer (setting that first on
  the old, larger pipeline would raise its bitrate). A frame-rate step changes
  the frame limiter in the running pipeline. A size step stops the pipeline,
  switches the monitor mode, remaps the pen and pointer and starts a new
  pipeline, so the client sees one keyframe's pause. Every change is reported
  in a `session` message with `level` and `levels`.
- **Frame-rate cap:** a client whose panel something holds below its
  highest rate (the tablet in power saving at 60 Hz) sends `max_fps`
  (docs/protocol.md). The ladder stays as the hello asked for it, so `start`
  and the levels clients remember keep their meaning; the encoder runs at
  the level's rate or the cap, whichever is lower (`video.CappedFPS`,
  `session.streamFPS`), through the same frame limiter a frame-rate step
  uses, so the cap costs no keyframe, and `session.fps` reports that rate.
  The own screen keeps its mode (120 Hz), since changing it would restart the cast;
  the limiter drops the frames above the cap before conversion and
  encoding. The controller (`SetFrameRateCap`) treats the levels of one size
  that the cap makes stream alike as one, the first of them: under a 60 fps
  cap the 120 and 60 fps levels stream alike, so a step down goes straight
  to 30 fps instead of a step that changes nothing and a lost second, a
  probe back up lands on the 120 fps level (which streams at 60), and a
  session that ends under the cap leaves the client remembering the top. A
  cap that rises or is lifted raises the rate at once and is watched like
  an upward move: congestion within 15 s puts the stream back on the
  level of the old rate with the usual lockout, and during a lockout the
  stream goes to that level at once. For a cap between the ladder's rates,
  such as 90, that level is the next slower one (60 fps). The session loop
  applies the controller's level as it is when it gets to it, so a level
  the UDP goroutine queued before the cap moved it changes nothing. A new
  ladder for a resized own screen keeps the cap.

Manual presets have a one-level ladder, so only the quantizer moves, between
their request and `max_qp`, with the same steps and the same return when
still. A `fixed` hello (the Sharp preset) changes nothing during the session:
the quantizer the client asked for, which may lie below `min_qp` down to 8
(`video.FixedMinQP`), at the top level. The clients remember the last level
per host and network (the Android network type, or the laptop's interface
name) by its size and frame rate, never more than one level below the top,
and send it as `start`; the host starts at the best rung no larger and no
faster (`LevelFor`), but at most one level below the top (`StartLevel`), so
every session starts at the full size even when the last one ended on a
bad link, or a client from before this rule remembered a smaller level. A
`start` with the top's frame rate or more starts at the top whatever its
size: clients ask for their view's size, so a session after one in a
smaller window or view remembers a smaller size, which says nothing about
the link.

In the minute of work above, encoded at 60 fps, the old settings (index 90,
120-frame GOP, speed preset 4) sent 13.0 MB with 30 keyframes on a clean
link and 35 keyframes with the congestion episode; the new ones (index 60,
1024-frame GOP, speed preset 2) sent 6.7 MB with 4 keyframes clean and 5.8
MB with 5 keyframes in the episode, with still text 1.5 dB sharper (100 %
text 43.3 against 41.8 dB) and encoding 6.7 ms a frame on average instead of
10.9. The measurement scripts are kept outside the repository; the numbers
come from an AMD RX 7800 XT with Mesa 26.2.7 and GStreamer 1.28.9.

## Sound (`desktop/audio.go`, `video/audio.go`)

When `hello.audio` is set, the session moves the desktop's sound to the
client. `RedirectAudio` loads a null sink named `desklink` ("DeskLink") with
`pactl load-module module-null-sink` and makes it the default output;
WirePlumber moves the streams that follow the default along with it, so the
desk's speakers fall silent. The previous default is saved in `state.json`
(`audio_default_sink`). `pulsesrc device=desklink.monitor` captures what
plays there with a 10 ms buffer, `opusenc frame-size=10 bitrate=128000
audio-type=restricted-lowdelay` encodes it, and each frame goes out as one
audio datagram (docs/protocol.md). If the sink cannot be made the default,
`RedirectAudio` restores the previous default and unloads the sink with a
5 s deadline of its own (a `set-default-sink` that timed out may still have
worked); if that fails too, the session keeps the route so its cleanup tries
again. Cleanup stops the pipeline, makes the
saved output the default again and unloads the sink; at startup the daemon
unloads any `desklink` null sink a crashed run left and restores the saved
default. If the null sink cannot be made, the default output's monitor
(`@DEFAULT_MONITOR@`) is streamed and the desk keeps playing; if capture
fails, the session goes on without sound. pactl talks to PipeWire through
pipewire-pulse; the RPM requires `pulseaudio-utils` and
`gstreamer1-plugins-good` (pulsesrc).

### The client's microphone (`desktop/microphone.go`, `video/microphone.go`, `daemon/microphone.go`)

When `hello.mic` or a `mic` message turns it on, `CreateMicrophone` gives
the desk a virtual microphone that desktop apps (calls, dictation) record:

1. `pactl load-module module-null-sink sink_name=desklink_mic_feed
   channel_map=mono` ("DeskLink microphone feed"), the output the session
   plays the client's sound into;
2. `pactl load-module module-remap-source source_name=desklink_mic
   master=desklink_mic_feed.monitor channel_map=mono` ("DeskLink
   microphone"), which turns that output's monitor into a proper
   `Audio/Source`, listed by apps that hide monitors;
3. `pactl set-default-source desklink_mic`, so apps that follow the default
   use it; WirePlumber moves streams that follow the default along.

A single null sink created with `media.class=Audio/Source/Virtual` would be
one device instead of two, but WirePlumber 0.5 links a playback stream to a
named target only when that target takes input, so `pulsesink
device=desklink_mic` falls back to the default output and the microphone
stays silent; both behaviours were checked against PipeWire 1.6.11 and
WirePlumber 0.5.19 in a private instance. The feed shows in GNOME's output
list while the microphone exists.

The previous default source is saved in `state.json` (`mic_default_source`).
If a step fails, what was made is removed again with a timeout of its own;
if that fails too, the session keeps the route and cleanup retries it.
`MicRoute.Restore` makes the previous source the default again only while
the virtual microphone still is (a source the user picked meanwhile stays;
pipewire-pulse's `@DEFAULT_SOURCE@` placeholder, which it reports when
there was no source, is never set), then unloads the remapped source before
its feed, clearing each part it has undone so a retry does only the rest. At
startup the daemon unloads every leftover `desklink_mic` remap and
`desklink_mic_feed` sink of a crashed run and restores the saved default.

`video.Microphone` plays the frames: `appsrc is-live=true format=time
max-buffers=3 leaky-type=downstream ! opusdec plc=true ! audioconvert !
audioresample ! audio/x-raw,rate=48000,channels=1 ! pulsesink
device=desklink_mic_feed sync=false buffer-time=40000 latency-time=10000`.
The UDP goroutine only puts each frame in a jitter buffer
(`video/jitter.go`, ordered by the 16-bit counter); a playout goroutine
hands the decoder one frame slot at a time, scheduled by the frames'
durations from their TOC bytes:

- playout starts once 40 ms is queued and starts at the oldest frame
  queued; more than 60 ms queued (a burst, or a client clock that runs fast)
  is dropped back to 40 ms, so the delay cannot grow;
- a frame whose turn has passed is dropped as late, never played late;
- a missing frame is concealed: a GAP event through the appsrc, queued in
  order with the buffers, makes opusdec run Opus packet-loss concealment
  for its duration;
- after 100 ms of concealment (the client stopped sending, as when the app
  goes to the background) playout stops, the output plays silence, and it
  starts again at the next frame that comes, 40 ms later; a counter more
  than 50 frames away from the expected one starts the buffer afresh.

Buffers are pushed through C (`gst_buffer_new_memdup`,
`gst_app_src_push_buffer`), so no go-gst wrapper holds a reference. The
UDP goroutine plays a frame only while the session's microphone is on, and
`session.cleanup` removes the microphone after the sound; turning it off
removes it at once. If it cannot be made, `session.mic` reports false (a
`mic` message also gets `error`; a hello does not, since an error before
the first datagram ends the session on the client).

## Clipboard

Mutter does not implement the wlroots or ext data-control protocols, so
`wl-paste --watch` exits at once on GNOME. The host tries it and, when it
fails, reads the clipboard 250 ms after a copy shortcut typed through
DeskLink (Ctrl+C, Ctrl+X, Ctrl+Shift+C, Ctrl+Insert) and on
`clipboard_request`. Copies made with the mouse reach the tablet through
"Get clipboard". Incoming text is written with `wl-copy`. On GNOME
wl-clipboard briefly maps a surface to get focus for each call. `wl-copy`
forks a child that serves the text until another program takes the
clipboard, and the child keeps `wl-copy`'s stdout and stderr open, so they go
to an unlinked temporary file, never a pipe: waiting for a pipe to close
waited for the child, and on 2026-11-25 hung the session for minutes.

Images go both ways as PNG, up to `max_clipboard_image_bytes` (default
8 MiB, at most 16 MiB, 0 for text only), with clients that say they take them
(docs/protocol.md, "Clipboard images"). For such a client a read first lists
the offered types (`wl-paste --list-types`, a second surface on GNOME); a
clipboard with `image/png` and no plain text is read with `wl-paste --type
image/png`, anything else as text, as before. The session sends the image
only up to the smaller of its own limit and the client's, and sends an image
it reads again only when it changed (by its SHA-256). An image from the
client is put on the clipboard with `wl-copy --type image/png`, behind the
same temporary file as text. A `clipboard_request` is answered off the
control loop, since an image's parts can take seconds on a slow link, and
one lock keeps two reads from interleaving their images' parts.

## Files from clients

`POST /files` (docs/protocol.md) saves a file a paired device sends in the
user's download folder: what `xdg-user-dir DOWNLOAD` prints, or
`~/Downloads` when it fails, prints nothing or prints the home directory
itself, which the XDG spec uses for a folder that is not set
(`desktop.DownloadDir`, asked again for each file). The folder is created if
missing. Saved files are readable by the user only (0600). No session is
needed: the tablet's share sheet and the laptop's Send file… work whether a
session runs or not, and a file does not unlock or wake the desk. Each saved
file shows "Saved <name> from <device>" at the desk, naming the folder;
clicking it, or its Show in Files button, opens the file manager with the
file selected. `max_upload_bytes` (default 2 GiB) limits a file; 0 refuses
every file.

## Notifications

`desktop.Notifier` shows notices through `org.freedesktop.Notifications` on
the session bus, as "DeskLink". Client-chosen names (the device's paired
name, a file name) go into the summary, which notification servers show as
plain text, with control and formatting characters removed and at most 64
characters (`daemon/notices.go`); the body, which GNOME Shell reads as markup,
is escaped. A notice about a file offers the `default` action (a click on the
notification) and a Show in Files button; the notifier listens for
`ActionInvoked` and calls `org.freedesktop.FileManager1.ShowItems`, which
Nautilus implements, so nothing is executed. It remembers the files of the
last 32 notices and forgets one when its notice closes.

A session announces itself with "<device> connected and unlocked this
computer" when its own unlock opened the desk, and "<device> connected"
otherwise (the desk was open, or another device's session had unlocked it).
A reconnect of the same device, which replaced its own session and kept the
desk as it was, shows nothing, so a tablet roaming between networks does not
fill the notification list. The notice is sent in the background with 5 s to
answer and stays in GNOME's notification list, so whoever comes back to the
desk sees who was connected. `notify_session_start: false` turns it off.

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

### Typing text (`desktop/remotedesktop.go`, `daemon/typing.go`)

Keys from the clients are evdev codes, which the host's layout turns into
characters, and the tablet's soft keyboard can only send the keys of a US
layout. Text that needs more arrives as `type` messages (docs/protocol.md),
typed through the remote desktop session's own virtual keyboard: each
character's keysym is pressed and released with `NotifyKeyboardKeysym`, in
order. `desktop.Keysym` picks the keysym as libxkbcommon's
`xkb_utf32_to_keysym` does (and gnome-remote-desktop with it): the code point
for ASCII and Latin-1, a legacy keysym where one exists (`Cyrillic_a` for
"а", `aogonek` for "ą", `EuroSign`), else 0x01000000 + code point; the
legacy table `keysyms_table.go` is generated from the installed libxkbcommon
(`go generate ./internal/desktop`). Newline and carriage return press Return,
tab Tab and U+0008 BackSpace; other control characters are skipped. The
calls go out without waiting for each reply (Mutter handles one connection's
calls in order), and a press always gets its release, even when the session
ends meanwhile; stopping the remote desktop session also releases what its
keyboard holds. The text is never logged: it may be a password.

What Mutter types depends on the active layout. Its native backend looks for
a key carrying the keysym in the current layout group only, at the levels
plain, Shift and AltGr, presses Shift or AltGr for it, and drops a keysym the
layout lacks with only a warning in the journal ("No keycode found for keyval
… in current group", `meta-virtual-input-device-native.c`); the D-Bus call
still succeeds. So on a German host "z", "ä" and "€" come out right, but "é"
on a US host, Cyrillic while a Latin layout is active, and emoji on any
host are dropped. A keysym at level four (Shift+AltGr) comes out without its
modifiers, and Caps Lock applies as usual. Typing what the layout lacks would
need Mutter's `SetKeymap`, which replaces the keymap for every keyboard of
the desk; it is not used.

The lock screen: GNOME Shell inhibits remote access while the session is
locked (session mode `unlock-dialog` has `allowScreencast` false, and
`main.js` calls `inhibit_remote_access`), and Mutter then closes every
screen-cast and remote desktop session and refuses new ones ("Session
creation inhibited"). Text therefore never reaches the lock screen's password
field; the session's cast closes with it as it always did, and uinput keys
are unaffected.

Keys and text keep the order the client sent them in (docs/protocol.md,
"Text and keys"): `readControl` puts `type` messages into a queue of their
own (`textQueue`), which a goroutine of the session types, and
`HandleInput` holds key records back while a text the host read before them
waits or is being typed, and 5 ms after its last keysym, since Mutter
answers each call once it has queued the key for its input thread and a
uinput key written at once could get there first. It never waits itself:
it runs on the UDP goroutine that every datagram shares, microphone frames
and keepalives included. A key record that must wait goes to a worker of the
session, and so does every input record after it, keys or not, until the
worker has injected them all, which keeps the client's order; then input is
injected at once again. Keys wait at most 1 s for one text, and records
behind the same text do not wait again; without waiting keys, the pointer,
buttons and the pen are injected at once. Cleanup waits for the worker before
it lifts every held key, so no key it injects stays down. Text that arrives
while a resize or mirror switch replaces the screen waits for the new
screen's cast, at most `captureTimeout` (15 s), instead of failing; the
switch waits for a text being typed to finish before it stops the old cast.

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
  "min_qp": 12,
  "max_qp": 38,
  "lock_after_autologin": true,
  "notify_session_start": true,
  "max_upload_bytes": 2147483648,
  "max_clipboard_image_bytes": 8388608
}
```

`notify_session_start` shows a notification at the desk when a device
starts a session ("Notifications"). `max_upload_bytes` is the largest file a
device may send, 0 to refuse files ("Files from clients");
`max_clipboard_image_bytes` the largest clipboard image synced, at most
16 MiB, 0 for text only ("Clipboard").

`advertise_ip` goes into the pairing QR code and picks the interface whose MAC
`/health` reports for Wake-on-LAN. Empty means detected: the source address of
the default route, or else the first private address of an interface that is
up and not a container or VM bridge (`lxdbr*`, `docker*`, `virbr*`, …). Set it
when clients reach the host on another address, such as a VPN's. `monitor` is
the connector to mirror (`DP-1`, `eDP-1`; `gnome-monitor-config list` shows
them) when the client has chosen none; empty means the monitor shaped most
like the client's screen (see "The session's own screen"). Connections and datagrams from
outside `allowed_subnets` are closed or dropped before TLS or decryption. The
listeners bind IPv4 only. `min_qp` and `max_qp` bound the quantizer of
adaptive sessions on the HEVC scale (AV1 uses five times the value): the
finest a client may ask for and the coarsest the host goes to under
congestion before it lowers the size (see Adapting to the connection).
Fixed-quality sessions may go below `min_qp`, down to 8.
