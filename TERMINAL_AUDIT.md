# Bunk Terminal Feature Audit

Date: 2026-03-12

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
| 2 | Dim/Faint | **MISSING** | `git diff` dim lines, `ls --color` dim permissions, many TUIs use for disabled state |
| 3 | Italic | OK | |
| 4 | Underline | OK | |
| 4:1-4:5 | Curly/dotted/dashed underline | **MISSING** | Neovim diagnostics (curly underline for errors) |
| 5-6 | Blink | OK | |
| 7 | Reverse | **FIXED** | Was broken for default-color cells — vtColor mapped both DefaultFG and DefaultBG to the positional `def` param, undoing vt10x's FG/BG swap. Claude/Copilot cursor (reverse-video space) was invisible |
| 8 | Hidden/Invisible | **MISSING** | Password prompts in some TUIs |
| 9 | Strikethrough | **MISSING** | Markdown renderers (glow, bat), task managers (taskwarrior) |
| 21 | Double underline | **MISSING** | Rare, some rich-text TUIs |
| 53 | Overline | **MISSING** | Rare |
| 58;5;N | Colored underline (256) | **MISSING** | Neovim LSP diagnostics, helix |
| 58;2;R;G;B | Colored underline (RGB) | **MISSING** | Same |
| 30-37, 90-97 | ANSI FG colors | OK | |
| 40-47, 100-107 | ANSI BG colors | OK | |
| 38;5;N / 48;5;N | 256 colors | OK | |
| 38;2;R;G;B / 48;2;R;G;B | True color (24-bit) | OK | |

Root cause: vt10x's `setAttr()` has no cases for SGR 2, 8, or 9. The `Glyph.Mode` bitmask has no bits allocated for them. Fixing requires patching vt10x or adding a parallel tracking layer.

Apps affected: `git diff`, `ls --color`, neovim with LSP, glow, bat, delta, lazygit

---

## 2. OSC Sequences

| OSC | Feature | Status | Notes |
|-----|---------|--------|-------|
| 0/1/2 | Window title | OK | vt10x handles; bunk supplements with process/cwd |
| 4 | Set palette color | OK | vt10x handles |
| 7 | CWD notification | OK | Forwarded to host |
| 8 | Hyperlinks | OK | Forwarded to host |
| 10/11/12 | Query fg/bg/cursor color | **MISSING** | vt10x consumes but doesn't respond. Neovim, helix, and many TUIs query these to detect dark/light theme |
| 52 | Clipboard | OK | Forwarded to host |
| 104 | Reset palette color | OK | vt10x handles |
| 110/111/112 | Reset fg/bg/cursor color | **MISSING** | Some apps reset to defaults |
| 133 | Shell integration/prompt marking | **MISSING** | Used by bash/zsh/fish for semantic prompts. Terminals use this for jump-to-prompt. Not forwarded to host |

Highest impact: OSC 10/11 (color query). Without a response, apps can't auto-detect dark vs light theme.

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
| DECRQM (CSI ? Ps $ p) | Request mode | **MISSING** | Apps query whether sync updates/focus events are supported |
| XTVERSION (CSI > 0 q) | Terminal version | **MISSING** | Feature detection |
| DECRQSS | Request setting | **MISSING** | Low impact |

Highest impact: DECRQM. Apps may skip sync updates without a mode-supported response.

---

## 5. Key Encoding

| Feature | Status | Notes |
|---------|--------|-------|
| Basic ASCII keys | OK | |
| Ctrl+letter | OK | |
| Alt+key (ESC prefix) | OK | |
| Arrow keys | OK | |
| F1 | **MISSING** | Not in the switch statement. F1 = `\x1bOP` |
| F2-F12 | OK | |
| F13-F24 | **MISSING** | Shift+F1-F12 should produce F13-F24 |
| Home/End/PgUp/PgDn/Ins/Del | OK | |
| Modified arrows (Ctrl+Up etc) | **MISSING** | Legacy `\x1b[1;5A` form not generated |
| Modified Home/End/etc | **MISSING** | Ctrl+Home, Shift+End, etc. not encoded |
| Keypad keys | **MISSING** | Numpad Enter, keypad digits in app keypad mode |

Highest impact: Modified arrow keys (Ctrl+Left/Right for word-jump in shell readline).

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
| CJK double-width | **MISSING** | No wcwidth/runewidth handling |
| Combining characters | **PARTIAL** | vt10x stores one rune per cell |
| Emoji (multi-codepoint) | **MISSING** | No grapheme clustering |

---

## 8. Cursor Style (DECSCUSR)

| Feature | Status | Notes |
|---------|--------|-------|
| \x1b[N q forwarding | OK | Scans PTY output and forwards to host |
| Reset on exit | OK | Resets to default on shutdown |

---

## Priority Implementation Plan

### Tier 1 — High impact, affects common apps daily

1. **SGR 2 (dim)** — Needs vt10x patch or bitmask extension
2. **SGR 9 (strikethrough)** — Same
3. **Modified arrow keys** — Fix in keyToBytes (Ctrl+Left/Right, etc)
4. **F1 key** — Trivial addition to keyToBytes switch
5. **OSC 10/11 response** — Dark/light theme detection for apps
6. **DECRQM response for mode 2026** — Tells apps sync updates are supported

### Tier 2 — Medium impact, affects specific workflows

7. **OSC 133 forwarding** — Shell integration / prompt marking
8. **Modified special keys** — Ctrl+Home, Shift+End, Ctrl+Delete
9. **CJK double-width characters** — Needs runewidth library
10. **SGR 8 (invisible)** — Password entry in some TUIs

### Tier 3 — Nice to have

11. Colored/curly underlines (neovim LSP diagnostics)
12. F13-F24 (Shift+F-keys)
13. XTVERSION response
14. Grapheme clustering (mode 2027)
15. Graphics protocol passthrough
