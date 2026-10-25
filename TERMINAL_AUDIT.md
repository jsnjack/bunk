# Bunk Terminal Feature Audit

Date: 2026-03-12 (updated 2026-10-25)

## Legend
- **OK** — fully handled
- **FIXED** — implemented; notes retain the regression history
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
| 21 | Double underline | OK | Same style as SGR 4:2; does not clear bold/dim |
| 53 | Overline | OK | Stored through reflow and emitted as SGR 53 by the pinned tcell fork; SGR 55/0 clear it. Host must support overline |
| 58;5;N | Colored underline (256) | OK | attrHasULColor flag + UL Color field; vtColor mapping; tcell style.Underline(color) |
| 58;2;R;G;B | Colored underline (RGB) | **FIXED** | Same; RGB values below 256 were palette indexes (see 38;2) |
| 30-37, 90-97 | ANSI FG colors | OK | |
| 40-47, 100-107 | ANSI BG colors | OK | |
| 38;5;N / 48;5;N | 256 colors | OK | |
| 38;2;R;G;B / 48;2;R;G;B | True color (24-bit) | **FIXED** | Was stored as `r<<16\|g<<8\|b`, so zero red and green with blue below 256 equalled a palette index: `38;2;0;0;255` painted palette 255 (grey) and DECRQSS reported `38;5;255`. Truecolour now carries a flag bit (`vt10x.RGB`); OSC 4/10/11/12 overrides too |

Root cause (resolved): vt10x vendored to `internal/vt10x`; SGR 2/8/9 attr bits added and wired.

Apps affected: `git diff`, `ls --color`, neovim with LSP, glow, bat, delta, lazygit

---

## 2. OSC Sequences

| OSC | Feature | Status | Notes |
|-----|---------|--------|-------|
| 0/1/2 | Window title | OK | vt10x handles; bunk supplements with process/cwd |
| 4 | Set palette color | **FIXED** | vt10x handles indices 0-255 and rejects others without changing state; an index equal to a truecolour value used to recolour those cells. A query (`4;N;?`) answers with N and the override; a colour without an override is unknown and gets no reply |
| 7 | CWD notification | OK | Forwarded to host |
| 8 | Hyperlinks | OK | Stored on glyphs and emitted inline with text; hyperlink identities survive reflow. Link IDs no cell refers to are swept and reused, so a pane keeps new links clickable after any number of unique URLs; only live links count against the 65,535-ID and 16 MiB limits. bunk-gui: Ctrl+hover underlines, Ctrl+click opens (http, https, file, mailto, ftp); plain-text http/https/file URLs too |
| 10/11/12 | Query/set fg/bg/cursor color | OK (host-dependent) | In-stream replies in both screen modes, including SSH/mosh. FG/BG changes repaint existing cells; cursor colour follows the active pane via tcell. Terminal-theme defaults are probed at startup; a genuinely unknown default is not fabricated |
| 52 | Clipboard | OK | Forwarded to host |
| 104 | Reset palette color | OK | vt10x handles indices 0-255 |
| 110/111/112 | Reset fg/bg/cursor color | OK | Clears dynamic overrides, repaints FG/BG, and restores the pane's cursor-colour baseline (or the host default when unknown) |
| 133 | Shell integration/prompt marking | OK | Forwarded to host so semantic prompt integration and jump-to-prompt can work when the outer terminal supports it |

Host limitation: an outer terminal that does not disclose a default colour cannot
be queried accurately for that value. Explicit pane colour overrides remain queryable.

---

## 3. DEC Private Modes (DECSET/DECRST)

| Mode | Feature | Status | Notes |
|------|---------|--------|-------|
| 1 | DECCKM (cursor keys app mode) | OK | Unmodified arrows/Home/End use SS3 in application mode; modified keys retain CSI modifier encoding |
| 7 | DECAWM (auto-wrap) | OK | |
| 12 | Cursor blink | OK | Toggles blinking while retaining block/underline/bar shape; queryable with DECRQM |
| 25 | DECTCEM (cursor visible) | OK | |
| 47/1047 | Alt screen | OK | |
| 1000-1003 | Mouse modes | OK | Release events are forwarded in 1000/1002 as well as 1003 |
| 1004 | Focus events | OK | Pane navigation and outer-terminal focus changes are forwarded when enabled |
| 1006 | SGR mouse | OK | |
| 1049 | Alt screen + save cursor | OK | |
| 2004 | Bracketed paste | OK | |
| 2026 | Synchronized updates | OK | Suppresses only the updating pane; a one-second timeout releases abandoned updates |
| 2027 | Grapheme clustering | **FIXED** | Enabled by default; incremental Unicode grapheme assembly with bounded cell storage and legacy per-codepoint mode on reset. In legacy mode VS16 and keycap marks widened their cell to two columns; they now join it without widening, and render paints the base character only |

---

## 4. Terminal Capability Queries/Responses

| Query | Feature | Status | Notes |
|-------|---------|--------|-------|
| DA1 (CSI c) | Primary device attributes | OK | Responds as VT220 |
| DA2 (CSI > c) | Secondary device attributes | OK | Responds as xterm 279 |
| DA3 (CSI = c) | Tertiary device attributes | OK | DCS !\|00000000 ST (no hardware serial number) |
| CPR (CSI 6 n / CSI ? 6 n) | Cursor position report | OK | Both screen modes; respects origin mode and scroll margins |
| DSR (CSI 5 n) | Device status report | OK | CSI 0 n, including remote panes |
| DECRQM (CSI ? Ps $ p) | Request mode | OK | Tracked modes include 12/2027; unknown modes return 0, not the permanently-reset status 4 |
| XTVERSION (CSI > 0 q) | Terminal version | OK | Responds with DCS >|VTE(8203) ST; VTE_VERSION=8203 set in pane env; used by Claude Code, Neovim, WezTerm for feature detection |
| XTGETTCAP (DCS + q) | Terminfo capability query | OK | Reports Smulx/Setulc/Su, TN/name, Co/colors (256), RGB (8 bits/channel), navigation/editing keys, keypad Enter, F1–F24, and modified navigation keys (Shift/Alt/Ctrl combinations). Termcap aliases are supported for unmodified keys. Key values follow current application cursor/keypad and Kitty modes; unknown names receive "not found" |
| DECRQSS | Request setting | OK | SGR (`m`), scroll margins (`r`), and cursor style (`SP q`); unsupported settings receive DCS 0 $ r ST |

---

## 5. Key Encoding

| Feature | Status | Notes |
|---------|--------|-------|
| Basic ASCII keys | OK | |
| Ctrl+letter | OK | |
| Alt+key (ESC prefix) | OK | |
| Arrow keys | OK | |
| Shift+Tab (BackTab) | **FIXED** | Was sending `\x1b[9;3u` (Alt+Tab) in kitty mode; now correctly `\x1b[9;2u` |
| Kitty keyboard protocol | **FIXED** | Push/pop/query stack; CSI u encoding for Enter, Tab, Backspace, Ctrl+letter. Fixed: stale stack after non-alt-screen KKP app exits without `\x1b[<u` — cleared by `trackFgProcess` on PGID change. Fixed: set-flags form `\x1b[=<flags>;<mode>u` (two params) was not stripped — the `;` broke the single-digit scan, leaking the sequence to vt10x which read the trailing `u` as DECRC, corrupting cursor-relative drawing (e.g. Copilot CLI welcome screen / mascot) |
| F1–F12 | OK | Any key bound to a bunk action (default: F1=split, F12=zoom) is consumed outside passthrough mode; bindings are configurable |
| F13-F24 | OK | Shift+F1-F12; handled both via `KeyF13`–`KeyF24` and via `KeyF1`+`ModShift` modifier path |
| Home/End/PgUp/PgDn/Ins/Del | OK | All modifiers forwarded as `\x1b[<code>;<mod>~` / `\x1b[1;<mod>H/F`; Shift+PgUp/PgDn consumed by default for scrollback outside passthrough mode (config-dependent) |
| Modified arrows (Ctrl+Up etc) | OK | Forwarded as `\x1b[1;<mod>A/B/C/D`; Alt+arrows consumed by default for pane nav outside passthrough mode (config-dependent) |
| Modified Home/End/etc | OK | Ctrl+Home, Shift+End, Ctrl+Delete etc. forwarded with xterm modifier parameter |
| Keypad keys | OK (host-dependent) | Keypad identity survives SS3/Kitty input; digits/operators/Enter use application-keypad SS3 or CSI-u. Hosts sending ordinary digit/Enter bytes cannot identify their physical origin |

> **Passthrough:** Ctrl+F12 (remappable as `[keys].passthrough`) toggles per-pane
> keyboard passthrough. All keys except the toggle reach the child application
> using its negotiated encoding. PASS shares the badge layout with highest
> priority; mouse controls remain available.

> **Note:** Outside passthrough mode, any key bound to a bunk action in the user's config is intercepted and not forwarded to the PTY. Default consumed keys: Ctrl+F12 (passthrough toggle, also reserved during passthrough), F1 (split), Alt+F1 (split-context), F12 (zoom), Alt+arrows (pane nav), Shift+PgUp/PgDn (scrollback), Ctrl+C (copy/forward), Ctrl+V (paste), Ctrl+Q (quit), Ctrl+F (search), Ctrl+N (search-next). All of these are user-remappable.

---

## 6. Graphics Protocols

| Feature | Status | Notes |
|---------|--------|-------|
| Sixel | OK (cell-rendered subset) | Static raster data, repeats, RGB/HLS palettes, transparency; square source pixels |
| Kitty graphics | OK (cell-rendered subset) | Direct RGB/RGBA/PNG, zlib, chunking, query replies, cached placement, cropping/scaling, cursor policy, ID/coordinate deletion |
| iTerm2 inline images | OK (cell-rendered subset) | Inline PNG/JPEG/GIF first frame; multipart uploads; cell/pixel/percentage sizing and aspect preservation |

bunk's TUI downsamples images into RGB half-block cells, not native pixels.
bunk-gui draws them at full resolution: the session keeps each placement's
bitmap in a bounded store (64 MiB per pane, least recently used first out), and
each cell draws its own sub-rectangle as a textured quad, 1:1 when the cell size
matches the placement's, scaled otherwise. A cell whose bitmap was evicted, or
that blends a translucent image over another, draws its half-block samples.
Either way, images clip to the pane, scroll into history, survive resize, and
are overwritten/erased like text. Transparent samples blend against the cell background; fully transparent
cells leave existing text intact. Deletion removes visible image cells without
restoring covered text or editing archived scrollback. Layering, animation,
Unicode placeholders, relative placements and non-square SIXEL pixel aspects are
outside this rendering mode; the TUI also lacks native-pixel fidelity. Unsupported Kitty actions
receive an error (subject to `q`); file/shared-memory transfers are rejected.
iTerm2 downloads are never written to disk. Explicit protocol selection may be
needed in clients that rely on host-brand environment variables.

Bounds: 8 MiB encoded transfer, 4096 pixels per axis, 4 million source pixels,
256 Ki output cells, and a 32 MiB / 32-image Kitty cache. Cached images are evicted
oldest-first; a later placement of an evicted ID returns `ENOENT`. Replay history
is bounded too: images whose upload/placement has aged out cannot be reconstructed.
CSI 14/16/18 and PTY pixel sizes describe the same virtual cell geometry.

---

## 7. Unicode / Character Width

| Feature | Status | Notes |
|---------|--------|-------|
| UTF-8 | OK | Boundary detection prevents split-rune corruption |
| CJK double-width | **FIXED** | vt10x tracks display-cell widths and continuations, including right-edge wrapping and editing. Rendering, cursor positions, copy, search, and reflow use those same coordinates |
| Combining characters | OK | Immutable grapheme suffix retained in lead cells, including copy/search/reflow |
| Emoji (multi-codepoint) | OK | ZWJ families, flags, modifiers, variation selectors, and keycaps; widths and continuation cells agree with tcell. Visual shaping still requires a capable host/font |

---

## 8. Cursor Style (DECSCUSR)

| Feature | Status | Notes |
|---------|--------|-------|
| \x1b[N q forwarding | OK | Scans PTY output and forwards to host |
| Reset on exit | OK | Resets to default on shutdown |

---

## 9. Erase / Scrollback Management

| Sequence | Feature | Status | Notes |
|----------|---------|--------|-------|
| ED 0/1/2 (CSI J) | Erase in display | OK | Never touches scrollback — Ctrl+L / `clear -x` keep history, matching xterm |
| ED 3 (CSI 3 J) | Erase saved lines (xterm E3, sent by clear(1)) | OK | vt10x fires a scrollback-clear callback (`WithScrollbackClearCallback`); the pane empties sbRing, snaps sbOff to live, drops any active selection, and reflows only the remaining cells so erased history cannot return |
| RIS (ESC c) | Full reset (sent by reset(1)) | OK | Same scrollback-clear callback, fired after vt10x state reset; init-time reset() does not fire it |

---

## Remaining scope and limitations

All listed feature rows are implemented within their stated scope; this is not
an exhaustive claim of terminal-protocol conformance.

- **Capability queries:** XTGETTCAP exposes the names and key families listed
  above, not an entire terminfo database. Higher function keys and Meta modifiers
  are not exposed. DECRQSS reports only implemented settings; other requests are
  rejected.
  Additional replies must describe behaviour bunk actually supports.
- **Graphics extensions:** animation, layering, Unicode placeholders, relative
  placements, and non-square SIXEL pixel aspects remain unsupported. bunk's TUI
  has no native-pixel output (bunk-gui has); it needs a different rendering
  approach.
- **Host dependencies:** unknown default colours, physical keypad identity when
  the host sends ordinary keys, and font/emoji shaping cannot be recovered from
  information the host does not provide.
- **Intentional bounds:** transfer, image, cache, and scrollback limits remain part
  of the design. Kitty file/shared-memory access is intentionally rejected.

## Completed implementation history

### Capability-query expansion (2026-09-21)

- XTGETTCAP reports the pane's terminal name, indexed colour count, RGB depth,
  and supported key encodings, including termcap aliases and modified navigation.
- Key replies use the normal input encoder with the pane's current modes and
  Kitty flags, including mode changes earlier in the same PTY stream.
- Replies remain pane-local in both screen modes and remote sessions; unsupported
  settings still receive negative replies.

### Keyboard passthrough (2026-09-21)

- Added per-pane Ctrl+F12 passthrough with a persistent PASS badge. Other bunk
  keyboard bindings reach the pane while enabled; the toggle remains reserved.
- PASS shares the status layout and takes priority when badges do not fit.

### Reliability fixes (2026-09-17)

- Primary-screen resize retains terminal modes, cursor style, active attributes,
  dynamic colours, hyperlinks, and scrollback-clear callbacks.
- Replay sizing counts hard line breaks as well as wrapping, preserving short-line history.
- PTY framing caps ordinary controls at 64 KiB (recognized graphics at 8 MiB) and discards oversized
  sequences through their terminator; incomplete input no longer grows unbounded.
- Foreground polling retains Kitty negotiation made by the current process;
  only negotiation owned by a departed foreground process is cleared.
- Repeated transient line clears expire independently and cannot leave rendering suppressed.

### Protocol/text fixes (2026-09-18)

- Implemented SGR 21/53 display, cursor blink/colour, DA3, and DECRQSS.
- Corrected DSR, alternate-screen CPR, origin-relative CPR, remote query replies,
  and unknown-mode DECRQM status. Colour replies no longer depend on alt-screen.
- Added application cursor/keypad input and host focus-event forwarding.
- Added bounded grapheme storage across chunk boundaries, wide-cluster wrapping,
  rendering, selection, search, and reflow.
- Corrected Kitty set-flags add/remove operations, capped negotiation depth,
  and prevented string payloads from being interpreted as keyboard negotiation.
- Bundled the existing tcell v2.13.9 with small overline/keypad/keycap patches;
  its regression tests are included in `make test`.

### Graphics fixes (2026-09-18)

- Implemented the agreed portable, cell-rendered static subset of all three
  graphics protocols; no protocol remains entirely missing.
- Added bounded decoding/transfer/cache limits, malformed-input recovery,
  pane-local clipping, alpha blending, placement deletion, and cell reflow support.
- Added `terminal_features.sh graphics`, simulation-screen and PTY-path tests,
  and decoder fuzz coverage. Native graphics and the extensions excluded above
  are not claimed as implemented.

Protocol references: [xterm control sequences](https://invisible-island.net/xterm/ctlseqs/ctlseqs.html),
[Kitty keyboard protocol](https://sw.kovidgoyal.net/kitty/keyboard-protocol/),
[Unicode grapheme segmentation](https://www.unicode.org/reports/tr29/).
Graphics references: [Kitty graphics protocol](https://sw.kovidgoyal.net/kitty/graphics-protocol/),
[iTerm2 inline images](https://iterm2.com/documentation-images.html),
[DEC VT300 reference](https://vt100.net/dec/ek-vt3xx-hr-002.pdf).

### Resize ordering (2026-09-28)

- Host resize bursts coalesce emulator reflow and PTY size notification together.
  The emulator reaches the new dimensions before SIGWINCH can trigger a redraw;
  superseded resize callbacks are ignored.
- Regression coverage exercises primary and alternate screens across two width
  reductions and an expansion, with redraws at the notified right margin.
- Primary-screen resize reflows stored glyphs instead of replaying historical
  cursor commands. Soft wraps are joined before repacking; hard breaks remain.
  Cursor position, scroll anchoring, glyph styles, hyperlinks, and image samples
  are preserved. Raw PTY replay storage and its serializer have been removed.
- Reflow output is bounded by the configured scrollback limit plus pane height.
  A two-cell glyph becomes a replacement character if the pane is only one
  column wide, matching the emulator's handling of new output at that width.
- When narrowing would move the cursor into scrollback, the cursor's line
  stays visible and excess rows below it are clipped, as with screen shrinking.

### Parser throughput (2026-10-24)

- Runs of printable ASCII skip grapheme segmentation and fill row segments in
  block copies. Parser state is an enum, and CSI parameters parse in place.
- Steady-state parsing makes no allocations; it made about 2 per byte.
- `TestASCIIFastPathMatchesRuneParser` compares the fast path with the
  rune-at-a-time parser on random streams, including wide-cell invariants.
- Grapheme clusters are capped at two cells. uniseg adds Prepend widths
  (`\u0600\u0600a` reported 3), which left orphan continuation cells.
- PTY framing reuses its output buffer. Scrollback rows drop trailing blank
  cells that render, copy and search like missing ones.
- The grid and scrollback store 16-byte cells without pointers instead of
  56-byte glyphs; rare data sits in a per-row side table. Scrollback for
  10 000 lines in a 200-column pane takes 16 MB (90-character lines) or
  33 MB (full lines) instead of 55 or 124 MB, and a full GC cycle over it
  dropped from 3–5 ms to about 1 ms.
- Rune widths are cached for code points below U+30000.
- Measured pinned to a Zen 5 core (`taskset -c 0`), master → now:
  `BenchmarkPaneOutput` ASCII 9.7 → 154 MB/s, full-width lines 11.9 →
  357 MB/s, SGR-heavy 11.0 → 55 MB/s; `BenchmarkWrite` ASCII 11.9 → 493,
  SGR 14.8 → 146, Unicode 14.6 → 36, TUI redraws 13.3 → 266 MB/s.
  On the Zen 5c CPUs (4–11, 16–23) the same code runs about 35% slower, so
  pin benchmarks before comparing.

### Parser throughput for coloured and Unicode output (2026-10-25)

- `Write` hands text and CSI sequences to `scan` instead of parsing rune by
  rune. CSI parameters are collected in one pass; plain decimal parameters
  are read without splitting.
- Runes that can never join a neighbour skip grapheme segmentation: CJK,
  box drawing, Hangul syllables and most emoji. Their class and width share
  one cached 4-bit entry per code point. The classes come from probing
  uniseg, and `TestSimpleGraphemesNeverJoin` and `TestEndsCluster` check
  them over random pairs and clusters.
- Joining runes (accents, VS16, skin tones, ZWJ sequences, flags, keycaps)
  still segment, but each State keeps 64 recent segmentations, so repeated
  sequences cost no uniseg call and no allocation.
- U+2E3A and U+2E3B are two cells wide. uniseg reports three and four, which
  stored a negative width and joined the next character to the dash.
- In the session, PTY framing appends CSI parameters in bulk, the OSC and
  query scanners skip to the next ESC, and query matching switches on the
  byte after `ESC [`.
- `TestFastPathsMatchRuneParser` and `FuzzFastPaths` compare the fast paths
  with the rune parser on streams of mixed runes and sequences;
  `FuzzCSIParams` and `FuzzFraming` compare against the previous code.
- Measured pinned to a Zen 5 core, ten interleaved runs, medians, before →
  after. `BenchmarkWrite` (MB/s): ASCII 622 → 698, SGR 150 → 311, Unicode
  31 → 186, TUI 271 → 617, agent transcript 74 → 367, highlighted Go
  100 → 337, box drawing 96 → 486, CJK 32 → 294, emoji 56 → 156.
  `BenchmarkOutput`: ASCII 290 → 402, full rows 345 → 549, SGR 68 → 148,
  Go source 110 → 135, agent transcript 47 → 180, highlighted Go 51 → 156,
  box drawing 56 → 195, CJK 28 → 200, emoji 47 → 117.

### Automated feature scripts (2026-10-25)

- All seven `terminal_features.sh` groups run as Go tests
  (`terminal_features_test.go`). Each test runs the real script in a bunk
  pane on a PTY, answers its pauses through bunk's key and paste handling,
  and checks painted cells, cursor shape and position, printed query
  replies, and OSC 7/52/133 reaching the host through `app.oscBuf`.
- Query helpers wait for a DA1 fence instead of sleeping; the query and OSC
  groups dropped from about 15 s of sleeps to under 0.3 s.
- Bugs found: truecolour below 256 rendered as a palette colour (SGR row
  above); DECRST 2027 widened cells for VS16 and keycaps (mode 2027 row).
- Script bugs fixed: the OSC 8 line and other sequences put ST (`ESC \`)
  inside printf formats, so `\b` became a backspace and the link text and
  newline were lost. The bold colour row reset bold after its first block,
  and the bracketed-paste message toggled 2004 instead of printing it.
- New script checks: DECRQSS cursor style, margins and negative reply;
  CSI 14/16/18 t; Kitty push and pop; per-codepoint widths with 2027 reset;
  OSC 7 and OSC 52; the pasted bytes echoed back with their 200~/201~ markers.

### Scroll path (2026-10-25)

- Rows record how far their text reaches, so clearing a recycled row and
  trimming a scrolled-off row cost the text length instead of the width.
  Screen rows are pointers, and a full-screen scroll marks rows dirty with
  one flag. A differential run against the previous emulator (4000 random
  streams with erases, insert/delete, scroll regions, coloured blanks, wide
  cells, links, images and resizes) matched every cell, dirty row and
  scrolled row.
- `BenchmarkOutput` pinned to a Zen 5 core, 200x50, before → after: ASCII
  144–157 → 338–344 MB/s, Go source (about 33-byte lines) 54 → 117–127,
  SGR-heavy 56 → 77–78, full-width lines 360–364 → 406–408.
