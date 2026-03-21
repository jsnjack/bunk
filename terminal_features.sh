#!/usr/bin/env bash
# terminal_features.sh — visual + interactive test for advanced terminal features.
# Run inside any terminal (including bunk) to see what it supports.
#
# Usage: bash terminal_features.sh [--no-queries]
#   --no-queries   skip the escape-sequence query section (no raw-mode needed)

ESC=$'\033'
CSI="${ESC}["
OSC="${ESC}]"
DCS="${ESC}P"
ST="${ESC}\\"
R="${CSI}0m"   # reset

SKIP_QUERIES=0
[[ "${1}" == "--no-queries" ]] && SKIP_QUERIES=1

hr() { printf '\n%s\n' "$(printf '─%.0s' {1..72})"; }
section() { hr; printf "${CSI}1;36m▶ %s${R}\n" "$1"; }
ok()   { printf "  ${CSI}32m✔${R} %s\n" "$1"; }
info() { printf "  ${CSI}33m•${R} %s\n" "$1"; }
na()   { printf "  ${CSI}90m✘${R} %s ${CSI}90m(not rendered)${R}\n" "$1"; }

# ---------------------------------------------------------------------------
section "1. SGR Text Attributes"
# ---------------------------------------------------------------------------

printf "  %-22s %s\n" "Attribute" "Example"
printf "  %-22s %s\n" "---------" "-------"
printf "  %-22s ${CSI}1mBold text${R}\n"            "SGR 1  bold"
printf "  %-22s ${CSI}2mDim/faint text${R}\n"        "SGR 2  dim"
printf "  %-22s ${CSI}3mItalic text${R}\n"           "SGR 3  italic"
printf "  %-22s ${CSI}4mUnderline text${R}\n"        "SGR 4  underline (solid)"
printf "  %-22s ${CSI}4:1mUnderline text${R}\n"      "SGR 4:1 underline (solid)"
printf "  %-22s ${CSI}4:2mDouble underline${R}\n"    "SGR 4:2 double underline"
printf "  %-22s ${CSI}4:3mCurly underline${R}\n"     "SGR 4:3 curly/wavy underline"
printf "  %-22s ${CSI}4:4mDotted underline${R}\n"    "SGR 4:4 dotted underline"
printf "  %-22s ${CSI}4:5mDashed underline${R}\n"    "SGR 4:5 dashed underline"
printf "  %-22s ${CSI}5mBlink text${R}\n"            "SGR 5  blink"
printf "  %-22s ${CSI}7mReverse text${R}\n"          "SGR 7  reverse"
printf "  %-22s ${CSI}8m(invisible)${R} <-- should be blank\n" "SGR 8  invisible"
printf "  %-22s ${CSI}9mStrikethrough${R}\n"         "SGR 9  strikethrough"
printf "  %-22s ${CSI}53mOverline text${R}\n"        "SGR 53 overline"
printf "  %-22s ${CSI}1;3;4mBold+italic+under${R}\n" "SGR combined"

# ---------------------------------------------------------------------------
section "2. Colors — ANSI 8 + bright 16"
# ---------------------------------------------------------------------------

printf "  FG (30-37):  "
for c in 30 31 32 33 34 35 36 37; do
    printf "${CSI}${c}m█${R}"
done
printf "   ${CSI}1m"
for c in 30 31 32 33 34 35 36 37; do
    printf "${CSI}${c}m█${R}"
done
printf "${R}\n"

printf "  BG (40-47):  "
for c in 40 41 42 43 44 45 46 47; do
    printf "${CSI}${c}m  ${R}"
done
printf "\n"

printf "  Bright FG:   "
for c in 90 91 92 93 94 95 96 97; do
    printf "${CSI}${c}m█${R}"
done
printf "\n"

printf "  Bright BG:   "
for c in 100 101 102 103 104 105 106 107; do
    printf "${CSI}${c}m  ${R}"
done
printf "\n"

# ---------------------------------------------------------------------------
section "3. 256-Color Palette (38;5;N)"
# ---------------------------------------------------------------------------

printf "  System 0-15:  "
for i in {0..15}; do
    printf "${CSI}48;5;${i}m  ${R}"
done
printf "\n"

printf "  Color cube:   "
for i in {16..51}; do
    printf "${CSI}48;5;${i}m ${R}"
done
printf "\n"
printf "                "
for i in {52..87}; do
    printf "${CSI}48;5;${i}m ${R}"
done
printf "\n"
printf "                "
for i in {88..123}; do
    printf "${CSI}48;5;${i}m ${R}"
done
printf "\n"
printf "                "
for i in {124..159}; do
    printf "${CSI}48;5;${i}m ${R}"
done
printf "\n"
printf "                "
for i in {160..195}; do
    printf "${CSI}48;5;${i}m ${R}"
done
printf "\n"
printf "                "
for i in {196..231}; do
    printf "${CSI}48;5;${i}m ${R}"
done
printf "\n"

printf "  Greys 232-255:"
for i in {232..255}; do
    printf "${CSI}48;5;${i}m ${R}"
done
printf "\n"

# ---------------------------------------------------------------------------
section "4. True Color / 24-bit RGB (38;2;R;G;B)"
# ---------------------------------------------------------------------------

printf "  Red gradient:    "
for i in {0..71}; do
    r=$(( i * 255 / 71 ))
    printf "${CSI}48;2;${r};0;0m ${R}"
done
printf "\n"

printf "  Green gradient:  "
for i in {0..71}; do
    g=$(( i * 255 / 71 ))
    printf "${CSI}48;2;0;${g};0m ${R}"
done
printf "\n"

printf "  Blue gradient:   "
for i in {0..71}; do
    b=$(( i * 255 / 71 ))
    printf "${CSI}48;2;0;0;${b}m ${R}"
done
printf "\n"

printf "  Rainbow:         "
for i in {0..71}; do
    deg=$(( i * 360 / 72 ))
    h=$(( deg / 60 ))
    f=$(( (deg % 60) * 255 / 59 ))
    q=$(( 255 - f ))
    case $h in
        0) r=255; g=$f;  b=0   ;;
        1) r=$q;  g=255; b=0   ;;
        2) r=0;   g=255; b=$f  ;;
        3) r=0;   g=$q;  b=255 ;;
        4) r=$f;  g=0;   b=255 ;;
        *) r=255; g=0;   b=$q  ;;
    esac
    printf "\033[48;2;${r};${g};${b}m \033[0m"
done
printf "\n"

# ---------------------------------------------------------------------------
section "5. Colored Underlines (SGR 58)"
# ---------------------------------------------------------------------------

printf "  %-30s %s\n" "Sequence" "Example"
printf "  %-30s ${CSI}4;58;5;196mRed 256-color underline${R}\n"      "4 + 58;5;196 (red, 256)"
printf "  %-30s ${CSI}4;58;5;82mGreen 256-color underline${R}\n"     "4 + 58;5;82  (green, 256)"
printf "  %-30s ${CSI}4;58;5;39mBlue 256-color underline${R}\n"      "4 + 58;5;39  (blue, 256)"
printf "  %-30s ${CSI}4;58;2;255;80;0mOrange RGB underline${R}\n"    "4 + 58;2;255;80;0 (orange, RGB)"
printf "  %-30s ${CSI}4:3;58;2;255;0;128mCurly+colored underline${R}\n" "4:3 + 58;2;255;0;128 (curly+pink)"
info "All five lines should show text with a colored line beneath (color differs from text color)"

# ---------------------------------------------------------------------------
section "6. Cursor Shapes (DECSCUSR — CSI N SP q)"
# ---------------------------------------------------------------------------

printf "  Cycling through cursor shapes. Watch the cursor:\n\n"
shapes=(
    "1 blinking block (default)"
    "2 steady block"
    "3 blinking underline"
    "4 steady underline"
    "5 blinking bar (I-beam)"
    "6 steady bar (I-beam)"
)
for entry in "${shapes[@]}"; do
    n="${entry%% *}"
    label="${entry#* }"
    printf "  Shape %s — %-30s  " "$n" "$label"
    printf "${CSI}${n} q"   # set cursor shape
    sleep 0.3
    printf "(press Enter)"
    read -r
done
# Reset to default
printf "${CSI}0 q"
ok "Cursor reset to default (shape 0)"

# ---------------------------------------------------------------------------
section "7. OSC 8 — Hyperlinks"
# ---------------------------------------------------------------------------

printf "  Click this link → ${OSC}8;;https://github.com/jsnjack/bunk${ST}bunk on GitHub${OSC}8;;${ST}\n"
info "Should be clickable in terminals that support OSC 8 (kitty, WezTerm, foot, GNOME Terminal 3.32+)"

# ---------------------------------------------------------------------------
section "8. Unicode — Wide Characters and Emoji"
# ---------------------------------------------------------------------------

printf "  CJK wide chars:  |中文日本語한국어|\n"
printf "  Emoji (1-wide?): 😀 🎉 🦀 🔥 ✅ ❌\n"
printf "  Box drawing:     ┌─┬─┐  ╔═╦═╗  ├─┼─┤\n"
printf "                   │ │ │  ║ ║ ║  └─┴─┘\n"
printf "  Braille:         ⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏  (spinner frames)\n"
printf "  Powerline glyphs: \ue0b0\ue0b1\ue0b2\ue0b3 (needs Nerd Font)\n"
info "CJK/emoji each occupy 2 columns — columns after should still align"

# ---------------------------------------------------------------------------
section "9. Dim + True Color Combination"
# ---------------------------------------------------------------------------

printf "  ${CSI}2;38;2;100;200;255mDim + true-color text${R}  (should appear faded/blended, not full brightness)\n"
printf "  ${CSI}1;38;2;255;100;50mBold + true-color text${R} (should appear brighter/stronger)\n"
printf "  ${CSI}3;4:3;58;2;255;200;0mItalic + curly gold underline${R}\n"

# ---------------------------------------------------------------------------
section "10. Bracketed Paste Mode"
# ---------------------------------------------------------------------------

printf "  Enabling bracketed paste (${CSI}?2004h) then disabling (${CSI}?2004l)...\n"
printf "${CSI}?2004h"
info "Bracketed paste ON — if your terminal supports it, pasted text will be wrapped in ESC[200~ ... ESC[201~"
printf "  Paste something now and check if you see the bracket markers. (Enter to skip)\n"
read -r -t 5
printf "${CSI}?2004l"
ok "Bracketed paste disabled"

# ---------------------------------------------------------------------------
if [[ $SKIP_QUERIES -eq 1 ]]; then
    section "11. Terminal Queries (skipped — use without --no-queries)"
    hr
    printf "\nDone. Run without --no-queries to also test escape-sequence responses.\n\n"
    exit 0
fi

section "11. Terminal Queries (reads responses — raw mode)"
# ---------------------------------------------------------------------------

# Helper: put terminal in raw mode, send query, read response with timeout, restore
query_terminal() {
    local query="$1"
    local timeout="${2:-0.5}"
    local old_stty response
    old_stty=$(stty -g 2>/dev/null)
    stty -echo -icanon min 0 time 0 2>/dev/null
    printf '%s' "$query"
    sleep "$timeout"
    response=""
    while IFS= read -r -s -t 0.05 -d '' -n 1 ch 2>/dev/null; do
        response+="$ch"
        [[ ${#response} -gt 200 ]] && break
    done
    stty "$old_stty" 2>/dev/null
    # Print as escaped string
    printf '%s' "$response" | cat -v
}

# --- DA1 ---
printf "\n  ${CSI}1mDA1${R} (Primary Device Attributes — CSI c)\n"
printf "  Query:    ^[[c\n"
printf "  Response: "
r=$(query_terminal "${CSI}c")
printf '%s\n' "$r"
info "VT220 response: ^[?62;1;2;4;6;9;15;22c  |  xterm: ^[?63;1;..."

# --- DA2 ---
printf "\n  ${CSI}1mDA2${R} (Secondary Device Attributes — CSI > c)\n"
printf "  Query:    ^[[>c\n"
printf "  Response: "
r=$(query_terminal "${CSI}>c")
printf '%s\n' "$r"
info "Expected: ^[[>0;279;0c  (type 0 = VT100 class, version 279)"

# --- XTVERSION ---
printf "\n  ${CSI}1mXTVERSION${R} (CSI > 0 q)\n"
printf "  Query:    ^[[>0q\n"
printf "  Response: "
r=$(query_terminal "${CSI}>0q")
printf '%s\n' "$r"
info "Expected: DCS >|VTE(8203) ST  or  DCS >|xterm(NNN) ST  etc."

# --- CPR ---
printf "\n  ${CSI}1mCPR${R} (Cursor Position Report — CSI 6 n)\n"
printf "  Query:    ^[[6n\n"
printf "  Response: "
r=$(query_terminal "${CSI}6n")
printf '%s\n' "$r"
info "Expected: ^[[row;colR"

# --- XTGETTCAP Smulx ---
printf "\n  ${CSI}1mXTGETTCAP${R} Smulx (extended underline — DCS +q 536d756c78 ST)\n"
printf "  Query:    DCS +q 536d756c78 ST\n"
printf "  Response: "
r=$(query_terminal "${DCS}+q536d756c78${ST}")
printf '%s\n' "$r"
info "Found:     DCS 1+r 536d756c78=1b5b343a25703125646d ST"
info "Not found: DCS 0+r 536d756c78 ST"

# --- XTGETTCAP Setulc ---
printf "\n  ${CSI}1mXTGETTCAP${R} Setulc (underline color — DCS +q 536574756c63 ST)\n"
printf "  Query:    DCS +q 536574756c63 ST\n"
printf "  Response: "
r=$(query_terminal "${DCS}+q536574756c63${ST}")
printf '%s\n' "$r"
info "Found:     DCS 1+r 536574756c63=... ST"

# --- DECRQM 2026 (sync updates) ---
printf "\n  ${CSI}1mDECRQM${R} mode 2026 (synchronized updates — CSI ?2026\$p)\n"
printf "  Query:    ^[[?2026\$p\n"
printf "  Response: "
r=$(query_terminal "${CSI}?2026\$p")
printf '%s\n' "$r"
info "Expected: ^[[?2026;1\$y (set) or ^[[?2026;2\$y (not set)"

# --- DECRQM 2004 (bracketed paste) ---
printf "\n  ${CSI}1mDECRQM${R} mode 2004 (bracketed paste — CSI ?2004\$p)\n"
printf "  Query:    ^[[?2004\$p\n"
printf "  Response: "
r=$(query_terminal "${CSI}?2004\$p")
printf '%s\n' "$r"
info "Expected: ^[[?2004;2\$y (reset = not currently active)"

# --- OSC 10 (fg color) ---
printf "\n  ${CSI}1mOSC 10${R} (foreground color query — OSC 10;? ST)\n"
printf "  Query:    OSC 10;? ST\n"
printf "  Response: "
r=$(query_terminal "${OSC}10;?${ST}")
printf '%s\n' "$r"
info "Expected: OSC 10;rgb:xxxx/xxxx/xxxx ST"

# --- OSC 11 (bg color) ---
printf "\n  ${CSI}1mOSC 11${R} (background color query — OSC 11;? ST)\n"
printf "  Query:    OSC 11;? ST\n"
printf "  Response: "
r=$(query_terminal "${OSC}11;?${ST}")
printf '%s\n' "$r"
info "Expected: OSC 11;rgb:xxxx/xxxx/xxxx ST"

# ---------------------------------------------------------------------------
section "12. Kitty Keyboard Protocol"
# ---------------------------------------------------------------------------

printf "  Querying keyboard flags (CSI ?u)...\n"
printf "  Response: "
r=$(query_terminal "${CSI}?u")
printf '%s\n' "$r"
info "Expected: ^[[NNu where NN = current flags bitmask (0 = none enabled)"
info "To enable: ^[[>31u  To disable: ^[[<u"

# ---------------------------------------------------------------------------
hr
printf "\n${CSI}1;32mAll sections complete.${R}\n"
printf "Compare results across terminals to see which features each one supports.\n\n"
