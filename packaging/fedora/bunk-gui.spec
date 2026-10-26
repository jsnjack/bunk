# bunk-gui: GTK4 terminal built on bunk's emulator core.
#
# Build with "make rpm" from the repository root. As in mailbox, the
# Makefile builds the binary with go build, which reuses the Go build cache,
# and stages the linked modules' licenses next to it (licenses.sh) in
# stagedir. rpmbuild compiles nothing: it installs those files and the
# repository's (srcdir). Pass the version with --define "bunk_version
# X.Y.Z"; the Makefile takes it from monova.

%{!?bunk_version:%global bunk_version 0.0.0}
%global appid io.github.jsnjack.BunkGui
# The binary is built outside rpmbuild, so there is no debug info to split.
%global debug_package %{nil}

Name:           bunk-gui
Version:        %{bunk_version}
Release:        1%{?snapinfo:.%{snapinfo}}%{?dist}
Summary:        Terminal for running coding agents side by side

# bunk, vt10x, creack/pty, rivo/uniseg: MIT; gotk4, gotk4-adwaita: MPL-2.0;
# gotk4/core: ISC; gotk4/cairo/swizzle, x/sync: BSD-3-Clause.
License:        MIT AND MPL-2.0 AND ISC AND BSD-3-Clause
URL:            https://github.com/jsnjack/bunk

BuildRequires:  desktop-file-utils
BuildRequires:  appstream

# The renderer loads OpenGL with dlopen, so RPM cannot see these.
Requires:       libglvnd-egl
Requires:       libglvnd-opengl

Provides:       bundled(golang(github.com/creack/pty)) = 1.1.24
Provides:       bundled(golang(github.com/diamondburned/gotk4/pkg)) = 0.4.1
Provides:       bundled(golang(github.com/diamondburned/gotk4-adwaita/pkg)) = 0~20260808200908.d4aecaa0ff32
Provides:       bundled(golang(github.com/rivo/uniseg)) = 0.4.7
Provides:       bundled(golang(golang.org/x/sync)) = 0~20210220032951.036812b2e83c
Provides:       bundled(golang(github.com/hinshun/vt10x))

%description
bunk-gui is a GPU-rendered terminal built on the emulator core of the bunk
multiplexer. Each tab is a workspace for one coding agent. Tabs show which
agent runs, its repository and branch, and whether it is working or waiting
for you. It has splits, scrollback, mouse selection and clipboard support.

%install
install -Dpm0755 %{stagedir}/%{name} %{buildroot}%{_bindir}/%{name}
desktop-file-install --dir=%{buildroot}%{_datadir}/applications \
    %{srcdir}/packaging/%{appid}.desktop
install -Dpm0644 %{srcdir}/packaging/%{appid}.svg \
    %{buildroot}%{_datadir}/icons/hicolor/scalable/apps/%{appid}.svg
install -Dpm0644 %{srcdir}/packaging/%{appid}.metainfo.xml \
    %{buildroot}%{_metainfodir}/%{appid}.metainfo.xml
# %%license and %%doc take their files from the build directory.
cp -p %{srcdir}/LICENSE %{srcdir}/README.md .
cp -pr %{stagedir}/bundled-licenses .

%check
desktop-file-validate %{buildroot}%{_datadir}/applications/%{appid}.desktop
appstreamcli validate --no-net %{buildroot}%{_metainfodir}/%{appid}.metainfo.xml

%files
%license LICENSE
%license bundled-licenses
%doc README.md
%{_bindir}/%{name}
%{_datadir}/applications/%{appid}.desktop
%{_datadir}/icons/hicolor/scalable/apps/%{appid}.svg
%{_metainfodir}/%{appid}.metainfo.xml

%changelog
* Sun Oct 25 2026 Yauhen Shulitski <5493713+jsnjack@users.noreply.github.com> - 0.74.0-1
- First package of bunk-gui
