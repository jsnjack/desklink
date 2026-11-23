# DeskLink

DeskLink shows a Fedora GNOME desktop on a Galaxy Tab S9 over the FRITZ!Box
VPN, with the tablet's keyboard, trackpad, touchscreen and S-Pen controlling
the desktop. The desktop's GPU encodes the screen as AV1 or 10-bit HEVC, and
the tablet decodes it in hardware, so text stays sharp at up to 120 frames a
second.

When the tablet connects, the desktop unlocks, stops blanking and switches to
the tablet's 16:10 resolution if the monitor supports it. Disconnecting puts
the monitor back and leaves running apps open.

## Install from a release

Each [release](https://github.com/jsnjack/desklink/releases) has two Fedora
RPMs and the Android APK:

```bash
# On the desktop:
sudo dnf install ./desklinkd-*.x86_64.rpm
systemctl --user enable --now desklinkd

# On a laptop:
sudo dnf install ./desklink-*.x86_64.rpm

# For the tablet, with USB debugging on:
adb install -r desklink.apk
```

HEVC encoding needs RPM Fusion's `mesa-va-drivers-freeworld`; AV1 works with
Fedora's own Mesa.

## Set up the desktop from source

```bash
sudo dnf install golang gstreamer1-devel gstreamer1-plugins-base-devel \
    pipewire-gstreamer gstreamer1-plugins-bad-free wl-clipboard
make install-host
systemctl --user enable --now desklinkd
```

`make install-host` installs `~/.local/bin/desklinkd` and the systemd user
unit. Fedora already lets the logged-in user create virtual input devices; on
other systems install `host/deploy/60-desklink.rules` as the command prints.

Settings live in `~/.config/desklink/config.json`; the defaults listen on
ports 9443 (TCP) and 9444 (UDP) and advertise `192.168.178.11`. See
[docs/desktop.md](docs/desktop.md#configuration).

## Set up the tablet

```bash
make install        # builds bin/desklink.apk and runs adb install -r
```

## Pair

At the desk, run:

```bash
desklinkd pair
```

It prints a QR code and a six-digit PIN, valid for five minutes, and GNOME
asks once whether DeskLink may share the screen; allow it. In the app, tap
**Scan pairing QR**. Later connections start without any dialog.

## Connect

Choose a quality preset and tap **Connect**. **Auto**, the default, adapts the
resolution and frame rate to each connection and remembers where it settled
on the office VPN and on home Wi-Fi. A three-finger tap, or the pill
at the right edge, opens the HUD with the app drawer (Bunk Terminal first),
clipboard buttons, the on-screen keyboard and Disconnect.

## Connect from a laptop

`desklink` is the same remote desktop in a window on a Fedora laptop, over the
same VPN.

```bash
sudo dnf install gstreamer1-plugin-gtk4
make install-laptop
```

At the desk, run `desklinkd pair`, then on the laptop paste the JSON line it
prints into **Pair with code** in DeskLink (or run `desklink pair '<JSON>'`).
After that, open DeskLink and choose **Connect**. The keyboard and mouse go to
the desktop; Ctrl+Alt+Shift+F toggles fullscreen and Ctrl+Alt+Shift+Q
disconnects. Step-by-step details are in [docs/laptop.md](docs/laptop.md).

## Development

See [AGENTS.md](AGENTS.md) for the code map and commands, and `docs/` for the
protocol, desktop integration, Android client and security model.
