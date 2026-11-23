# Binary RPMs for DeskLink. The Makefile builds the binaries first and passes
# the source tree as srcdir; rpmbuild does not compile Go here. Build with:
# make rpm
%global debug_package %{nil}
# Releases are tracked as git tags, not an RPM %%changelog.
%global source_date_epoch_from_changelog 0

Name:           desklink
Version:        %{?appversion}%{!?appversion:0.0.0}
Release:        1%{?dist}
Summary:        DeskLink laptop client: show and control a desktop running desklinkd
License:        MIT
URL:            https://github.com/jsnjack/desklink
ExclusiveArch:  x86_64

Requires:       gtk4
Requires:       libadwaita
# gtk4paintablesink shows the decoded video in the window.
Requires:       gstreamer1-plugin-gtk4
# av1parse, h265parse and the VA-API decoders.
Requires:       gstreamer1-plugins-bad-free
# Software AV1 decoding when the GPU has no VA-API decoder.
Recommends:     gstreamer1-plugin-dav1d

%description
desklink shows a GNOME desktop that runs desklinkd in a window on a Fedora
laptop, over the LAN or a VPN, and sends the laptop's keyboard and mouse to it.
Video is AV1 or 10-bit HEVC decoded on the GPU.

%package -n desklinkd
Summary:        DeskLink host: stream this GNOME desktop to a tablet or laptop
Requires:       pipewire-gstreamer
# vapostproc and the VA-API AV1 and HEVC encoders.
Requires:       gstreamer1-plugins-bad-free
# Fedora's Mesa build leaves out HEVC; RPM Fusion's freeworld build has it.
Requires:       (mesa-va-drivers or mesa-va-drivers-freeworld)
Requires:       wl-clipboard
# gio launches apps from the client's app drawer.
Requires:       glib2
Requires:       systemd
%{?systemd_requires}
# Arms Wake-on-LAN on wired cards at install time; NetworkManager keeps it on.
Recommends:     ethtool

%description -n desklinkd
desklinkd captures the GNOME desktop through Mutter's screen-cast API, encodes
it on the GPU with VA-API, and streams it as encrypted UDP to the DeskLink
Android app or the desklink laptop client, injecting their keyboard, mouse,
touch and pen input through uinput. It runs as a systemd user service, enabled
for every graphical session, and arms Wake-on-LAN on wired connections.

%install
install -Dm0755 %{srcdir}/bin/desklink %{buildroot}%{_bindir}/desklink
install -Dm0644 %{srcdir}/host/deploy/com.desklink.Client.desktop \
    %{buildroot}%{_datadir}/applications/com.desklink.Client.desktop
install -Dm0755 %{srcdir}/bin/desklinkd %{buildroot}%{_bindir}/desklinkd
# The unit in the tree starts ~/.local/bin/desklinkd for `make install-host`.
install -d %{buildroot}%{_userunitdir}
sed 's|%%h/.local/bin/desklinkd|%{_bindir}/desklinkd|' %{srcdir}/host/deploy/desklinkd.service \
    > %{buildroot}%{_userunitdir}/desklinkd.service
install -Dm0644 %{srcdir}/host/deploy/60-desklink.rules %{buildroot}%{_udevrulesdir}/60-desklink.rules
install -Dm0644 %{srcdir}/packaging/80-desklinkd.preset %{buildroot}%{_userpresetdir}/80-desklinkd.preset
install -Dm0644 %{srcdir}/packaging/60-desklink-wake-on-lan.conf \
    %{buildroot}%{_prefix}/lib/NetworkManager/conf.d/60-desklink-wake-on-lan.conf
install -Dm0644 %{srcdir}/LICENSE %{buildroot}%{_licensedir}/desklink/LICENSE
install -Dm0644 %{srcdir}/LICENSE %{buildroot}%{_licensedir}/desklinkd/LICENSE

%check
desktop-file-validate %{buildroot}%{_datadir}/applications/com.desklink.Client.desktop
grep -q '^ExecStart=%{_bindir}/desklinkd$' %{buildroot}%{_userunitdir}/desklinkd.service

%post
update-desktop-database &>/dev/null || :

%postun
update-desktop-database &>/dev/null || :

%post -n desklinkd
%systemd_user_post desklinkd.service
%udev_rules_update
# Apply the uaccess rule to the existing /dev/uinput now, not at the next boot.
udevadm trigger --subsystem-match=misc --sysname-match=uinput &>/dev/null || :
nmcli general reload conf &>/dev/null || :

%posttrans -n desklinkd
# Arm Wake-on-LAN on wired cards that are already up; NetworkManager keeps it
# on from the next time they connect.
if command -v ethtool >/dev/null; then
    for dev in /sys/class/net/*; do
        [ -e "$dev/device" ] && [ ! -e "$dev/wireless" ] && [ "$(cat "$dev/operstate" 2>/dev/null)" = up ] || continue
        ethtool -s "${dev##*/}" wol g &>/dev/null || :
    done
fi
# Start desklinkd now in running graphical sessions, instead of at the next
# login. Upgrades restart it through %%systemd_user_postun_with_restart.
for user in $(loginctl list-users --no-legend 2>/dev/null | awk '{print $2}'); do
    sd="systemctl --user --machine=$user@.host"
    $sd is-active --quiet graphical-session.target &>/dev/null || continue
    $sd daemon-reload &>/dev/null || :
    $sd is-enabled --quiet desklinkd.service &>/dev/null && $sd start desklinkd.service &>/dev/null || :
done

%preun -n desklinkd
%systemd_user_preun desklinkd.service

%postun -n desklinkd
%systemd_user_postun_with_restart desklinkd.service
%udev_rules_update
nmcli general reload conf &>/dev/null || :

%files
%license %{_licensedir}/desklink/LICENSE
%{_bindir}/desklink
%{_datadir}/applications/com.desklink.Client.desktop

%files -n desklinkd
%license %{_licensedir}/desklinkd/LICENSE
%{_bindir}/desklinkd
%{_userunitdir}/desklinkd.service
%{_udevrulesdir}/60-desklink.rules
%{_userpresetdir}/80-desklinkd.preset
%{_prefix}/lib/NetworkManager/conf.d/60-desklink-wake-on-lan.conf
