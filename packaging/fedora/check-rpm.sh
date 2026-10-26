#!/usr/bin/env bash
# check-rpm.sh RPM SOURCE_TARBALL - check a built bunk-gui RPM.
#
# Checks the license tag and files, the installed files, hardening flags,
# dependencies and the stamped version, then runs the packaged binary on the
# headless compositor and requires pixel-exact output. "make rpm" runs it
# after rpmbuild.
set -euo pipefail

rpm=$(realpath "${1:?usage: check-rpm.sh RPM SOURCE_TARBALL}")
tarball=$(realpath "${2:?usage: check-rpm.sh RPM SOURCE_TARBALL}")
top=$(git rev-parse --show-toplevel)
appid=io.github.jsnjack.BunkGui
failures=0
fail() { echo "FAIL: $*" >&2; failures=$((failures + 1)); }
pass() { echo "ok:   $*"; }

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
(cd "$work" && rpm2cpio "$rpm" | cpio -idm --quiet)

license=$(rpm -qp --qf '%{LICENSE}' "$rpm")
[[ $license == "MIT AND MPL-2.0 AND ISC AND BSD-3-Clause" ]] &&
	pass "License: $license" || fail "License tag is '$license'"

lic=$work/usr/share/licenses/bunk-gui
cmp -s "$lic/LICENSE" "$top/LICENSE" &&
	pass "bunk's own LICENSE shipped unchanged" || fail "LICENSE differs from the repository's"
cmp -s "$lic/bundled-licenses/github.com/hinshun/vt10x/LICENSE" "$top/internal/vt10x/LICENSE" &&
	pass "vt10x notice shipped" || fail "vt10x LICENSE missing or changed"

# bunk itself is vendored into cmd/bunk-gui; its LICENSE is not a bundled one.
[[ ! -e $lic/bundled-licenses/bunk ]] && pass "no bunk copy among bundled licenses" ||
	fail "bundled-licenses/bunk duplicates bunk's own LICENSE"

# Every vendored module must ship at least one license file under its path.
modules=$(tar -xzOf "$tarball" --wildcards '*/cmd/bunk-gui/vendor/modules.txt' |
	awk '$1 == "#" && $2 != "bunk" { print $2 }')
for m in $modules; do
	if compgen -G "$lic/bundled-licenses/$m/LICENSE*" >/dev/null ||
		compgen -G "$lic/bundled-licenses/$m/COPYING*" >/dev/null; then
		pass "license for $m"
	else
		fail "no license file shipped for $m"
	fi
done

for f in usr/bin/bunk-gui usr/share/applications/$appid.desktop \
	usr/share/icons/hicolor/scalable/apps/$appid.svg usr/share/metainfo/$appid.metainfo.xml; do
	[[ -s $work/$f ]] && pass "/$f" || fail "/$f missing"
done

bin=$work/usr/bin/bunk-gui
readelf -h "$bin" | grep -q 'DYN (Position-Independent' && pass "PIE" || fail "binary is not PIE"
readelf -d "$bin" | grep -q 'BIND_NOW' && pass "BIND_NOW" || fail "binary lacks BIND_NOW"
readelf -lW "$bin" | grep -q 'GNU_RELRO' && pass "RELRO" || fail "binary lacks RELRO"

requires=$(rpm -qp --requires "$rpm")
for r in 'libgtk-4.so.1()(64bit)' 'libadwaita-1.so.0()(64bit)' libglvnd-egl libglvnd-opengl; do
	grep -qxF "$r" <<<"$requires" && pass "Requires $r" || fail "missing Requires $r"
done

version=$(rpm -qp --qf '%{VERSION}' "$rpm")
[[ $("$bin" --version) == "bunk-gui version $version" ]] && pass "--version prints $version" ||
	fail "--version: $("$bin" --version 2>&1)"

out=$("$top/scripts/headless-gui.sh" "$bin" --verify --command 'exec sleep 30' --do '1s quit' 2>&1) ||
	fail "packaged binary exited with an error"
n=$(grep -c ' 0 of [0-9]* pixels differ' <<<"$out" || true)
[[ $n -eq 2 ]] && pass "packaged binary renders pixel-exact at 133%" ||
	fail "pixel check: expected 2 clean results, got $n"$'\n'"$out"

if ((failures)); then
	echo "check-rpm: $failures failure(s)" >&2
	exit 1
fi
echo "check-rpm: all checks passed"
