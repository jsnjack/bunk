#!/usr/bin/env bash

set -u

ESC=$'\033'
CSI="${ESC}["
OSC="${ESC}]"
DCS="${ESC}P"
ST="${ESC}\\"
BEL=$'\007'
R="${CSI}0m"

# Save original stdout (the pane's PTY) to fd 3 so query functions can
# write escape sequences to the terminal even inside $(...) captures.
if [[ -t 1 ]]; then
    exec 3>&1
else
    exec 3>/dev/null
fi

hr() {
    printf '\n%s\n' "$(printf '─%.0s' {1..72})"
}

section() {
    hr
    printf "${CSI}1;36m▶ %s${R}\n" "$1"
}

info() {
    printf "  ${CSI}33m•${R} %s\n" "$1"
}

ok() {
    printf "  ${CSI}32m✔${R} %s\n" "$1"
}

warn() {
    printf "  ${CSI}31m!${R} %s\n" "$1"
}

expect() {
    printf "  Expect: %s\n" "$1"
}

diagnose() {
    printf "  Diagnose: %s\n" "$1"
}

note() {
    printf "  Note: %s\n" "$1"
}

escaped() {
    printf '%s' "$1" | cat -v
}

# pause_for_input PROMPT [AUTO_DELAY]: wait for Enter. The line typed (or
# pasted) before Enter is kept in PAUSE_INPUT.
PAUSE_INPUT=""
pause_for_input() {
    local prompt="$1"
    local auto_delay="${2:-0.2}"
    PAUSE_INPUT=""
    printf "%s" "$prompt"
    if [[ "${TERMINAL_FEATURES_AUTO:-0}" == "1" || ! -t 0 ]]; then
        sleep "${auto_delay}"
        printf "\n"
        return
    fi
    IFS= read -r PAUSE_INPUT
}

timed_pause() {
    local seconds="${1:-5}"
    if [[ "${TERMINAL_FEATURES_AUTO:-0}" == "1" || ! -t 0 ]]; then
        sleep 0.2
        return
    fi
    read -r -t "${seconds}" || true
}

require_query_tty() {
    if [[ ! -t 0 || ! -t 1 ]]; then
        printf 'This script requires an interactive TTY on stdin/stdout.\n' >&2
        printf 'Run it directly in a real terminal (for bunk, inside a pane attached to one).\n' >&2
        exit 1
    fi
}

# query_raw QUERY: write QUERY, then a DA1 request as a fence, and print the
# raw reply bytes that arrive before the DA1 reply. Terminals answer in
# order, so no fixed sleep is needed. Waiting for a byte gives up after
# TERMINAL_FEATURES_QUERY_TIMEOUT seconds (default 2).
query_raw() {
    local query="$1"
    local timeout="${TERMINAL_FEATURES_QUERY_TIMEOUT:-2}"
    local fence_re=$'\e''\[\?[0-9;]*c$'
    local old_stty response="" ch fences=1 rest="$1"

    # Each DA1 request inside QUERY adds one DA1 reply before the fence's.
    while [[ $rest == *"${CSI}c"* ]]; do
        fences=$((fences + 1))
        rest=${rest#*"${CSI}c"}
    done
    old_stty=$(stty -g 2>/dev/null) || return 1
    stty -echo -icanon min 1 time 0 2>/dev/null || return 1
    # Drop stale input, such as keys pressed before the query.
    while read -r -t 0; do IFS= read -r -n 1 -d '' ch; done
    printf '%s%s' "$query" "${CSI}c" >&3
    while IFS= read -r -t "$timeout" -n 1 -d '' ch; do
        response+="$ch"
        if [[ $ch == c && $response =~ $fence_re ]]; then
            fences=$((fences - 1))
            if ((fences == 0)); then
                response=${response%"${BASH_REMATCH[0]}"}
                break
            fi
        fi
        [[ ${#response} -gt 4096 ]] && break
    done
    stty "$old_stty" 2>/dev/null
    printf '%s' "$response"
}

# query_terminal QUERY: print the reply to QUERY in cat -v form.
query_terminal() {
    query_raw "$1" | cat -v
}

# query_terminal_alt QUERY: like query_terminal, on the alternate screen.
query_terminal_alt() {
    local response
    response=$(query_raw "${CSI}?1049h$1")
    printf '%s' "${CSI}?1049l" >&3
    printf '%s' "$response" | cat -v
}

script_done() {
    hr
    printf "${CSI}1;32mDone.${R}\n"
}
