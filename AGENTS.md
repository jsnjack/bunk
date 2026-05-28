# AGENTS.md

> See [AGENTS.universal.md](./AGENTS.universal.md) and [AGENTS.go.md](./AGENTS.go.md) for universal conventions.
> Refresh: `make standards`

---

## Overview

bunk is a lightweight terminal multiplexer written in Go. Each pane is a real
PTY-backed shell; the screen is divided using a BSP tree; tcell handles host
terminal I/O. It is used daily with Copilot CLI, Claude Code, btop, vim, and
other TUI apps.

---

## Architecture

```
main.go             Entry point — calls Execute()
cmd.go              cobra root command, run(), app initialization
cmd_config.go       "bunk config" subcommand tree
app.go              App struct, event loop, key handling (keyToBytes), triggerRedraw()
pane.go             Pane struct, PTY spawn, readPTY, captureAndWrite, scrollback capture
render.go           render(), renderPane(), vtColor(), border/scrollbar drawing
layout.go           BSP tree: Node, split/remove/resize math
scrollback.go       sbRing ring buffer, scroll-detection algorithm
reflow.go           Terminal reflow on resize, rawBuf replay, stripAltScreen()
osc.go              OSC pre-scanner, passthrough of OSC 7/8/52/133 to host
mouse.go            Mouse events → PTY byte sequences
status.go           Status badges (scroll count, container, SSH, flash messages)
search.go           In-pane text search
config.go           TOML config, theme registry, keybinding resolution
hostcolors.go       Host terminal OSC colour probing
clipboard.go        OSC 52 clipboard passthrough
logger.go           Structured logging (slog)
cellaspect.go       Cell pixel aspect ratio detection

internal/vt10x/     Vendored VT100/ANSI emulator (fork of github.com/hinshun/vt10x)
  state.go          State struct, Cursor, Glyph, ModeFlag constants, setAttr, setMode
  csi.go            CSI escape sequence dispatcher (handleCSI)
  str.go            String escape handler (OSC, DCS, APC)
  parse.go          Byte-by-byte parser state machine (put)
  vt.go             Terminal / View interfaces, New() constructor
  vt_posix.go       terminal concrete type, Write, Parse (POSIX)
  color.go          Color type, named colour constants

assets/             Demo assets (gif)
scripts/            Manual regression helper scripts
```

Key local extensions to vendored vt10x:
- SGR 2/8/9/53/58 and 4:N underline styles
- `Cursor.Shape` (DECSCUSR)
- `ModeSetPaste` (DECSET 2004), `ModeSync` (DECSET 2026)
- `QueryPrivateMode(n)` — returns DECRQM status byte for any tracked private mode
- Private-parameter SGR guard (`\x1b[?4m` no longer misfires as SGR 4)

---

## Key Flows

1. **Startup** — `main()` → `Execute()` → `run()` loads config, initialises
   slog, queries cell aspect, probes host OSC colours, initialises tcell,
   spawns the first `Pane` and enters `App.eventLoop()`.
2. **Pane I/O** — three goroutines per pane: `readPTY` (PTY → vt10x →
   redraw), `waitForExit`, `trackFgProcess`. One shared `renderLoop` drains
   `app.redraw` (buffered 1).
3. **Resize / reflow** — host terminal resize triggers `App.resize()`, which
   recomputes the BSP tree and calls `Pane.reflow()` to replay `rawBuf`
   against a new vt10x state at the new column width.
4. **OSC passthrough** — `osc.go` pre-scans PTY bytes; OSC 7/8/52/133 are
   forwarded to the host terminal via `app.oscCh`, never written into vt10x.

**Lock ordering:** always `app.mu` before `Pane.mu`. Never acquire `app.mu`
while holding `Pane.mu`.

---

## Build & Run

```bash
make check    # full validation gate (fmt → vet → build → test → lint)
make test     # tests only (race-enabled)
make build    # cross-compiles bin/bunk_{linux,darwin}_{amd64,arm64}
make run      # builds local binary and opens a debug session with --trace
```

Tests run with `-race` and are required to pass before any binary is built.

Manual regression scripts (run for changes that touch the listed areas):
- `bash terminal_features.sh text` — SGR/style changes in `render.go` or `internal/vt10x/state.go`
- `bash terminal_features.sh colors` — ANSI/256/RGB/underline-colour changes
- `TERMINAL_FEATURES_AUTO=1 bash terminal_features.sh cursor` — cursor-shape, width, emoji, reflow
- `TERMINAL_FEATURES_AUTO=1 bash terminal_features.sh integration` — hyperlink, bracketed-paste, OSC 133
- `bash terminal_features.sh queries` + `bash scripts/osc_smoke.sh` — OSC, DECRQM, capability queries, kitty-keyboard

---

## Configuration

Config file: `~/.config/bunk/config.toml` (override with `--config`). Generate
a documented default with `bunk config init`.

Key fields agents may need to know about:
- `theme` — built-in name (`terminal`, `default`, `solarized-dark`, `dracula`, `nord`) or custom palette
- `scrollback` — per-pane scrollback line cap (default 10 000)
- `log_file` — destination for `--debug` / `--trace` output (default `/tmp/bunk.log`)
- `cell_aspect` — fallback height/width ratio when the host terminal doesn't answer the pixel-size query
- `[keybindings]` — overrides for split / zoom / quit / search / copy / paste
- `[ui]` — hex colour overrides for borders and scrollbar

`config.go` is the authoritative reference for every field and its default.

---

## Design Decisions

- **PTY is one column narrower than the pane.** The rightmost column
  (`p.x + p.w - 1`) is reserved for the scrollbar; the PTY never writes there.
- **Cursor shape and cell content must be read under the same lock.** Both
  live inside `p.term`; `render()` must read them within the same `p.mu`
  acquisition so `readPTY` can't sneak in a write between the two. Guarded by
  `TestReadPTYSingleLock_ClaudeCursorRace`.
- **vt10x treats all characters as single-width.** `renderPane` keeps a
  separate `displayCol` counter using `uniseg.StringWidth` to advance by 2
  for wide chars — vt10x column N ≠ screen column N when wide chars appear
  earlier in the row.
- **Terminal cleanup runs synchronously in `main` after the event loop.**
  Background goroutines can't be relied on to finish their cleanup before
  the process exits.
- **Nested sessions are refused.** `BUNK=1` is exported into every pane's
  environment; the binary exits at startup if it sees it set.

---

## Gotchas

- TUI owns stderr — do not write logs there. All diagnostic output goes
  through `slog` to the trace file (`/tmp/bunk.log`). Errors that the user
  needs to see surface through the UI.
- Vendored vt10x has local patches. Prefer fixing bugs there over working
  around them in the main code.
- `TERMINAL_AUDIT.md` is the authoritative reference for which terminal
  features are implemented, partial, or missing. When investigating a
  rendering or input bug, consult it first; when fixing a feature, update it
  to reflect the new status.

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
