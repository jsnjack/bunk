#!/usr/bin/env bash
# osc_smoke.sh -- bunk-specific OSC/query smoke tests.
#
# Run this inside a bunk pane attached to a real terminal:
#   bash scripts/osc_smoke.sh
#
# What it checks:
#   - same-chunk alt-screen + OSC 10/11/12 set/query ordering
#   - OSC 110/111/112 reset semantics against the pane's baseline colours
#   - normal-mode OSC 10/11/12 suppression
#   - same-chunk DECRQM state updates
#   - manual OSC 133 prompt-marker forwarding

set -u

ESC=$'\033'
CSI="${ESC}["
OSC="${ESC}]"
ST="${ESC}\\"
BEL=$'\007'

PASS=0
FAIL=0
WARN=0
TTY_OLD=""

restore_tty() {
    if [[ -n "${TTY_OLD}" ]]; then
        stty "${TTY_OLD}" 2>/dev/null || true
        TTY_OLD=""
    fi
}

trap restore_tty EXIT INT TERM

say()  { printf '%s\n' "$*"; }
ok()   { PASS=$((PASS + 1)); printf 'PASS  %s\n' "$*"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL  %s\n' "$*"; }
warn() { WARN=$((WARN + 1)); printf 'WARN  %s\n' "$*"; }

escaped() {
    printf '%s' "$1" | cat -v
}

drain_input() {
    local ch
    while IFS= read -r -s -t 0 -n 1 ch 2>/dev/null; do
        :
    done
}

query_tty() {
    local payload="$1"
    local timeout="${2:-0.35}"
    local response="" ch

    TTY_OLD=$(stty -g 2>/dev/null) || return 1
    stty -echo -icanon min 0 time 0 2>/dev/null || return 1
    drain_input
    printf '%s' "$payload"
    sleep "$timeout"
    while IFS= read -r -s -t 0.05 -n 1 ch 2>/dev/null; do
        response+="$ch"
        [[ ${#response} -gt 400 ]] && break
    done
    restore_tty
    printf '%s' "$response"
}

assert_eq() {
    local name="$1"
    local got="$2"
    local want="$3"
    if [[ "$got" == "$want" ]]; then
        ok "$name"
    else
        fail "$name"
        say "      got : $(escaped "$got")"
        say "      want: $(escaped "$want")"
    fi
}

assert_empty() {
    local name="$1"
    local got="$2"
    if [[ -z "$got" ]]; then
        ok "$name"
    else
        fail "$name"
        say "      got : $(escaped "$got")"
        say "      want: <empty>"
    fi
}

require_tty() {
    if [[ ! -t 0 || ! -t 1 ]]; then
        say "This script requires an interactive TTY on stdin/stdout."
        say "Run it directly inside a bunk pane attached to a real terminal."
        exit 1
    fi
}

main() {
    require_tty

    say "bunk OSC smoke test"
    say

    local baseline10 baseline11 baseline12
    baseline10=$(query_tty "${CSI}?1049h${OSC}10;?${ST}${CSI}?1049l")
    baseline11=$(query_tty "${CSI}?1049h${OSC}11;?${ST}${CSI}?1049l")
    baseline12=$(query_tty "${CSI}?1049h${OSC}12;?${ST}${CSI}?1049l")

    say "Baseline responses:"
    say "  OSC 10: $(escaped "$baseline10")"
    say "  OSC 11: $(escaped "$baseline11")"
    say "  OSC 12: $(escaped "$baseline12")"
    say

    local got

    got=$(query_tty "${CSI}?1049h${OSC}10;rgb:1111/2222/3333${ST}${OSC}10;?${ST}${CSI}?1049l")
    assert_eq "same-chunk OSC 10 set/query in alt-screen" "$got" "${OSC}10;rgb:1111/2222/3333${ST}"

    got=$(query_tty "${CSI}?1049h${OSC}11;rgb:4444/5555/6666${ST}${OSC}11;?${ST}${CSI}?1049l")
    assert_eq "same-chunk OSC 11 set/query in alt-screen" "$got" "${OSC}11;rgb:4444/5555/6666${ST}"

    got=$(query_tty "${CSI}?1049h${OSC}12;rgb:7777/8888/9999${ST}${OSC}12;?${ST}${CSI}?1049l")
    assert_eq "same-chunk OSC 12 set/query in alt-screen" "$got" "${OSC}12;rgb:7777/8888/9999${ST}"

    got=$(query_tty "${CSI}?1049h${OSC}10;rgb:1111/2222/3333${ST}${OSC}110${BEL}${OSC}10;?${ST}${CSI}?1049l")
    assert_eq "OSC 110 reset restores baseline fg response" "$got" "$baseline10"

    got=$(query_tty "${CSI}?1049h${OSC}11;rgb:4444/5555/6666${ST}${OSC}111${BEL}${OSC}11;?${ST}${CSI}?1049l")
    assert_eq "OSC 111 reset restores baseline bg response" "$got" "$baseline11"

    got=$(query_tty "${CSI}?1049h${OSC}12;rgb:7777/8888/9999${ST}${OSC}112${BEL}${OSC}12;?${ST}${CSI}?1049l")
    assert_eq "OSC 112 reset restores baseline cursor response" "$got" "$baseline12"

    got=$(query_tty "${OSC}10;?${ST}")
    assert_empty "normal-mode OSC 10 suppressed" "$got"

    got=$(query_tty "${OSC}11;?${ST}")
    assert_empty "normal-mode OSC 11 suppressed" "$got"

    got=$(query_tty "${OSC}12;?${ST}")
    assert_empty "normal-mode OSC 12 suppressed" "$got"

    got=$(query_tty "${CSI}?2004h${CSI}?2004\$p${CSI}?2004l${CSI}?2004\$p")
    assert_eq "same-chunk DECRQM state updates" "$got" "${CSI}?2004;1\$y${CSI}?2004;2\$y"

    say
    say "Manual:"
    printf '%s' "${OSC}133;A${ST}${OSC}133;B${ST}sample command under OSC 133 markers
${OSC}133;C${ST}${OSC}133;D;0${ST}"
    warn "observe the outer terminal for jump-to-prompt or shell-integration behavior from the OSC 133 markers above"

    say
    say "Summary: PASS=${PASS} FAIL=${FAIL} WARN=${WARN}"
    [[ ${FAIL} -eq 0 ]]
}

main "$@"
