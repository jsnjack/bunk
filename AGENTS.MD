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
| `osc.go` | OSC pre-scanner, passthrough of OSC 7/8/52 to host |
| `mouse.go` | Mouse events → PTY byte sequences |
| `status.go` | Status badges (scroll count, container, SSH, flash messages) |
| `search.go` | In-pane text search |
| `config.go` | TOML config, theme registry, keybinding resolution |
| `clipboard.go` | OSC 52 clipboard passthrough |
| `logger.go` | Structured logging |
| `cellaspect.go` | Cell pixel aspect ratio detection |

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

---

## Test recipes

**Minimal Pane (no PTY):**
```go
term := vt10x.New(vt10x.WithSize(cols, rows))
p := &Pane{
    term:            term,
    cmd:             &exec.Cmd{},  // see gotcha #1
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

### 1. `cmd: &exec.Cmd{}` is required in render-path tests

`emitTitle()` calls `p.cwd()`, which dereferences `p.cmd`. If `p.cmd` is nil the test panics. `&exec.Cmd{}` gives a non-nil pointer with `Process == nil`, which makes `cwd()` return `""` safely.

### 2. PTY is created 1 column narrower than the pane

```go
pty.StartWithSize(cmd, &pty.Winsize{Rows: h, Cols: w - 1})
```

The rightmost column of each pane (`p.x + p.w - 1`) is reserved for the scrollbar; the PTY never writes there. Keep this in mind when writing geometry assertions or resizing logic.

### 3. `readPTY` flag updates and `captureAndWrite` must stay in one lock section

`cursorStyle`, `inSyncUpdate`, and `wantsBracketedPaste` are scanned from the raw PTY chunk and then written to the pane under `p.mu` **in the same lock acquisition** as the `captureAndWrite` call. Splitting them into two lock sections lets `render()` sneak between them and observe updated flags with stale vt10x cell state — which caused an intermittent invisible cursor (reverse-video swap undone). `TestReadPTYSingleLock_ClaudeCursorRace` guards this.

### 4. `vtColor`: `def` is ignored for `DefaultFG` and `DefaultBG`

```go
case vt10x.DefaultFG: return rt.fg   // def is not used
case vt10x.DefaultBG: return rt.bg   // def is not used
case vt10x.DefaultCursor: return def  // only here is def used
```

The parameter name `def` implies "default fallback" but it only applies to `DefaultCursor`. For `DefaultFG`/`DefaultBG` the theme colours are always returned unconditionally. This is intentional: vt10x pre-swaps FG↔BG for reverse-video cells, and the invariant `DefaultFG → rt.fg` / `DefaultBG → rt.bg` must hold for the swap to produce visible contrast.

### 5. vt10x treats all characters as single-width

vt10x does not implement East Asian Width — every character (including emoji, CJK) advances the cursor by 1 column regardless of actual display width. `renderPane` compensates with a separate `displayCol` counter that uses `uniseg.StringWidth` to advance by 2 for wide chars. This means vt10x column N does **not** equal screen column N when wide chars appear earlier in the row.

---

## Terminal Feature Audit

`TERMINAL_AUDIT.md` is the authoritative reference for which terminal features are implemented, partially implemented, or missing.

**When investigating a rendering or input bug:** consult `TERMINAL_AUDIT.md` first — the bug may be a known limitation (e.g. SGR 2 dim, modified arrow keys, F1 key). This avoids re-investigating already-documented gaps.

**When fixing a bug or adding a feature:** update `TERMINAL_AUDIT.md` to reflect the new status. Change `MISSING` → `OK` or `FIXED` (with a brief note), and update the Priority Plan if the item was listed there.
