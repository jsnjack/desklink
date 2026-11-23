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
   QR code, a JSON line and a PIN.
   On the laptop, either open DeskLink, choose **Pair with code** and paste the
   JSON line, or run:

   ```bash
   desklink pair '{"ip":"192.168.178.11","port":9443,"pin":"123456","fingerprint":"…"}'
   ```

   The JSON line carries the certificate fingerprint, which the laptop pins.
   Typing only the PIN (`desklink pair 192.168.178.11 --pin 123456`) trusts the
   first certificate seen; compare the fingerprint it prints with the desktop's.

3. **Connect.** Open DeskLink, pick a quality and codec, and choose **Connect**
   on the host. **Auto**, the default, lets the host lower the size and frame
   rate when the link cannot keep up and raise them again when it recovers;
   the laptop remembers the level per host and network interface (for
   example the WireGuard interface in the office) and starts there next
   time. The desktop unlocks and shows the laptop its own screen or a
   mirrored monitor, like the tablet (docs/desktop.md); the laptop client
   sends no `screen`, so the host's default `auto` applies. If the host does not
   answer and its MAC is known (stored at pairing from `/health`), Connect
   sends Wake-on-LAN packets and waits up to 90 seconds for it
   (`client.WakeAndWait`).

4. **While connected:**
   - The keyboard goes to the desktop, including Super and Alt+Tab once GNOME
     has allowed the window to inhibit shortcuts (it asks the first time).
   - The mouse is absolute: the laptop pointer maps onto the desktop, and the
     desktop's own cursor is what you see. Buttons and the wheel (smooth on a
     touchpad) go through too.
   - **Ctrl+Alt+Shift+F** toggles fullscreen and **Ctrl+Alt+Shift+Q**
     disconnects; every other combination reaches the desktop. In fullscreen,
     move the pointer to the top edge to show the toolbar.
   - The toolbar has the app drawer (Bunk Terminal first), **Send clipboard**,
     **Get clipboard**, the link numbers (`RTT | FPS | Mbps`) and Disconnect.
     The laptop clipboard is also sent when the window regains focus.

Hosts and their peer keys are stored in `~/.config/desklink-client/hosts.json`
with mode 0600. `--debug` logs to stderr and `--trace` writes
`/tmp/desklink.log`.

## Networks

A host keeps the address that answered last and the others it reported
(`client.Profile.Addresses`, from the pairing code and every `/health`).
`client.Reach` asks them all for `/health` at once with the pinned
certificate and connects to the first that answers; the host list and Connect
use it, and Wake-on-LAN sends towards every address, failing only when no
packet could be sent. What they learn is merged into the saved host
(`Store.Learn`), never written over it. Pairing with a code finds a saved
host by its certificate before its address; pairing a machine that is
already saved under another entry folds them into the oldest one, which keeps
its name and remembered levels and takes the new pairing and address
(`Store.Absorb`). While the window is open, `client.Discover` browses
`_desklink._tcp` through Avahi's D-Bus API: a paired host (same certificate
fingerprint) found at a new address gets it added, a host announced on
several interfaces is gone only when it has left them all, and an unknown one is
listed under "Found on this network" with a Pair button that opens the
pairing dialog with the announced fingerprint filled in.

## Sound

The laptop client always asks for the desktop's sound and plays it with
`appsrc ! opusdec plc=true ! audioconvert ! audioresample ! pulsesink
buffer-time=60000 latency-time=10000 sync=false` (`viewer/audio.go`): each
packet plays as soon as it arrives, Opus conceals lost packets, and packets
beyond eight queued are dropped so the sound stays with the picture.

## Design

- `internal/client` has no GUI: the host store, pairing over pinned TLS, the
  WebSocket session, the UDP transport, `FrameAssembler` (the same rules as the
  Android one) and the held-input snapshot. `client_test.go` runs a full pair,
  connect, stream and input exchange against the host's real control and media
  servers.
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
- Each connection attempt has a generation number: Disconnect, even while
  still connecting, abandons the attempt, closes a session that completes
  afterwards and drops events from any session but the current one. Control
  requests, closing and resolving the host run off the GTK thread, so a dead
  link never freezes the window. Shortcut inhibition is requested as soon as
  a session starts, and the header bar returns whenever fullscreen ends.
- `hosts.json` is shared by the GUI and `desklink pair`: every change reloads
  it under an exclusive lock (`hosts.json.lock`), so neither overwrites the
  other's edits.
- The host injects kind 6 through a fourth uinput device, an absolute mouse
  like a virtual machine's tablet, and maps it through the same borders and
  monitor placement as the pen.
- `make smoke` runs `TestWindowSmoke` under `scripts/headless-gui.sh`: it builds
  the window and decodes real VA-API AV1 frames into the picture on a private
  X server and bus.
