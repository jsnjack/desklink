# DeskLink

DeskLink shows a Fedora GNOME desktop on a Galaxy Tab S9 over the FRITZ!Box
VPN, with the tablet's keyboard, trackpad, touchscreen and S-Pen controlling
the desktop. The desktop's GPU encodes the screen as AV1 or 10-bit HEVC, and
the tablet decodes it in hardware, so text stays sharp at up to 120 frames a
second.

When the tablet connects, the desktop unlocks and stops blanking. If the
computer was locked (you are away), the tablet gets its own screen: a
virtual monitor of its size at 200 % with GNOME's top bar, readable at arm's
length. If you are using the computer, it mirrors the monitor closest to the
tablet's shape and changes nothing at your desk. The app's **Screen** setting
and HUD button override this. Disconnecting removes the tablet's screen; its
windows move to your monitors and running apps stay open.

## Install from a release

Each [release](https://github.com/jsnjack/desklink/releases) has two Fedora
RPMs and the Android APK:

```bash
# On the desktop (enables and starts desklinkd for every graphical session):
sudo dnf install ./desklinkd-*.x86_64.rpm

# On a laptop:
sudo dnf install ./desklink-*.x86_64.rpm

# For the tablet, with USB debugging on:
adb install -r desklink.apk
```

The desklinkd package also gives the user at the computer access to
`/dev/uinput` for input injection, and turns on Wake-on-LAN for wired
connections.

To update the tablet app, open https://jsnjack.github.io/android-apps/ on the tablet and tap
**Install** under DeskLink; your hosts and pairings stay.

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
unit. Input injection also needs the udev rule the command prints, unless the
desklinkd RPM is installed.

Settings live in `~/.config/desklink/config.json`; the defaults listen on
ports 9443 (TCP) and 9444 (UDP) and put the address of the default route in
the pairing QR code. See [docs/desktop.md](docs/desktop.md#configuration).

## Reach the desktop when it sleeps or after a reboot

- **Asleep.** Connect wakes a host that does not answer: the app sends
  Wake-on-LAN packets and connects once the host is up (up to 90 seconds).
  This needs a wired connection; `desklinkd pair` says whether Wake-on-LAN is
  ready, and the RPM turns it on in NetworkManager. Waking from power-off
  also needs it enabled in the BIOS.
- **Rebooted, nobody logged in.** desklinkd runs inside your GNOME session,
  so turn on automatic login (Settings → System → Users → Automatic Login).
  desklinkd locks the screen as soon as the session starts, and connecting
  unlocks it as usual. See
  [docs/desktop.md](docs/desktop.md#locking-after-automatic-login) for the
  trade-offs.

## Set up the tablet

```bash
make install        # builds bin/desklink.apk and runs adb install -r
```

## Pair

At the desk, run:

```bash
desklinkd pair
```

It prints a QR code and a six-digit PIN, valid for five minutes. In the app,
tap **Scan pairing QR**, or **Add host** and **Pair** with the PIN. From then
on the tablet connects with nobody at the desk: there is no sharing dialog.

Android keeps Win, Win+key and Alt+Tab for itself. To send them to the
desktop, tap **Settings** next to "Win and Alt+Tab" in the app and switch on
"DeskLink keyboard shortcuts"; it only takes keys while a session is on
screen.

`desklinkd peers` lists the paired devices and when they last connected;
`desklinkd peers remove <name or ID>` forgets one. **Forget** on a host in
the app or the laptop client does the same from the device's side.

## Connect

Choose a quality preset and tap **Connect**. **Auto**, the default, adapts the
resolution and frame rate to each connection and remembers where it settled
on the office VPN and on home Wi-Fi. A three-finger tap, or the pill
at the right edge, opens the HUD with the app drawer (Bunk Terminal first),
clipboard buttons, the on-screen keyboard and Disconnect.

## Home, office, VPN

Pair a machine once. The app knows every address it has (home LAN, office
LAN, VPN), tries them all when you connect and uses whichever answers first,
and finds it by itself on any local network (mDNS). A machine you paired twice
on different networks is merged into one entry. Auto quality is remembered
per network.

## Sound

While a tablet or laptop is connected, the desktop's sound plays on it
instead of the desk's speakers; disconnecting gives the speakers their sound
back. Turn it off in the app with **Play the desktop's sound here**.

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
