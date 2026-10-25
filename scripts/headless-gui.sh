#!/usr/bin/env bash
# headless-gui.sh CMD [ARGS...] - run a GUI command on a private headless
# GNOME compositor, so tests never open windows on the real desktop.
#
# The compositor is mutter, as in the desktop session, rendering on the GPU
# with one virtual monitor. It gets its own D-Bus session bus, config
# directory and clipboard, so tests cannot touch the user's clipboard or
# settings. Font and scaling settings are copied from the user's session.
#
#   HEADLESS_MODE   virtual monitor mode, default 2560x1600@120
#   HEADLESS_SCALE  monitor scale, default 1.3333333730697632 (133%)
#   HEADLESS_LOG    file for the compositor log, default: discarded
set -euo pipefail

if [[ ${1:-} != --inner ]]; then
	[[ $# -gt 0 ]] || { echo "usage: headless-gui.sh CMD [ARGS...]" >&2; exit 2; }
	dir=$(mktemp -d -t bunk-headless.XXXXXX)
	mkdir -p "$dir/config/glib-2.0/settings"
	# Copy the settings that change glyph rendering into a private keyfile.
	{
		echo "[org/gnome/desktop/interface]"
		for k in font-antialiasing font-hinting font-rgba-order text-scaling-factor \
			color-scheme monospace-font-name font-name cursor-size; do
			echo "$k=$(gsettings get org.gnome.desktop.interface "$k")"
		done
	} >"$dir/config/glib-2.0/settings/keyfile"
	[[ -d $HOME/.config/gtk-4.0 ]] && ln -s "$HOME/.config/gtk-4.0" "$dir/config/gtk-4.0"
	[[ -d $HOME/.config/fontconfig ]] && ln -s "$HOME/.config/fontconfig" "$dir/config/fontconfig"
	# A bus without service directories: nothing can be activated on it, so
	# no portal, keyring or gvfs daemon starts. GTK and libadwaita then read
	# the keyfile above instead of the settings portal.
	cat >"$dir/bus.conf" <<-'EOF'
		<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
		 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
		<busconfig>
		  <type>session</type>
		  <keep_umask/>
		  <listen>unix:tmpdir=/tmp</listen>
		  <auth>EXTERNAL</auth>
		  <policy context="default">
		    <allow send_destination="*" eavesdrop="true"/>
		    <allow eavesdrop="true"/>
		    <allow own="*"/>
		  </policy>
		</busconfig>
	EOF
	export HEADLESS_DIR=$dir XDG_CONFIG_HOME=$dir/config GSETTINGS_BACKEND=keyfile
	export GIO_USE_VFS=local
	unset DISPLAY WAYLAND_SOCKET
	exec dbus-run-session --config-file="$dir/bus.conf" -- "$0" --inner "$@"
fi
shift

display=bunk-headless-$$
log=${HEADLESS_LOG:-/dev/null}
mutter --headless --wayland --no-x11 --wayland-display "$display" \
	--virtual-monitor "${HEADLESS_MODE:-2560x1600@120}" >>"$log" 2>&1 &
mutter_pid=$!
cleanup() {
	kill "$mutter_pid" 2>/dev/null || true
	wait "$mutter_pid" 2>/dev/null || true
	rm -rf "$HEADLESS_DIR"
}
trap cleanup EXIT

for _ in $(seq 100); do
	[[ -S $XDG_RUNTIME_DIR/$display ]] && gdctl show >/dev/null 2>&1 && break
	kill -0 "$mutter_pid" 2>/dev/null || { echo "headless-gui: mutter exited" >&2; exit 1; }
	sleep 0.1
done
gdctl set --logical-monitor --primary --monitor Meta-0 \
	--scale "${HEADLESS_SCALE:-1.3333333730697632}" >/dev/null

# BUNK_HEADLESS tells window-opening tests that they may run.
export WAYLAND_DISPLAY=$display GDK_BACKEND=wayland ADW_DISABLE_PORTAL=1 \
	GTK_A11Y=none NO_AT_BRIDGE=1 BUNK_HEADLESS=1
"$@"
