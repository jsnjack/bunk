#!/usr/bin/env bash
# screenshots.sh - render the README pictures into assets/.
#
# Runs TestGUIScreenshots (cmd/bunk-gui/screenshots_test.go and
# screenshots_features_test.go) on a private headless compositor
# (scripts/headless-gui.sh), so no window opens on the desktop. The test
# checks every shot; only a passing run replaces files. Then each PNG is
# shrunk with the tools that are installed:
#
#   1. pngquant, else ImageMagick: 256-colour palette (skipped with
#      SCREENSHOTS_LOSSLESS=1, which keeps every pixel)
#   2. oxipng, else optipng: lossless recompression
#
# The frames of the states loop become gui-states.webp, through ffmpeg
# with libwebp, else ImageMagick. ImageMagick also draws social-preview.png,
# the image GitHub shows for links to the repository (Settings, Social
# preview), from the icon and the hero shot.
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
# report prints a file's size and dimensions and counts it when it is over
# the budget.
report() {
	local size dims
	size=$(stat -c %s "$1")
	dims=$($magick identify -format '%wx%h\n' "$1" 2>/dev/null | head -1) || dims=?
	printf '%-24s %5d KB  %s\n' "$(basename "$1")" $((size / 1024)) "$dims"
	if ((size > max_kb * 1024)); then
		over=$((over + 1))
	fi
}

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
	report "$dst"
done

# The states loop: 1.6 s per frame, lossless, repeating.
frames=("$raw"/frames/states-*.png)
if ((${#frames[@]} > 0)); then
	webp=$out/gui-states.webp
	if have ffmpeg && ffmpeg -hide_banner -encoders 2>/dev/null | grep -q libwebp_anim; then
		ffmpeg -loglevel error -y -framerate 1/1.6 -i "$raw/frames/states-%d.png" \
			-c:v libwebp_anim -lossless 1 -compression_level 6 -loop 0 "$webp"
		report "$webp"
	elif [[ -n $magick ]]; then
		$magick -delay 160 -loop 0 "${frames[@]}" -define webp:lossless=true "$webp"
		report "$webp"
	else
		echo "screenshots: no ffmpeg with libwebp or ImageMagick: $(basename "$webp") not updated" >&2
	fi
fi

# The social preview: icon, name and tagline over the top of the hero.
if [[ -n $magick && -f $out/gui-hero.png ]]; then
	social=$out/social-preview.png
	# The icon is drawn at a high density for a sharp downscale; the text
	# needs the default density back, or its point sizes grow with it.
	$magick -size 1280x640 xc:'#16161a' \
		\( -background none -density 384 "$root/packaging/io.github.jsnjack.BunkGui.svg" -resize 104x104 \) \
		-geometry +72+64 -composite -density 72 \
		-font Noto-Sans-Bold -pointsize 72 -fill '#f4f4f6' -annotate +200+128 'bunk' \
		-font Noto-Sans-Regular -pointsize 30 -fill '#a9a9b4' -annotate +202+172 'A terminal for running coding agents side by side' \
		\( "$out/gui-hero.png" -resize 1136x \) -geometry +72+236 -composite \
		-crop 1280x640+0+0 +repage -strip -dither None -colors 256 "PNG8:$social"
	report "$social"
else
	echo "screenshots: no ImageMagick: social-preview.png not updated" >&2
fi

if ((over > 0)); then
	echo "screenshots: $over file(s) over ${max_kb} KB" >&2
fi
