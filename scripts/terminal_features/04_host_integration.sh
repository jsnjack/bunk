#!/usr/bin/env bash

set -u

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
. "${SCRIPT_DIR}/lib.sh"

section "Host Integration"
info "This script checks terminal-host integration features that depend on escape sequences reaching the outer terminal."
expect "Hyperlinks should be clickable, bracketed paste should toggle cleanly, and OSC 133 markers should be observable in terminals that support shell integration."
diagnose "If bunk swallows these features, inspect OSC forwarding and event-loop writes to the host terminal."

section "OSC 8 Hyperlinks"
printf "  Click this link -> %sbunk on GitHub%s\n" "${OSC}8;;https://github.com/jsnjack/bunk${ST}" "${OSC}8;;${ST}"
expect "In supporting terminals, Ctrl+click or regular click should open the URL."
diagnose "If the text is visible but not clickable through bunk, inspect OSC 8 passthrough in the scanner/render drain path."

section "Bracketed Paste"
printf "  Enabling bracketed paste (ESC[?2004h), then disabling it (ESC[?2004l) after the pause.\n"
printf "${CSI}?2004h"
expect "If your terminal exposes bracketed paste, pasted text should arrive wrapped in ESC[200~ ... ESC[201~ while enabled."
diagnose "If enabling this breaks later typing or the reset fails, inspect DECSET 2004 tracking and state queries."
pause_for_input "  Paste something now, then press Enter to continue. " 0.2
printf "${CSI}?2004l"
printf "  Received: %s\n" "$(escaped "$PAUSE_INPUT")"
expect "The received line starts with ^[[200~ and ends with ^[[201~ around the pasted text."
ok "Bracketed paste disabled"

section "OSC 7 Working Directory"
printf '%s' "${OSC}7;file://${HOSTNAME:-localhost}${PWD}${ST}"
printf "  Reported directory: %s\n" "$PWD"
expect "Terminals with shell integration open new tabs or splits in this directory."
diagnose "If new tabs start elsewhere through bunk, inspect OSC 7 passthrough."

section "OSC 52 Clipboard"
printf '%s' "${OSC}52;c;$(printf '%s' 'bunk OSC 52 test' | base64)${ST}"
printf "  Copied to the clipboard: bunk OSC 52 test\n"
expect "Pasting now inserts: bunk OSC 52 test"
diagnose "If the clipboard is unchanged, inspect OSC 52 passthrough and bunk's native clipboard mirror."

section "OSC 133 Prompt Markers"
printf "  Emitting prompt/command markers now...\n"
printf "  Visible output: sample command under OSC 133 markers\n"
printf '%s' "${OSC}133;A${ST}${OSC}133;B${ST}"
printf "sample command under OSC 133 markers\n"
printf '%s' "${OSC}133;C${ST}${OSC}133;D;0${ST}"
expect "These markers are non-printing. In a terminal with shell integration, look for jump-to-prompt, prompt separators, or command timing tied to the emitted command line above."
diagnose "If the outer terminal loses prompt integration through bunk, inspect OSC 133 passthrough."

script_done
