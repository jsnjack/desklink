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

%description -n desklinkd
desklinkd captures the GNOME desktop through the ScreenCast portal, encodes it
on the GPU with VA-API, and streams it as encrypted UDP to the DeskLink
Android app or the desklink laptop client, injecting their keyboard, mouse,
touch and pen input through uinput. It runs as a systemd user service.

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

%preun -n desklinkd
%systemd_user_preun desklinkd.service

%postun -n desklinkd
%udev_rules_update

%files
%license %{_licensedir}/desklink/LICENSE
%{_bindir}/desklink
%{_datadir}/applications/com.desklink.Client.desktop

%files -n desklinkd
%license %{_licensedir}/desklinkd/LICENSE
%{_bindir}/desklinkd
%{_userunitdir}/desklinkd.service
%{_udevrulesdir}/60-desklink.rules
