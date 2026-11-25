# Laptop client

`desklink` is a GTK 4 / libadwaita app for a Fedora laptop. It speaks the same
protocol as the Android app and lives in the host's Go module, so it reuses
`internal/net` (packet codec) and `internal/crypto` (pairing and keys) instead
of a second implementation.

## Connecting from a laptop

The laptop must reach the desktop the same way the tablet does: on the home
LAN, or through the FRITZ!Box WireGuard VPN from the office.

1. **Install the client on the laptop** (Fedora):

   ```bash
   sudo dnf install gtk4 libadwaita gstreamer1-plugin-gtk4 gstreamer1-plugins-bad-free
   make install-laptop        # ~/.local/bin/desklink and a launcher entry
   ```

   Building needs `gtk4-devel libadwaita-devel gstreamer1-devel
   gstreamer1-plugins-base-devel`. Hardware decoding uses the laptop GPU's
   VA-API driver (`vaav1dec`, `vah265dec`); without it the client falls back to
   `dav1ddec` or `avdec_h265`.

2. **Pair once, at the desk.** On the desktop run `desklinkd pair`. It prints a
   QR code and, below it, the same pairing code as a line of JSON. On the
   laptop, either open DeskLink, choose **Pair with code** (or **Pair** on a
   host) and paste that line, or run:

   ```bash
   desklink pair '{"ip":"192.168.178.11","port":9443,"pin":"123456","fingerprint":"…"}'
   ```

   The code carries the desktop's address, the PIN and its certificate
   fingerprint, and the laptop trusts only that certificate. A PIN alone is
   not accepted, and a code without a fingerprint is refused
   (`client.ParsePairingCode`): the PIN only feeds a key derivation that the
   other side can recompute, so whoever answered a PIN-only pairing could
   try all million PINs offline and then pair with the real desktop.
   `client.PairWithCode` asks the code's addresses for `/health` with that
   certificate pinned, pairs at the first that answers, and saves the MAC,
   the host's other addresses and, for a new host, its hostname from that
   pinned answer.

3. **Connect.** Open DeskLink, pick a quality and codec, and choose **Connect**
   on the host. **Auto**, the default, asks for 2560×1600 at QP 12 and lets
   the host lower the frame rate, then the detail, and the size only as a
   last resort when the link cannot keep up, and undo that when it recovers
   (docs/desktop.md); the laptop remembers the level per host and network
   interface (for example the WireGuard interface in the office), at most
   one level below the top so that the next session starts at the full size
   (`client.LevelToRemember`), and starts there next time. **Sharp** asks
   for the same size and quantizer with `fixed`, so the host never changes
   them. The desktop unlocks and shows the laptop its own screen or a
   mirrored monitor, like the tablet (docs/desktop.md). Until you choose one
   under **Settings** (below), the hello sends no `screen` and the host's
   default `auto` applies: the own screen only when the desk was locked. The
   quality preset sets the quantizer and frame rate; the stream and the own
   screen have the size in device pixels of what shows them
   (`viewer.ownScreenSizeFor`): in full screen the monitor's, in a window the
   picture's, the part of the window under the header bar, each side made
   even and within 320 to 4096. The size is read just before the hello, once
   the stream page has been laid out; before the window has a size the
   preset's stands in. In a window the picture's size is the right one,
   because the stream then fills the window pixel for pixel: a screen the
   size of the monitor would be scaled down into the window, which blurs
   text, and the window's size is also what the user chose to give the
   desktop. The hello always sends a scale: the one you chose, on the
   nearest 25 % step from 100 to 200 %, or else the scale of the laptop
   monitor showing the window as it is (`gdk_monitor_get_scale`, fractional
   with GNOME's fractional scaling, 100 % if the monitor is unknown, kept
   within 100 to 200 %; `viewer.requestScale`), which the host rounds to the
   nearest scale Mutter supports. With the own screen at the window's device
   pixels and the monitor's scale, text on the desktop has the size of the
   laptop's own text and reaches the screen unscaled. Without a scale the
   host would use 200 %, which turns a 1920×1200 own screen into a
   960×600-point desktop.

   During an own-screen session the screen follows the window: once a
   second (with the link numbers) the client compares the size it would ask
   for with the own screen's (the top of `session.levels`), and when a new
   size has held for two looks it sends `resize` once (`screenFollower`), so
   going full screen or resizing the window gives the desktop the new size
   after a second or two, and a window still being dragged asks nothing.
   Each resize restarts the own screen on the host (docs/desktop.md); beside
   an unlocked desk the windows on it then move to the desk. In full screen
   the monitor's size counts, so the header bar sliding in at the top edge
   asks nothing. A mirroring session keeps its size, and the picture
   letterboxes it.
   The hello also sends the saved keep-awake choice. If the host does not
   answer and its MAC is known (stored at pairing from the pinned `/health`), Connect
   sends Wake-on-LAN packets and waits up to 90 seconds for it
   (`client.WakeAndWait`).

   The stream counts as ready when the first authenticated UDP packet from
   the host arrives, not when the host describes the stream over the
   control connection: the host sends nothing over UDP until a packet from
   the laptop has told it where to, so that first packet shows the path
   works both ways. Until then the status card stays up. If nothing arrives
   within 3 seconds, or nothing for 5 seconds later on, the card says so and
   names the UDP port, which a firewall or the VPN may be blocking. After 10
   seconds without a packet the session ends with that reason
   (`client.ErrMediaLost`) and the window returns to the host list. The host
   sends a keepalive every 2 seconds and echoes the laptop's, so a working
   link is never that quiet.

4. **While connected:**
   - The keyboard goes to the desktop, including Super and Alt+Tab once GNOME
     has allowed the window to inhibit shortcuts (it asks the first time).
   - The mouse is absolute: the laptop pointer maps onto the desktop, and the
     desktop's own cursor is what you see. Buttons and the wheel (smooth on a
     touchpad) go through too.
   - **Ctrl+Alt+Shift+F** toggles fullscreen and **Ctrl+Alt+Shift+Q**
     disconnects; every other combination reaches the desktop. In fullscreen,
     move the pointer to the top edge to show the toolbar.
   - The toolbar has the app drawer (Bunk Terminal first), **Send file…**,
     **Send clipboard**, **Get clipboard**, **Settings**, the link numbers (`RTT · delay | FPS | Mbps`) and
     Disconnect. The delay runs from capture on the host to the frame
     reaching the GTK sink: Push stamps each frame with the pipeline's running
     time, decoders keep it, and a probe on the sink finds the frame's capture
     time by that stamp.
     The laptop clipboard is also sent when the window regains focus,
     unless a password manager marked it secret: KeePassXC and others offer
     the extra target `x-kde-passwordManagerHint`, and the client skips
     content that offers it without reading the hint's value. **Send
     clipboard** sends it anyway.
   - Clipboard images go both ways when the host takes them (its `session`
     message says how large, docs/protocol.md): the hello says the laptop
     takes 8 MiB. A clipboard that offers `image/png` and no plain text is
     read as a GDK texture, encoded as PNG (`gdk_texture_save_to_png_bytes`)
     and sent in parts (`Session.PushClipboardImage`, which refuses an image
     over the host's limit before sending). An image from the host becomes a
     texture on the laptop clipboard, which GTK offers to other apps as PNG
     and other formats. The automatic push on focus skips the window's own
     clipboard, which holds the host's image (GTK would encode it again, so
     it would not match), an image it sent already (by its SHA-256) and an
     image over the host's limit; **Send clipboard** sends the image anyway
     and reports the limit. To an older host the clipboard goes as text, as
     before.
   - **Send file…** picks files to send to the desk, and so does dropping
     files on the stream. They are saved in the desk's download folder
     (docs/desktop.md, "Files from clients") one after another, because the
     host takes one file per device at a time (`viewer/transfer.go`). A
     card at the bottom of the stream shows the file being sent, its place
     in the batch and how much has gone, and a toast gives the name the host
     saved it under, or why it refused it. Folders and files without a local
     path are skipped. Disconnecting cancels what is still being sent.
     `client.SendFile` needs no session: it reaches the host at any of its
     addresses, authenticates with a fresh token and streams the file with
     `Expect: 100-continue`, so a file the host refuses is not sent.
   - **Settings** opens a popover with the tablet's screen and keep-awake
     choices (`viewer/settings.go`). **Own screen** / **Mirror** shows what
     the session shows; choosing the other saves it and connects again,
     because a session keeps the screen it started with. When the host lists
     more than one monitor (`session.monitors`), a row of their connectors
     follows, the mirrored one active, each with its mode and whether it is
     primary as a tooltip: choosing one switches a mirroring session to it
     at once (`mirror`), or connects the own screen again to mirror it, and
     the choice is saved for the host (`Profile.Monitor`) and sent as
     `hello.monitor`, so later mirroring sessions start on it. For the own screen,
     − and + change its scale in 25 % steps from 100 to 200 %, at once
     (`scale`); its subtitle says why 100 % and 200 % are sharpest (GNOME
     stretches XWayland apps at the others). **Primary screen** puts GNOME's top bar and overview on the
     laptop's screen until you disconnect (`primary`), and **Desk screens
     off** moves every window there until the desk's monitors turn on again
     (`desk`); each switch appears only while the host's `session` message
     includes it. **Keep awake after disconnect** (Off, 1 h, 4 h, ∞ until
     a client connects again, or Sleep, which suspends the computer 20 s
     after the session unless a client reconnects) keeps the computer from
     suspending after the session, so a job left running goes on
     (`keep_awake`), and **Lock the desk when I disconnect** locks it at the
     end even when it was unlocked as you connected (`lock_after`, saved
     and sent in each hello). **Microphone** streams this computer's
     microphone to the desk (see Sound). The popover
     shows the host's latest `session` message, which the host sends again
     after every change. A switch the host refuses gets an `error`, shown
     as a toast, and the popover returns to the last `session` message.
     The screen, scale, keep-awake and monitor choices are saved in
     `hosts.json` for later connections; the two switches last for the
     session only.

Hosts, their peer keys and the stream and screen choices are stored in
`~/.config/desklink-client/hosts.json` with mode 0600. `--debug` logs to stderr and `--trace` writes
`/tmp/desklink.log`.

## Networks

A host keeps the address that answered last and up to seven others
(`client.Profile.Addresses`, `client.MaxAddresses`), newest first: those the
host reported in its latest pinned `/health` or pairing code come before
older ones, and the oldest are dropped. Addresses that arrive from the
network (`/health`, the pairing code's list, mDNS) must be IP addresses
another machine can answer at (`client.UsableAddress`: not unspecified,
loopback, multicast, broadcast or link-local); an address the user typed is
kept as typed. `client.Reach` asks every address for `/health` at once with
the pinned certificate and connects to the first that answers, except that
an address where a session in the last 10 minutes got HTTPS through but no
UDP datagram is used only when no other answers, so the next Connect after
such a failure tries the host's other addresses; the host
list and Connect use it, and Wake-on-LAN sends towards every address,
failing only when no packet could be sent. The host list's check also sends
an authenticated UDP probe to the address that answered (`client.ProbeUDP`,
the media port from `/health`) and says whether the stream can reach it,
and says which side to update when the host and the laptop speak different
protocol versions (`protocol` in `/health`). A host that is not paired has no
certificate to pin, so it is not asked at all. What `Reach` learns is merged
into the saved host (`Store.Learn`), never written over it, and only while
the saved host is still paired with the certificate that answered. As on
the tablet, the code decides which saved host it pairs: the one with the
code's certificate (the same machine on any network), else the host whose
**Pair** was used (a reinstalled desktop's new certificate replaces the old
one, and the toast says so), else the one at the code's address and port,
else a new host. Pairing a machine that is already saved under another
entry folds them into the oldest one, which keeps its name and remembered
levels and takes the new pairing and address (`Store.Absorb`).

While the window is open, `client.Discover` browses `_desklink._tcp` through
Avahi's D-Bus API. An announcement is unauthenticated, so it is only a hint.
When it names a paired host's certificate at a new address,
`client.Confirm` asks that address for `/health` with the pinned
certificate, and the address is saved only if the host answers there. A
host announced on several interfaces is gone only when it has left them
all. An unknown host is listed under "Found on this network"; its **Pair**
opens the pairing-code dialog and saves nothing from the announcement. If
the pasted code's fingerprint is not the announced one, the toast says so
and the pairing uses the code's.

**Forget** asks for confirmation, then `client.Forget` asks a paired host to
drop this laptop's pairing (`DELETE /pair`) and the host is removed here
either way. Only an HTTP 200 counts as removed on the host. The host answers
401 both to a device it no longer knows and to a token it cannot verify (for
example after its clock changed), so a 401 returns
`client.ErrForgetUnconfirmed` and the toast says the host could not confirm,
with the `desklinkd peers remove` command to run there.

## Sound

The laptop client always asks for the desktop's sound and plays it with
`appsrc ! opusdec plc=true ! audioconvert ! audioresample ! pulsesink
buffer-time=60000 latency-time=10000 sync=false` (`viewer/audio.go`): each
packet plays as soon as it arrives, Opus conceals lost packets, and packets
beyond eight queued are dropped so the sound stays with the picture.

The **Microphone** switch in **Settings** (off by default, saved in
`hosts.json` as `mic`) lets desk apps hear this computer as "DeskLink
microphone" (docs/desktop.md). It shows only when the host reports `mic` in
`session`. Turning it on sends `mic {"on":true}` (and later hellos ask with
`mic`); capture starts once `session.mic` is true (`micWanted`), with
`pulsesrc buffer-time=40000 latency-time=10000 ! audioconvert !
audioresample ! audio/x-raw,format=S16LE,rate=48000,channels=1 ! opusenc
frame-size=20 bitrate=32000 audio-type=voice ! appsink`
(`video.StartMicrophoneCapture`, the default source), and each frame goes
out as a type-7 datagram (`Session.SendMicrophone`). Turning it off stops
capture at once; a disconnect stops it too. PipeWire does not cancel echo by
default, so with the desk's sound on the laptop's speakers a call hears
itself: use a headset, or PipeWire's echo-cancel source as the default.

## Design

The laptop client does not ask for exact tiles (docs/protocol.md):
`gtk4paintablesink` shows each decoded frame on its own schedule, so the
window cannot tell which frame is on screen and could not take a tile away
with the frame that changes its part.

- `internal/client` has no GUI: the host store, pairing over pinned TLS (an
  empty fingerprint trusts no certificate; a `/health` or `/pair` reply
  over 64 KiB fails to decode), the WebSocket session, the UDP transport, `FrameAssembler` (the same rules as the
  Android one) and the held-input snapshot. The transport drops a host packet
  whose sequence number it has already seen or that is 1024 or more behind
  the newest, the same window the host uses, before the packet counts for
  the stats, the loss and clock estimates or the 10-second deadline. An
  incomplete frame keeps only the first parity packet of each group, indexed
  by group; a packet checks only its own group and counts the missing members
  before it rebuilds a payload. An `error` with reason `replaced` ends the
  session (`client.ErrReplaced`): another device took it over. When a
  session ends for any reason, the transport first sends the release of
  every key and button it holds, then an empty held-input snapshot; the host
  also releases everything when the session ends. `client_test.go` runs a full pair,
  connect, stream, input and screen-settings exchange against the host's real
  control and media servers.
- `internal/viewer` decodes with `appsrc ! parser ! VA-API decoder !
  vapostproc ! gtk4paintablesink sync=false` (`videoconvert` after a
  software decoder) and shows the sink's GdkPaintable in a GtkPicture. The
  converter turns the 10-bit stream into what the sink accepts, and passes it
  through when nothing needs converting. When more than 8 frames wait for a
  slow decoder, the client skips to the next keyframe and asks for one; three
  decoder failures within 30 s end the session. The paintable is fetched with `g_object_get` and wrapped as a
  gotk4 object, because go-gst and gotk4 have separate GObject bindings.
- Input: hardware keycodes minus 8 are evdev codes; motion becomes input kind 6
  (absolute pointer) normalised to the letterboxed picture; buttons come from a
  legacy controller so several can be held. Keys and buttons held when the
  window loses focus are released, a button GTK no longer reports as pressed
  (GNOME took the pointer) is released on the next motion, and the key state
  resets with every session.
- Typing: keys go to the host as evdev codes, so the host's layout decides
  what they type. When the host types text (`session.keysyms`), a dead key
  or Compose starts a composition instead (`viewer/compose.go`): that key and
  every key until the composition ends go to GTK's input method (an
  `IMMulticontext`, IBus on GNOME), and its commit, "é" from ´ then e, goes to
  the host as a `type` message (`Session.TypeText`), whatever dead keys the
  host's layout has. A key's release goes where its press went, so the host
  never gets a release alone. The composition ends with the commit, when the
  input method declines a key (which then goes to the host as usual), or
  when the window loses the focus; one cancelled without a commit ends at
  the next key. Other input-method commits are not taken: plain keys never
  reach the input method, so shortcuts and key repeat work as before, and
  IBus hotkeys such as its emoji picker reach the host's own IBus. Hosts
  without `keysyms` get the dead keys as raw keys, as before. `TypeText`
  returns at once: one goroutine of the session sends the messages, at most
  1000 code points each, in order, so a stalled link never freezes the
  window. Input sent while text waits to go out goes through the same
  goroutine (`Session.SendInput`), after the text has been written to the
  WebSocket and 20 ms longer (`textLead`, as on the tablet), because the host
  keeps keys behind text only once it has received the text
  (docs/protocol.md); then input goes out at once again. Neither call blocks
  the GTK thread.
- Each connection attempt has a generation number: Disconnect, even while
  still connecting, abandons the attempt, closes a session that completes
  afterwards and drops events from any session but the current one. A
  clipboard read is asynchronous, so it carries the generation and session
  that started it (`viewer/ticket.go`): its text goes to that session only,
  and is dropped if the session has ended or been replaced by the time the
  read finishes. Control
  requests, closing and resolving the host run off the GTK thread, so a dead
  link never freezes the window. Shortcut inhibition is requested as soon as
  a session starts, and the header bar returns whenever fullscreen ends.
- Host names, `/health` fields, error texts and announcements come from the
  network, so every row, toast and dialog shows them as plain text: action
  rows and toasts parse Pango markup by default in libadwaita, and the
  viewer turns that off (`plainRow`, `window.toast`, `alert`).
- `hosts.json` is shared by the GUI and `desklink pair`: every change reloads
  it under an exclusive lock (`hosts.json.lock`), so neither overwrites the
  other's edits.
- The host injects kind 6 through a fourth uinput device, an absolute mouse
  like a virtual machine's tablet, and maps it through the same borders and
  monitor placement as the pen.
- `make smoke` runs `TestWindowSmoke` under `scripts/headless-gui.sh`: it builds
  the window, puts a clipboard image from the host on the private display's
  clipboard as a texture, and decodes real VA-API AV1 frames into the
  picture on a private X server and bus. `transfer_test.go` in
  `internal/client` sends files and clipboard images to the host's real
  control server.
