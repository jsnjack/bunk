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
printf "  Click this link -> ${OSC}8;;https://github.com/jsnjack/bunk${ST}bunk on GitHub${OSC}8;;${ST}\n"
expect "In supporting terminals, Ctrl+click or regular click should open the URL."
diagnose "If the text is visible but not clickable through bunk, inspect OSC 8 passthrough in the scanner/render drain path."

section "Bracketed Paste"
printf "  Enabling bracketed paste (${CSI}?2004h) then disabling (${CSI}?2004l)...\n"
printf "${CSI}?2004h"
expect "If your terminal exposes bracketed paste, pasted text should arrive wrapped in ESC[200~ ... ESC[201~ while enabled."
diagnose "If enabling this breaks later typing or the reset fails, inspect DECSET 2004 tracking and state queries."
pause_for_input "  Paste something now, then press Enter to continue. " 0.2
printf "${CSI}?2004l"
ok "Bracketed paste disabled"

section "OSC 133 Prompt Markers"
printf "  Emitting prompt/command markers now...\n"
printf "  Visible output: sample command under OSC 133 markers\n"
printf "${OSC}133;A${ST}${OSC}133;B${ST}"
printf "sample command under OSC 133 markers\n"
printf "${OSC}133;C${ST}${OSC}133;D;0${ST}"
expect "These markers are non-printing. In a terminal with shell integration, look for jump-to-prompt, prompt separators, or command timing tied to the emitted command line above."
diagnose "If the outer terminal loses prompt integration through bunk, inspect OSC 133 passthrough."

script_done
