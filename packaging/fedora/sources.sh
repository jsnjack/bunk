#!/usr/bin/env bash
# sources.sh VERSION OUTDIR - write OUTDIR/bunk-gui-VERSION.tar.gz for rpmbuild.
#
# The tarball holds the working tree (tracked and untracked files that git does
# not ignore) plus vendor/ for the root module and cmd/bunk-gui, so the RPM
# builds without network access. Run from anywhere inside the repository.
set -euo pipefail

version=${1:?usage: sources.sh VERSION OUTDIR}
outdir=$(realpath -m "${2:?usage: sources.sh VERSION OUTDIR}")
name=bunk-gui-$version
top=$(git rev-parse --show-toplevel)

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir "$work/$name"

# .claude holds agent worktrees, which are full copies of the repository.
git -C "$top" ls-files -z -c -o --exclude-standard -- . ':!:.claude' |
	tar -C "$top" --null --ignore-failed-read -T - -cf - |
	tar -C "$work/$name" -xf -

(cd "$work/$name" && go mod vendor)
(cd "$work/$name/cmd/bunk-gui" && go mod vendor)

mkdir -p "$outdir"
tar -C "$work" --sort=name --owner=0 --group=0 --numeric-owner \
	-czf "$outdir/$name.tar.gz" "$name"
echo "$outdir/$name.tar.gz"
