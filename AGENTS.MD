# bunk — Agent Guide

bunk is a lightweight terminal multiplexer in Go. Each pane is a real PTY-backed shell; the screen is divided using a BSP tree; tcell handles host terminal I/O.

---

## File map

| File | Responsibility |
|---|---|
| `main.go` | Entry point — calls `Execute()` |
| `cmd.go` | cobra root command, `run()`, app initialization |
| `cmd_config.go` | `bunk config` subcommand |
| `app.go` | `App` struct, event loop, key handling (`keyToBytes`), `triggerRedraw()` |
| `pane.go` | `Pane` struct, PTY spawn, `readPTY`, `captureAndWrite`, scrollback capture |
| `render.go` | `render()`, `renderPane()`, `vtColor()`, border/scrollbar drawing |
| `layout.go` | BSP tree: `Node`, split/remove/resize math |
| `scrollback.go` | `sbRing` ring buffer, scroll-detection algorithm |
| `reflow.go` | Terminal reflow on resize, `rawBuf` replay, `stripAltScreen()` |
| `osc.go` | OSC pre-scanner, passthrough of OSC 7/8/52/133 to host |
| `mouse.go` | Mouse events → PTY byte sequences |
| `status.go` | Status badges (scroll count, container, SSH, flash messages) |
| `search.go` | In-pane text search |
| `config.go` | TOML config, theme registry, keybinding resolution |
| `clipboard.go` | OSC 52 clipboard passthrough |
| `logger.go` | Structured logging |
| `cellaspect.go` | Cell pixel aspect ratio detection |

### `internal/vt10x/` — vendored VT100/ANSI terminal emulator

bunk vendors a fork of `github.com/hinshun/vt10x` with significant local patches. Prefer editing this package over working around it in the main code.

| File | Responsibility |
|---|---|
| `state.go` | `State` struct, `Cursor`, `Glyph`, `ModeFlag` constants, `setAttr`, `setMode`, `QueryPrivateMode` |
| `csi.go` | CSI escape sequence dispatcher (`handleCSI`) |
| `str.go` | String escape handler (OSC, DCS, APC) |
| `parse.go` | Byte-by-byte parser state machine (`put`) |
| `vt.go` | `Terminal` / `View` interfaces, `New()` constructor |
| `vt_posix.go` | `terminal` concrete type, `Write`, `Parse` (POSIX) |
| `color.go` | `Color` type, named colour constants |
| `modes_test.go` | Tests for DECSET 2004/2026, DECSCUSR, `QueryPrivateMode` |

Key extensions over upstream vt10x:
- SGR 2 (dim), 8 (invisible), 9 (strikethrough), 53 (overline), 58 (underline colour), 4:N (underline styles)
- `Cursor.Shape` — DECSCUSR cursor shape (0–6)
- `ModeSetPaste` (DECSET 2004), `ModeSync` (DECSET 2026)
- `QueryPrivateMode(n)` — returns DECRQM status byte for any tracked private mode
- Private-parameter SGR guard (`\x1b[?4m` no longer misfires as SGR 4)

---

## Concurrency

Three goroutines per pane: `readPTY`, `waitForExit`, `trackFgProcess`.
One shared goroutine: `renderLoop` (drains `app.redraw`, buffered 1).

**Lock ordering:** always `app.mu` before `Pane.mu`. Never acquire `app.mu` while holding `Pane.mu`.

---

## Build and test

```bash
make test          # go test ./... -count=1 -race
make build         # runs tests first, then produces bin/bunk_linux_amd64
```

Tests run with `-race`. Tests are a prerequisite of the binary build — both must pass.

**Always run `gofmt -w .` after completing any code changes.** All Go source files must be gofmt-clean before committing.

Manual regression scripts:
- `bash terminal_features.sh text` for SGR/style changes in `render.go` or `internal/vt10x/state.go`
- `bash terminal_features.sh colors` for ANSI/256/RGB/underline-colour changes in `render.go`, `config.go`, or vt10x colour parsing
- `TERMINAL_FEATURES_AUTO=1 bash terminal_features.sh cursor` for cursor-shape, width, emoji, or reflow changes
- `TERMINAL_FEATURES_AUTO=1 bash terminal_features.sh integration` for hyperlink, bracketed-paste, and OSC 133 host-integration changes
- `bash terminal_features.sh queries` plus `bash scripts/osc_smoke.sh` inside bunk for OSC, DECRQM, capability-query, or kitty-keyboard changes in `pane.go`, `osc.go`, or `internal/vt10x/str.go`

---

## Test recipes

**Minimal Pane (no PTY):**
```go
term := vt10x.New(vt10x.WithSize(cols, rows))
p := &Pane{
    term:            term,
    cmd:             &exec.Cmd{},  // non-nil prevents cwd() nil-deref in emitTitle
    x: 0, y: 0, w: cols, h: rows,
    scrollbackLines: 100,
    sb:              sbRing{maxLines: 100},
}
```

**Simulation screen:**
```go
scr := tcell.NewSimulationScreen("UTF-8")
scr.Init()
defer scr.Fini()
scr.SetSize(w, h)
// scr.GetContent(x, y) → (mainc rune, combc []rune, style, width)
// reflects SetContent calls immediately; Show() not required
```

**Minimal App for calling render():**
```go
app := &App{
    screen: scr,
    root:   &Node{pane: p},
    active: p,
    oscCh:  make(chan []byte, oscChanSize),
    theme:  testTheme(),
}
```

---

## Gotchas

### 1. PTY is created 1 column narrower than the pane

```go
pty.StartWithSize(cmd, &pty.Winsize{Rows: h, Cols: w - 1})
```

The rightmost column of each pane (`p.x + p.w - 1`) is reserved for the scrollbar; the PTY never writes there. Keep this in mind when writing geometry assertions or resizing logic.

### 2. Cursor shape and cell content must be read under the same lock

`Cursor().Shape` and `Cell(x, y)` are both stored inside `p.term` (vt10x). A single `p.term.Write(chunk)` call under `p.mu` in `readPTY` updates both atomically. `render()` must read them within the **same** `p.mu` acquisition — splitting across two lock sections lets `readPTY` sneak a new write between them, producing a cursor with a stale shape (which causes the reverse-video swap to produce an invisible cursor). `TestReadPTYSingleLock_ClaudeCursorRace` guards this invariant.

### 3. vt10x treats all characters as single-width

vt10x does not implement East Asian Width — every character (including emoji, CJK) advances the cursor by 1 column regardless of actual display width. `renderPane` compensates with a separate `displayCol` counter that uses `uniseg.StringWidth` to advance by 2 for wide chars. This means vt10x column N does **not** equal screen column N when wide chars appear earlier in the row.

---

## Terminal Feature Audit

`TERMINAL_AUDIT.md` is the authoritative reference for which terminal features are implemented, partially implemented, or missing.

**When investigating a rendering or input bug:** consult `TERMINAL_AUDIT.md` first — the bug may be a known limitation. This avoids re-investigating already-documented gaps.

**When fixing a bug or adding a feature:** update `TERMINAL_AUDIT.md` to reflect the new status. Change `MISSING` → `OK` or `FIXED` (with a brief note), and update the Priority Plan if the item was listed there.
