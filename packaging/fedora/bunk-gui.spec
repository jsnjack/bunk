# bunk-gui: GTK4 terminal built on bunk's emulator core.
#
# Build with "make rpm" from the repository root. It runs sources.sh, which
# packs the working tree with vendor/ for both Go modules, so %build needs no
# network (mock and COPR included). Pass the version with
# --define "bunk_version X.Y.Z"; the Makefile takes it from monova.

%{!?bunk_version:%global bunk_version 0.0.0}
%global appid io.github.jsnjack.BunkGui
# cgo reads its own object files as ELF; LTO bytecode objects break that.
%global _lto_cflags %{nil}
# Fedora's hardened link flags force PIE, so test binaries need it too.
%global goenv %{expand:
export GOTOOLCHAIN=local GOPROXY=off GOFLAGS="-mod=vendor -buildmode=pie"
export CGO_CFLAGS="%{build_cflags}"
export CGO_LDFLAGS="%{build_ldflags}"
}

Name:           bunk-gui
Version:        %{bunk_version}
Release:        1%{?snapinfo:.%{snapinfo}}%{?dist}
Summary:        Terminal for running coding agents side by side

# bunk, vt10x, creack/pty, rivo/uniseg: MIT; gotk4, gotk4-adwaita: MPL-2.0;
# gotk4/core: ISC; gotk4/cairo/swizzle, x/sync: BSD-3-Clause.
License:        MIT AND MPL-2.0 AND ISC AND BSD-3-Clause
URL:            https://github.com/jsnjack/bunk
Source0:        %{name}-%{version}.tar.gz

ExclusiveArch:  %{golang_arches}

BuildRequires:  golang >= 1.26
BuildRequires:  gcc
BuildRequires:  pkgconfig(gtk4)
BuildRequires:  pkgconfig(libadwaita-1)
BuildRequires:  pkgconfig(pangocairo)
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

%prep
%autosetup -p1
# %%license keeps only base names, and most bundled files are called LICENSE,
# so stage them under their module paths.
mkdir bundled-licenses
(cd cmd/bunk-gui/vendor &&
    find . -path ./bunk -prune -o -type f \( -iname 'LICENSE*' -o -iname 'COPYING*' \) \
        -exec cp --parents -t ../../../bundled-licenses {} +)
install -Dpm0644 internal/vt10x/LICENSE \
    bundled-licenses/github.com/hinshun/vt10x/LICENSE

%build
%{goenv}
cd cmd/bunk-gui
go build -ldflags "-X main.Version=%{version} -linkmode=external -extldflags '%{build_ldflags}'" \
    -o %{name} .

%install
install -Dpm0755 cmd/bunk-gui/%{name} %{buildroot}%{_bindir}/%{name}
desktop-file-install --dir=%{buildroot}%{_datadir}/applications \
    packaging/%{appid}.desktop
install -Dpm0644 packaging/%{appid}.svg \
    %{buildroot}%{_datadir}/icons/hicolor/scalable/apps/%{appid}.svg
install -Dpm0644 packaging/%{appid}.metainfo.xml \
    %{buildroot}%{_metainfodir}/%{appid}.metainfo.xml

%check
desktop-file-validate %{buildroot}%{_datadir}/applications/%{appid}.desktop
appstreamcli validate --no-net %{buildroot}%{_metainfodir}/%{appid}.metainfo.xml
%{goenv}
# The shared core, then the GUI tests that need no compositor; -short skips
# the window tests, which "make gui-test" runs on a headless mutter.
go test -count=1 ./internal/...
(cd cmd/bunk-gui && go test -short -count=1 ./...)

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
