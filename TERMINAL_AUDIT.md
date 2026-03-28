# Bunk Terminal Feature Audit

Date: 2026-03-12 (updated 2026-03-21)

## Legend
- **OK** — fully handled
- **PARTIAL** — works but incomplete
- **MISSING** — not implemented, will break or degrade apps that use it
- **N/A** — not feasible in a multiplexer (inherent limitation)

---

## 1. SGR Text Attributes

| SGR Code | Feature | Status | Impact |
|----------|---------|--------|--------|
| 0 | Reset | OK | |
| 1 | Bold | OK | |
| 2 | Dim/Faint | OK | vt10x vendored; AttrDim bit added. RGB-colour terminals ignore ti.Dim, so bunk blends FG 50% toward BG itself |
| 3 | Italic | OK | |
| 4 | Underline | OK | |
| 4:0-4:5 | Underline styles (curly/double/dotted/dashed) | OK | CSI sub-param parser fixed; all styles stored in Glyph.Mode bits and mapped to tcell.UnderlineStyleXxx |
| 5-6 | Blink | OK | |
| 7 | Reverse | **FIXED** | Was broken for default-color cells — vtColor mapped both DefaultFG and DefaultBG to the positional `def` param, undoing vt10x's FG/BG swap. Claude/Copilot cursor (reverse-video space) was invisible |
| 8 | Hidden/Invisible | OK | AttrInvisible bit added; rendered as space character |
| 9 | Strikethrough | OK | AttrStrikethrough bit added; tcell StrikeThrough(true) applied |
| 21 | Double underline | **MISSING** | Rare, some rich-text TUIs |
| 53 | Overline | **MISSING** | Parsed and stored (AttrOverline bit); reflow-correct; not rendered — tcell has no overline attribute |
| 58;5;N | Colored underline (256) | OK | attrHasULColor flag + UL Color field; vtColor mapping; tcell style.Underline(color) |
| 58;2;R;G;B | Colored underline (RGB) | OK | Same |
| 30-37, 90-97 | ANSI FG colors | OK | |
| 40-47, 100-107 | ANSI BG colors | OK | |
| 38;5;N / 48;5;N | 256 colors | OK | |
| 38;2;R;G;B / 48;2;R;G;B | True color (24-bit) | OK | |

Root cause (resolved): vt10x vendored to `internal/vt10x`; SGR 2/8/9 attr bits added and wired.

Apps affected: `git diff`, `ls --color`, neovim with LSP, glow, bat, delta, lazygit

---

## 2. OSC Sequences

| OSC | Feature | Status | Notes |
|-----|---------|--------|-------|
| 0/1/2 | Window title | OK | vt10x handles; bunk supplements with process/cwd |
| 4 | Set palette color | OK | vt10x handles |
| 7 | CWD notification | OK | Forwarded to host |
| 8 | Hyperlinks | OK | Forwarded to host |
| 10/11/12 | Query fg/bg/cursor color | **PARTIAL** | Queries are answered in-stream from bunk's current dynamic colour state. Replies are gated to alt-screen mode to avoid leaking into normal-mode apps. In `theme="terminal"` mode bunk now probes the outer terminal's OSC 10/11/12 defaults at startup and uses them when available; replies are still suppressed if the host terminal does not answer or the cursor colour is genuinely unknown |
| 52 | Clipboard | OK | Forwarded to host |
| 104 | Reset palette color | OK | vt10x handles |
| 110/111/112 | Reset fg/bg/cursor color | **PARTIAL** | Clears bunk's dynamic fg/bg/cursor overrides. Cursor-colour state is tracked for query/reset semantics, but bunk does not visibly render a separate cursor colour |
| 133 | Shell integration/prompt marking | OK | Forwarded to host so semantic prompt integration and jump-to-prompt can work when the outer terminal supports it |

Highest impact: Dynamic colour queries/resets are implemented and `theme="terminal"` now probes host defaults at startup, but compliance is still partial when the outer terminal does not support OSC 10/11/12 or does not expose a distinct cursor colour.

---

## 3. DEC Private Modes (DECSET/DECRST)

| Mode | Feature | Status | Notes |
|------|---------|--------|-------|
| 1 | DECCKM (cursor keys app mode) | OK | |
| 7 | DECAWM (auto-wrap) | OK | |
| 12 | Cursor blink | **MISSING** | vt10x logs "not implemented" |
| 25 | DECTCEM (cursor visible) | OK | |
| 47/1047 | Alt screen | OK | |
| 1000-1003 | Mouse modes | OK | |
| 1004 | Focus events | OK | |
| 1006 | SGR mouse | OK | |
| 1049 | Alt screen + save cursor | OK | |
| 2004 | Bracketed paste | OK | |
| 2026 | Synchronized updates | OK | |
| 2027 | Grapheme clustering | **MISSING** | Multi-codepoint emoji rendering |

---

## 4. Terminal Capability Queries/Responses

| Query | Feature | Status | Notes |
|-------|---------|--------|-------|
| DA1 (CSI c) | Primary device attributes | OK | Responds as VT220 |
| DA2 (CSI > c) | Secondary device attributes | OK | Responds as xterm 279 |
| DA3 (CSI = c) | Tertiary device attributes | **MISSING** | Rare, low impact |
| CPR (CSI 6 n) | Cursor position report | OK | |
| DSR (CSI 5 n) | Device status report | OK | |
| DECRQM (CSI ? Ps $ p) | Request mode | OK | Responds for modes 2026, 2004, 1004, 1049 |
| XTVERSION (CSI > 0 q) | Terminal version | OK | Responds with DCS >|VTE(8203) ST; VTE_VERSION=8203 set in pane env; used by Claude Code, Neovim, WezTerm for feature detection |
| XTGETTCAP (DCS + q) | Terminfo capability query | OK | Responds to Smulx/Setulc/Su (found with hex-encoded value); all others get "not found"; eliminates startup latency in apps that query capabilities |
| DECRQSS | Request setting | **MISSING** | Low impact |

Highest impact: XTVERSION (feature detection by newer apps).

---

## 5. Key Encoding

| Feature | Status | Notes |
|---------|--------|-------|
| Basic ASCII keys | OK | |
| Ctrl+letter | OK | |
| Alt+key (ESC prefix) | OK | |
| Arrow keys | OK | |
| Shift+Tab (BackTab) | **FIXED** | Was sending `\x1b[9;3u` (Alt+Tab) in kitty mode; now correctly `\x1b[9;2u` |
| Kitty keyboard protocol | **FIXED** | Push/pop/query stack; CSI u encoding for Enter, Tab, Backspace, Ctrl+letter. Fixed: stale stack after non-alt-screen KKP app exits without `\x1b[<u` — cleared by `trackFgProcess` on PGID change |
| F1–F12 | OK | Any key bound to a bunk action (default: F1=split, F12=zoom) is consumed and not forwarded; this is config-dependent |
| F13-F24 | OK | Shift+F1-F12; handled both via `KeyF13`–`KeyF24` and via `KeyF1`+`ModShift` modifier path |
| Home/End/PgUp/PgDn/Ins/Del | OK | All modifiers forwarded as `\x1b[<code>;<mod>~` / `\x1b[1;<mod>H/F`; Shift+PgUp/PgDn consumed by default for scrollback (config-dependent) |
| Modified arrows (Ctrl+Up etc) | OK | Forwarded as `\x1b[1;<mod>A/B/C/D`; Alt+arrows consumed by default for pane nav (config-dependent) |
| Modified Home/End/etc | OK | Ctrl+Home, Shift+End, Ctrl+Delete etc. forwarded with xterm modifier parameter |
| Keypad keys | **MISSING** | Numpad Enter, keypad digits in app keypad mode |

> **Note:** Any key bound to a bunk action in the user's config is intercepted and not forwarded to the PTY. Default consumed keys: F1 (split), Alt+F1 (split-context), F12 (zoom), Alt+arrows (pane nav), Shift+PgUp/PgDn (scrollback), Ctrl+C (copy/forward), Ctrl+V (paste), Ctrl+Q (quit), Ctrl+F (search), Ctrl+N (search-next). All of these are user-remappable.

Highest impact: SGR 58 (underline colour) for neovim LSP diagnostics colour-coding.

---

## 6. Graphics Protocols

| Feature | Status | Notes |
|---------|--------|-------|
| Sixel | **MISSING** | Used by chafa, lsix |
| Kitty graphics | **MISSING** | Used by kitty icat, ranger |
| iTerm2 inline images | **MISSING** | Used by imgcat, viu |

N/A in practice for cell-based multiplexer without passthrough support.

---

## 7. Unicode / Character Width

| Feature | Status | Notes |
|---------|--------|-------|
| UTF-8 | OK | Boundary detection prevents split-rune corruption |
| CJK double-width | **FIXED** | displayCol tracking in renderPane decouples vt10x column from screen column |
| Combining characters | **PARTIAL** | vt10x stores one rune per cell |
| Emoji (multi-codepoint) | **MISSING** | No grapheme clustering (mode 2027) |

---

## 8. Cursor Style (DECSCUSR)

| Feature | Status | Notes |
|---------|--------|-------|
| \x1b[N q forwarding | OK | Scans PTY output and forwards to host |
| Reset on exit | OK | Resets to default on shutdown |

---

## Priority Implementation Plan

### Tier 1 — High impact, affects common apps daily

All high-impact items have been implemented. Remaining gaps are low-priority.

### Tier 2 — Medium impact, cosmetic or edge-case

1. **SGR 21 double underline** — stored as underline style 2 but tcell doesn't distinguish it visually
2. **SGR 53 overline display** — parsed/stored; blocked on tcell adding AttrOverline (upstream request needed)

### Tier 3 — Nice to have

3. Grapheme clustering (mode 2027) — wide-char clusters
4. Graphics protocol passthrough (Sixel / kitty)
5. Keypad keys (numpad in app keypad mode)
