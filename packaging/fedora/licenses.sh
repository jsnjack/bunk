#!/usr/bin/env bash
# licenses.sh DIR - copy the license files of the Go modules linked into
# bunk-gui to DIR/MODULE/PATH, for the RPM's %license. %license keeps only
# base names, and most of these files are called LICENSE, so each keeps its
# module path. A package's own license (gotk4's core, cairo/swizzle) is
# copied as well as its module's.
set -euo pipefail
shopt -s nocaseglob nullglob

dest=$(realpath -m "${1:?usage: licenses.sh DIR}")
top=$(git rev-parse --show-toplevel)

cd "$top/cmd/bunk-gui"
# bunk-gui's own module and bunk (replaced by ../..) are not bundled:
# bunk's LICENSE ships as bunk-gui's own.
go list -deps -f '{{with .Module}}{{if and (not .Main) (ne .Path "bunk")}}{{.Path}}|{{.Dir}}|{{$.Dir}}{{end}}{{end}}' . |
	sort -u |
	while IFS='|' read -r mod root dir; do
		# From the package's directory up to its module's root.
		while :; do
			for f in "$dir"/LICENSE* "$dir"/COPYING*; do
				install -Dpm0644 "$f" "$dest/$mod${dir#"$root"}/${f##*/}"
			done
			[[ $dir == "$root" ]] && break
			dir=${dir%/*}
		done
	done
install -Dpm0644 "$top/internal/vt10x/LICENSE" "$dest/github.com/hinshun/vt10x/LICENSE"
# Mermaid and KaTeX are embedded in the binary for the document panel
# (internal/present/assets).
install -Dpm0644 "$top/internal/present/assets/mermaid.LICENSE" "$dest/mermaid/LICENSE"
install -Dpm0644 "$top/internal/present/assets/katex/LICENSE" "$dest/katex/LICENSE"
