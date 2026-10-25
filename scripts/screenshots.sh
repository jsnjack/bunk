#!/usr/bin/env bash
# screenshots.sh - render the README screenshots of bunk-gui into assets/.
#
# Runs TestGUIScreenshots (cmd/bunk-gui/screenshots_test.go) on a private
# headless compositor (scripts/headless-gui.sh), so no window opens on the
# desktop. The test checks every shot; only a passing run replaces files.
# Then each PNG is shrunk with the tools that are installed:
#
#   1. pngquant, else ImageMagick: 256-colour palette (skipped with
#      SCREENSHOTS_LOSSLESS=1, which keeps every pixel)
#   2. oxipng, else optipng: lossless recompression
#
#   SCREENSHOTS_DIR       output directory, default assets/
#   SCREENSHOTS_LOSSLESS  1: no palette reduction
#   SCREENSHOTS_MAX_KB    size budget per file, default 300; larger files warn
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
out=${SCREENSHOTS_DIR:-$root/assets}
max_kb=${SCREENSHOTS_MAX_KB:-300}
mkdir -p "$out"
raw=$(mktemp -d -t bunk-screenshots.XXXXXX)
trap 'rm -rf "$raw"' EXIT

echo "==> rendering bunk-gui screenshots (headless)"
(cd "$root/cmd/bunk-gui" &&
	BUNK_SCREENSHOTS=$raw "$root/scripts/headless-gui.sh" \
		go test -run '^TestGUIScreenshots$' -count=1 .)

shopt -s nullglob
shots=("$raw"/*.png)
if [[ ${#shots[@]} -eq 0 ]]; then
	echo "screenshots: the test wrote no PNGs (renamed or skipped?)" >&2
	exit 1
fi

have() { command -v "$1" >/dev/null 2>&1; }
magick=
if have magick; then magick=magick; elif have convert; then magick=convert; fi

over=0
for src in "${shots[@]}"; do
	dst=$out/$(basename "$src")
	tmp=$raw/opt.png
	cp "$src" "$tmp"
	if [[ ${SCREENSHOTS_LOSSLESS:-0} != 1 ]]; then
		if have pngquant; then
			pngquant --quality=85-100 --speed 1 --strip --force --output "$tmp" "$src"
		elif [[ -n $magick ]]; then
			$magick "$src" -strip -dither None -colors 256 "PNG8:$tmp"
		fi
	fi
	if have oxipng; then
		oxipng -q -o 4 --strip safe "$tmp"
	elif have optipng; then
		optipng -quiet -o5 "$tmp"
	elif [[ -n $magick && ${SCREENSHOTS_LOSSLESS:-0} == 1 ]]; then
		$magick "$tmp" -strip -define png:compression-level=9 "$tmp"
	fi
	mv "$tmp" "$dst"
	size=$(stat -c %s "$dst")
	# Width and height from the PNG header.
	dims=$(od -An -j16 -N8 -tu1 "$dst" |
		awk '{printf "%dx%d", (($1*256+$2)*256+$3)*256+$4, (($5*256+$6)*256+$7)*256+$8}')
	printf '%-24s %5d KB  %s\n' "$(basename "$dst")" $((size / 1024)) "$dims"
	if ((size > max_kb * 1024)); then
		over=$((over + 1))
	fi
done
if ((over > 0)); then
	echo "screenshots: $over file(s) over ${max_kb} KB" >&2
fi
