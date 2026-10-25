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
cmd_notify.go       "bunk notify": the agent hook CLI (notify.RunCLI)
app.go              App struct, event loop, key handling, triggerRedraw()
input_modes.go      Host focus forwarding
input_tcell.go      tcell key/mouse events → internal/input (keyToBytesMode, mouseToBytes)
pane.go             Pane (embeds *session.Session): placement, render state, NewPane, Hooks
render.go           render(), renderPane(), vtColor(), border/scrollbar drawing
graphics.go         Image colour blending
layout.go           BSP tree: Node, split/remove/resize math
osc.go              Host passthrough buffer for OSC 7/52/133
mouse.go            Mouse events → PTY byte sequences
status.go           Status badges (agent state, scroll count, container, SSH,
                    flash messages); agent-state separator colours
search.go           In-pane search UI (matching is session.FindAll)
config.go           TOML config, theme registry, keybinding resolution
hostcolors.go       Host terminal OSC colour probing
clipboard.go        OSC 52 clipboard passthrough
logger.go           Structured logging (slog)
cellaspect.go       Cell pixel aspect ratio detection

internal/vt10x/     Vendored VT100/ANSI emulator (fork of github.com/hinshun/vt10x)
  state.go          State struct, Cursor, Glyph, ModeFlag constants, setAttr, setMode
  csi.go            CSI escape sequence dispatcher (handleCSI)
  str.go            String escape handler (OSC, DCS, APC)
  parse.go          Parser state machine (put); scan, putText and putCSI handle
                    text runs and CSI sequences in bulk
  vt.go             Terminal / View interfaces, New() constructor
  links.go          OSC 8 URL table: interning, sweeps, ID reuse
  vt_posix.go       terminal concrete type, Write, Parse (POSIX)
  color.go          Color type, named colour constants
  row.go            16-byte cell storage, Row with per-row side table
  grapheme.go       Incremental grapheme assembly, bounded cell text, cached
                    segmentations and the simple-grapheme classes
  status.go         DECRQSS setting serialization
  graphics.go       Image-to-cell painting, Kitty placement deletion
  rewrap.go         Rewrap: packs stored cells of logical lines at a new width

internal/graphics/ Bounded SIXEL, Kitty, and iTerm2 static image decoders;
                    store.go keeps placement bitmaps for full-resolution drawing

internal/termcorpus/ Realistic output streams for the parser benchmarks
                    (agent transcript, highlighted Go, TUI, CJK, emoji)

internal/input/     Toolkit-neutral key, mouse, focus and paste encoding
                    (legacy, application cursor/keypad, Kitty CSI u)

internal/session/   One terminal session, shared by front ends
  session.go        Session, Start/Run/ReadLoop/Feed, Hooks, query replies, Kitty keys, selection
  scrollback.go     Ring of compact scrollback rows without trailing blanks
  links.go          Marks the link IDs the scrollback keeps for link sweeps
  reflow.go         Cell-based reflow and scroll anchoring
  ptystream.go      Bounded streaming UTF-8/control framing before parsing
  osc.go            OSC pre-scanner and sanitising (7, 52, 133)
  process.go        Foreground process, SSH host and container detection
  search.go         Case-insensitive search over scrollback and the live grid
  status.go         Agent status: AgentSpec detection, attention requests
                    (OSC 9/99/777, BEL), failures (OSC 133;D), git branch,
                    explicit agent reports (Notify)
  winsize.go        PTY window size in cells and virtual pixels
  ptyfd.go          Setsize: window-size ioctl that holds the PTY descriptor
  ptyfd_linux.go    Pollable PTY master, so Close ends ReadLoop
  writer.go         PTY writer: one queue for query replies and input,
                    written by one goroutine; caps, drops and refusals

internal/notify/    Agent hook reports: Unix socket server (token → Session),
                    client, `notify` CLI (RunCLI), hook snippets per agent

internal/leakcheck/ Test helper: fds, goroutines, child processes and live heap
                    of the process, compared with a baseline after a settle wait

internal/ptychild/  Test helper: a raw-mode child that floods numbered DECRQM
                    queries and reads its input only when the test says so

third_party/tcell/  Pinned tcell v2.13.9 with overline/keypad/keycap-width patches
                    (local go.mod replacement; see BUNK_PATCHES.md)

cmd/bunk-gui/       GTK4 GUI front end (own Go module; cgo; not built by make check)
packaging/          Desktop entry, icon, metainfo; fedora/ holds the RPM spec

assets/             TUI demo gif; bunk-gui screenshots (regenerate with make screenshots)
docs/               agent-hooks.md: hook recipes and the notify protocol
scripts/            Terminal feature scripts (tested by terminal_features_test.go);
                    headless-gui.sh runs GUI tests; screenshots.sh renders assets/;
                    benchcheck/ runs and compares benchmarks (make bench-check)
testdata/           bench-baseline.txt: the timings bench-check compares with
```

Key local extensions to vendored vt10x:
- SGR 2/8/9/21/53/58 and 4:N underline styles
- `Cursor.Shape` (DECSCUSR)
- `ModeSetPaste` (DECSET 2004), `ModeSync` (DECSET 2026)
- `QueryPrivateMode(n)` — returns DECRQM status byte for any tracked private mode
- Private-parameter SGR guard (`\x1b[?4m` no longer misfires as SGR 4)
- Display-cell widths and wide-glyph continuations; `ReplaceScreen` preserves
  terminal modes, attributes, callbacks, and hyperlink identities during reflow
- DEC 12 cursor blink; DEC 2027 grapheme widths (enabled by default).
  `Glyph.Combining` stores an immutable grapheme suffix, bounded to 1 KiB/cell.
  With DECRST 2027 a zero-width code point (VS16, keycap) joins a cell
  without widening it; `renderPane` then paints only the base character.
- `Color`: 0-255 are palette indexes. Truecolour comes from `vt10x.RGB` and
  carries bit 24, so `38;2;0;0;255` never equals palette 255. Decode with
  `c < 256` (palette), then `c < DefaultFG` (RGB in the low 24 bits).
  The `Default*` values start at `1<<25`.
- DECRQSS reports SGR, scroll margins, and cursor style; unsupported settings
  return a negative response. Unknown DECRQM modes return status 0, not 4.
- CSI parameters are unsigned decimal numbers capped at 65535, as in xterm
  (`MAX_I_PARAM`); a larger count acts as 65535 instead of wrapping negative.
  OSC 4/104 take palette indices 0-255 only, because every cell colour goes
  through the override table.
- The grid stores 16-byte, pointer-free cells (`row.go`). Combining text,
  underline colour, hyperlink and image samples live in a per-row side table.
  Each Row tracks `used`: every cell from that index on equals `tail`, so
  clearing and scrollback trimming cost the text length, not the row width.
  Write cells through `set`, `fill`, `writeASCII` or `setWrap`; a direct
  `cells[x]` write must call `touch(x)` (or `touchAll` after shifting).
  `Cell`/`RawCell` still return `Glyph`; the scroll callback receives a `Row`,
  which bunk's scrollback ring copies without trailing blanks.
- `Write` sends most input through `scan` instead of `put`. `putText` writes
  printable ASCII and runes that never join a neighbour without grapheme
  segmentation; `putCSI` collects CSI parameters in one pass. The rune
  classes (`simpleGrapheme`, `endsCluster`) are cached with the width in
  `runeInfo`. Joining runes still go through `extendGrapheme`, which keeps
  recent segmentations in `State.clusters`.
- Any change to `parse`, `print`, `setChar`, the scan paths or the rune
  classes must keep `TestASCIIFastPathMatchesRuneParser`,
  `TestFastPathsMatchRuneParser`, `TestSimpleGraphemesNeverJoin`,
  `TestEndsCluster` and the `FuzzParse`/`FuzzFastPaths` seeds green. Track
  speed with `go test ./internal/vt10x -bench BenchmarkWrite` and
  `go test ./internal/session -bench BenchmarkOutput`.

---

## Key Flows

1. **Startup** — `main()` → `Execute()` → `run()` loads config, initialises
   slog, queries cell aspect, probes host OSC colours, initialises tcell,
   spawns the first `Pane` and enters `App.eventLoop()`.
2. **Pane I/O** — `Session.Run` starts three goroutines per pane:
   `ReadLoop` (PTY → vt10x), `waitForExit`, `trackFgProcess`. bunk attaches
   through `session.Hooks`: `Applied` runs under the session lock after each
   chunk, `Output` after it (repaint pacing). One shared `renderLoop` drains
   `app.redraw` (buffered 1). Replies and input reach the PTY through the
   session's writer (`writer.go`); its goroutine runs while bytes are queued.
3. **Resize / reflow** — `App.handleResize()` coalesces host resizes and
   updates the BSP tree. Emulator reflow finishes before the PTY receives its
   new size and SIGWINCH; both are deferred together during resize bursts.
   `Session.ResizeAndReflow()` joins soft-wrapped rows from scrollback and the live
   grid, repacks their cells at the new width, and preserves terminal state.
4. **OSC passthrough** — bounded `ptyStream` framing precedes `osc.go` scanning.
   OSC 7/52/133 reach the host through `app.oscBuf`; OSC 8 links are stored on
   glyphs and emitted with their rendered text.

**Lock ordering:** always `app.mu` before the session lock (`p.Lock()`, the
mutex embedded in `session.Session`). Never acquire `app.mu` while holding a
session lock.

---

## Build & Run

```bash
make check    # full validation gate (fmt → vet → race tests → build → lint)
make test     # tests only (race-enabled)
make build    # cross-compiles bin/bunk_{linux,darwin}_{amd64,arm64}
make run      # builds local binary and opens a debug session with --trace
make gui-test # bunk-gui: vet, race tests, packaging validation
make check-all # check + gui-test
make rpm      # Fedora RPM of bunk-gui in bin/rpmbuild, checked by packaging/fedora/check-rpm.sh
make fuzz     # every Fuzz target in turn, FUZZTIME each (default 30s); not in check-all
make soak     # leak and soak tests at full length (SOAK=1); not in check-all
make bench-check    # key benchmarks pinned to one core; fails on a >10% slowdown
make bench-baseline # records testdata/bench-baseline.txt for bench-check
```

Leak and soak tests check that sessions, panes and the notify socket release
everything they take. Each takes a `leakcheck.Snapshot`, runs a workload and
calls `leakcheck.Check`, which waits for the counts to settle and lists new
descriptors and child processes and the goroutine stacks on failure.
`TestCheckCatchesLeaks` injects each kind of leak to prove the detectors.
bunk-gui's `mark` action records the same counts plus live panes and their
GL textures in `-report`.

| Test | Short run (`make test`) | `make soak` |
|------|-------------------------|-------------|
| `TestSessionLeaks` (session) | 300 sessions in 2 batches | 2000 in 10 |
| `TestOutputSoak` (session) | 3 MiB with -race, 20 MiB without | 500 MiB |
| `TestTUISoak` (root) | 5 split/zoom/resize/exit cycles | 200 |
| `TestNotifySessionLeaks` (notify) | 60 reporting sessions | 400 |
| `TestGUITabLeaks` (bunk-gui, `make gui-test`) | 20 tabs with splits | 200 |

`make soak` runs without the race detector; `SOAKFLAGS=-race make soak` adds
it. Session kinds cover shells that exit, busy output closed mid-stream
(`yes`, `/dev/urandom`), children that ignore SIGHUP and children that leave
a process holding the PTY. A raw-mode child that never reads is closed with
its PTY writer blocked; TestTUISoak and TestGUITabLeaks run such panes too.
A helper child that is the test binary itself
sets `GORACE=atexit_sleep_ms=0`, or a -race build sleeps 1 s at exit.

**Run `make bench-check` before merging a performance-sensitive change.**
It compares the key benchmarks with `testdata/bench-baseline.txt`. Neither
bench target is part of `check` or `check-all`, because timings depend on load.
- **What runs:** `scripts/benchcheck` builds each test binary once. It runs
  every vt10x benchmark, session `BenchmarkOutput` and `BenchmarkReflow`, and
  bunk-gui `BenchmarkFullScreenFrame`. Write and Output include the
  `termcorpus` streams. About 4 minutes at the default count.
- **Interleaved rounds:** each of `BENCH_COUNT` rounds (10) runs every suite
  once. A burst of load then hits all benchmarks alike.
- **Pinning:** every run uses `taskset -c $(BENCH_CPU)` (CPU 0, a fast Zen 5
  core) and `GOMAXPROCS=1`. The benchmarked paths are single-threaded, so
  garbage collection runs on the measured core.
- **Disturbed runs:** a run is repeated up to twice, and the least disturbed
  attempt kept, on any of three signs. CPU time under 90% of wall time means a
  shared core. A clock under 80% of the top clock means a power limit or heavy
  load. An SMT sibling (CPU 12) over 50% busy shares the core's caches.
- **Verdict:** medians of `cpu-ns/op` where both files have it (reflow), else
  `ns/op`. A benchmark fails on two conditions together. A Mann-Whitney U
  test gives p < 0.05, and the median grew by over `BENCH_THRESHOLD` percent (10).
- **Not failures:** new or missing benchmarks, faster results, and warnings.
- **Warnings:** a load average above `BENCH_MAX_LOAD` (4; the benchmark adds 1).
  Disturbed runs that were kept. A different Go version, CPU, kernel, power
  profile or core clock. Changed suite settings. An allocation-free benchmark
  that now allocates.
- **Load on other cores:** it lowers the pinned core's clock from 5.1 GHz.
  Load 10 gives about 4.6 GHz, load 50 about 3.7 GHz. Compute-bound times grow
  by as much. Same-code runs under different load differ by up to about 10%.
  Rerun a failure that comes with a clock or load warning.
- **Partial runs:** `BENCH_SUITES=vt10x,session-output` runs a subset; the
  rest show as missing. `BENCH_TIME` overrides every suite's benchtime, which
  changes results: compare only runs with equal settings.
- **GUI frame benchmark:** it runs with `-test.short`, without a display, into
  a fake rasterizer. When bunk-gui does not build, it is skipped with a note.
  Its TestMain sets `runtime.MemProfileRate = 1`, which slows allocating code.
- **New baseline:** after an intended speed change, run `make bench-baseline`
  on a quiet machine. Commit `testdata/bench-baseline.txt` with the change.
  Its header records Go, CPU, kernel, power settings, commit, date and load.
- **Exit codes:** `go run ./scripts/benchcheck compare BASE CUR` exits 1 on a
  regression and 2 on an error. `bin/bench-current.txt` keeps the last run.

Fuzz targets cover every parser of program output and user input:
`internal/vt10x` (FuzzParse, FuzzResize), `internal/session` (FuzzPTYStream,
FuzzOSCScanner, FuzzStatusOSC, FuzzQueryReplies, FuzzXTGETTCAP,
FuzzFindAndSelect, FuzzPTYWriterOrder), `internal/input` (FuzzEncodeKey, FuzzEncodeMouse) and
`internal/graphics` (FuzzGraphicsDecoder, FuzzStore). Their seed corpora in
`testdata/fuzz` run with every `go test`, so keep seeds small. When a target
fails, keep the input it wrote to `testdata/fuzz`, fix the code, and add a
named regression test to the package's `fuzz_regression_test.go`. Run one
target longer with `go test ./internal/vt10x -run '^$' -fuzz '^FuzzParse$'
-fuzztime 10m`.

bunk-gui window tests run on a private headless mutter through
`scripts/headless-gui.sh`, never on the desktop. It gives one 2560x1600@120
virtual monitor at 133% on the GPU, with its own D-Bus and clipboard. Every
GUI check, including pixel, idle-frame, allocation and latency checks, lives
in the test suite; turn a manual check into a test before relying on it.
`go test -short` skips the window tests (the RPM's %check uses it).

**Run `make check-all` before reporting any work as done.** Work is not done
until the full suite is green; report its result, including failures.

Tests run with `-race` and are required to pass before any binary is built.
`make test` includes the locally patched tcell module and its terminfo tests.
The gate is serialized even with `make -j`; the project-specific test-before-
build rule takes precedence over the generic gate ordering.

Terminal feature scripts (`terminal_features.sh`, `scripts/terminal_features/`):
- **All seven groups are automated.** `terminal_features_test.go` has one
  `TestTerminalFeatures*` test per group; `make test` runs them (under 1 s).
- **Each test runs the real script** with bash on a PTY in a bunk pane
  (`features_harness_test.go`). It uses bunk's hooks, key handling, paste
  and render path, answers each pause, and checks the simulation screen,
  cursor, printed replies and host output (`App.hostOut`).
- **Change a script and its test together.** The tests read 06's assertion
  names and 05's "Bunk typically reports" lines from the scripts.
- **Run a group by hand only to judge the host terminal:** fonts, emoji
  shaping, visible underline styles, clicking links, the real clipboard.
  `TERMINAL_FEATURES_AUTO=1` skips the pauses; 04 writes the clipboard.

| Group | Script | Test |
|-------|--------|------|
| text | `01_text_attributes.sh` | `TestTerminalFeaturesText` |
| colors | `02_colors.sh` | `TestTerminalFeaturesColors` |
| cursor | `03_cursor_unicode.sh` | `TestTerminalFeaturesCursor` (shapes, widths, 2027, reflow) |
| integration | `04_host_integration.sh` | `TestTerminalFeaturesIntegration` (OSC 8, paste, OSC 7/52/133) |
| queries | `05_queries.sh` | `TestTerminalFeaturesQueries` |
| osc | `06_osc_smoke.sh` | `TestTerminalFeaturesOSC` |
| graphics | `07_graphics.sh` | `TestTerminalFeaturesGraphics` (clipping, resize, scrollback) |

Query helpers in `lib.sh` send DA1 after each query and read until its
reply, so they need no fixed sleep. `TERMINAL_FEATURES_QUERY_TIMEOUT`
(default 2 s) bounds the wait for a silent terminal.

---

## Configuration

Config file: `~/.config/bunk/config.toml` (override with `--config`). Generate
a documented default with `bunk config init`.
Malformed/unreadable files and missing explicit `--config` paths fail at startup;
a missing default file uses built-in defaults.

Key fields agents may need to know about:
- `theme` — built-in name (`terminal`, `default`, `solarized-dark`, `dracula`, `nord`) or custom palette
- `scrollback` — per-pane scrollback line cap (default 10 000)
- `log_file` — destination for `--debug` / `--trace` output (default `/tmp/bunk.log`)
- `cell_aspect` — fallback height/width ratio when the host terminal doesn't answer the pixel-size query
- `[keys]` — overrides for split / zoom / quit / search / copy / paste /
  next_needs_you; "none" disables an action and sends its key to the pane
- `[ui]` — hex colour overrides for borders and scrollbar

`config.go` is the authoritative reference for every field and its default.

bunk-gui reads `~/.config/bunk-gui/config.toml` (`-config`): `tabs`,
`rail_open`, `font`, `font_size`, `text_zoom`, `[[agents]]` and `[keys]`. `[keys]` uses
bunk's action names and key syntax where they overlap; "none" sends the key
to the terminal. The file is watched and reloads live, and a bad value shows
a toast and keeps the default. The shortcuts window (Ctrl+Shift+?) and the
preferences window (Ctrl+,) rewrite only the lines they change
(`cmd/bunk-gui/keysfile.go`). `cmd/bunk-gui/bindings.go` lists every action and
its default.

---

## Design Decisions

- **Keyboard passthrough is per pane.** Ctrl+F12 (`[keys].passthrough`) toggles
  forwarding of all other keys through the normal terminal encoder. The toggle
  remains reserved and exits bunk search. Mouse handling is unchanged. The PASS
  badge shares the status layout, takes highest priority, and invalidates the
  render overlay when toggled.

- **PTY is one column narrower than the pane.** The rightmost column
  (`p.x + p.w - 1`) is reserved for the scrollbar; the PTY never writes there.
- **Cursor shape and cell content must be read under the same lock.** Both
  live inside `p.term`; `render()` must read them within the same `p.mu`
  acquisition so `readPTY` can't sneak in a write between the two. Guarded by
  `TestReadPTYSingleLock_ClaudeCursorRace`.
- **vt10x coordinates are display cells.** Wide characters occupy a lead cell
  (`Glyph.Width == 2`) and a continuation (`-1`); `-2` marks padding before a
  wide-character wrap. Padding is only ever the last cell of a wrapped row;
  `repairWideRow` turns padding that a shift or resize moves into a blank.
  Rendering, search, and selection skip continuations.
  Combining marks, variation selectors, and multi-codepoint emoji stay together
  in the lead cell through render, copy, search, and reflow. Mode 2027 can switch
  back to per-codepoint widths for legacy applications.
- **Query replies belong to the pane.** They are generated in stream order in
  both primary and alternate screens, including SSH/mosh panes; they are never
  delegated to the outer terminal. Unknown host colour defaults remain unknown.
  XTGETTCAP key replies use the normal input encoder with the current pane modes
  and Kitty flags; its terminal-name reply matches the TERM exported to panes.
- **No PTY write blocks a front end or holds the session lock.** Every write
  to a session's PTY goes through its writer (`internal/session/writer.go`).
  That covers query replies, graphics replies, keys, pastes, mouse and focus.
  - **Queue:** one FIFO per session. A goroutine starts when bytes are queued
    and exits when the queue is empty. `go` on a bound func allocates nothing.
  - **Order:** bytes leave in the order they were queued. Each reply and each
    input event is queued whole, so no reply lands inside a key or a paste.
  - **No lock:** `CaptureAndWrite` queues replies under the session lock. The
    writer goroutine writes them without it. `WriteInput` returns at once.
  - **Reply cap:** `ReplyBytes` (64 KiB) of unwritten replies. A reply that
    would pass it is dropped whole and counted in `WriteStats`.
  - **Input cap:** `Options.InputBytes`, default `DefaultInputBytes` (16 MiB).
    An event that would pass it is refused whole with `ErrInputFull`.
  - **Refusals:** bunk flashes "input not sent" on the pane. bunk-gui shows a
    toast, at most one per pane every 3 s; `-input-bytes` sets its cap.
  - **Memory:** a drained queue keeps at most 4 KiB. A paste's buffer is freed.
  - **Close and exit:** closing the PTY drops the queue and ends a blocked
    write. Close does it, and ReadLoop does it when the child exits.
  - **Host pastes in the TUI:** tcell delivers them as key events. bunk
    collects them between the paste markers and queues one event. A paste
    without an end marker is sent after `pasteTimeout` (1 s).
  - **Tests:** `WriteStats` reports queued, dropped and refused bytes.
    `WaitWritten` waits for an empty queue before a test reads the PTY.
- **Graphics are ordinary cells.** Static SIXEL, Kitty, and iTerm2 images become
  half-block glyphs with immutable `Glyph.Image` samples. They clip, erase, scroll,
  and reflow with the grid; source bitmaps are not retained by scrollback. No image
  escape is forwarded to the host. Kitty file/shared-memory access is rejected.
  PTYs and CSI 14/16/18 replies use the session's cell pixels: virtual in bunk's
  TUI (8 wide, height from cell aspect); real device pixels in bunk-gui, which
  sets `Options.CellPixels` and calls `Session.SetCellPixels` on zoom or scale.
  Transfers are capped at 8 MiB, decoded images at 4 million pixels / 4096 per
  axis, and Kitty caches at 32 MiB / 32 images. Reflow retains cell samples
  without replaying image transfers.
- **Image bitmaps live in a per-session store; cells name them by ID.**
  `Options.ImageBytes` (bunk-gui: 64 MiB per pane, `-image-bytes`) creates a
  `graphics.Store`; bunk's TUI keeps none. A cell's `ImageCell` holds
  `Placement.Bitmap` (store ID) and its own `Col`/`Row` in the placement, in
  the side table, so references survive scrolling, scrollback and reflow.
  The store counts each bitmap once, sweeps placements no cell refers to
  (`MarkImages` over both screens and the scrollback) after Kitty deletion,
  ED 3, RIS, reflow, every 64 additions and before evicting, then evicts the
  least recently drawn. Cells without a bitmap draw their samples.
  `reflow_test.go` checks every image cell's reference after each reflow.
- **bunk-gui draws image cells as textured quads** (`cmd/bunk-gui/images.go`):
  a texture per placement, uploaded on first draw; texelFetch when the cell
  size matches the placement's, linear filtering otherwise. Textures of
  dropped placements are freed; textures not on screen are freed oldest
  first past `-image-gpu-bytes` (256 MiB per pane). Links (`links.go`):
  Ctrl+hover underlines OSC 8 links and plain URLs, Ctrl+click opens through
  `gtk.UriLauncher` behind `openURL`, Ctrl+Shift+click while the application
  reports the mouse. `launchRefused` keeps the launcher off the test
  compositor; window tests read the links from `-report` (`opened`).
- **Hyperlink IDs are swept and reused.** A cell's `Glyph.Link` is a uint16
  ID into its terminal's URL table. A sweep frees the IDs no cell of either
  screen, cursor attribute or kept row refers to; new URLs reuse them, lowest
  first. Sweeps run when the table reaches 4,096 URLs or twice what the last
  sweep kept, and after ED 3 and RIS. A cell never changes its ID, so it keeps
  its URL. Rows kept outside the terminal must be marked through
  `WithLinkReferences` (the session marks its scrollback ring); a terminal
  with a scroll callback but no marker never frees IDs. When live links alone
  exceed 65,535 IDs or 16 MiB, new links lose their URL until rows holding
  links leave; the sweep then retries once per 1,024 new URLs. A front end
  that resolves IDs from rows it copied earlier compares `LinkGen` first, as
  bunk-gui's Ctrl+click does; the TUI resolves them under the session lock.
- **Primary-screen reflow uses stored cells.** Soft-wrap markers join physical
  rows into logical lines; explicit line breaks remain separate. Cursor and
  scroll anchors follow their cells. Styles, links, and image samples survive
  reflow, and output storage is bounded by scrollback capacity plus pane height.
  Width changes clear selections whose coordinates are no longer valid.
  `vt10x.Rewrap` copies compact cells. A scrollback row that is a whole line
  and fits the new width moves without a copy; `Ring` caches that fit per
  row. Any reflow change must keep `FuzzReflowEquivalence` and the tests in
  `internal/session/reflow_test.go` green: they compare against the
  pre-rewrite implementation in `reflow_oracle_test.go`. Track speed with
  `go test ./internal/session -run '^$' -bench BenchmarkReflow`.
- **Cursor colour follows the active pane.** OSC 12 overrides and OSC 112 resets
  are emitted through tcell; shutdown restores the host cursor colour.
- **Synchronized updates are per pane.** Other panes continue repainting;
  an abandoned update is released after one second.
- **Kitty keyboard state belongs to its negotiating foreground process group.**
  Polling clears it only after that owner leaves the foreground.
- **Terminal cleanup runs synchronously in `main` after the event loop.**
  Background goroutines can't be relied on to finish their cleanup before
  the process exits.
- **Agent status is inferred unless the agent reports it.** `session.Status`
  names the foreground agent from `DefaultAgents` (add a CLI there, or via
  `Options.Agents`), counts output in the last 2 s as working, and treats
  OSC 9/99/777 notifications and BEL from an agent as "needs you". VTE's
  "Command completed" notices are ignored. Agent hooks can report state
  through `internal/notify` (`docs/agent-hooks.md`); `Session.Notify` takes
  priority over inference until `Acknowledge`, `clear` or a foreground
  change. Every change of the agent state runs `Hooks.StatusChanged` once:
  the reader, its idle timer, the 1 s process tick and `Notify` share one
  record of the last announced state. Without hooks, Claude Code only sends
  OSC notifications to terminals it recognises; set its
  `preferredNotifChannel` to `terminal_bell`.
- **The TUI shows agent status per pane.** `renderPane` reads
  `Session.Status` before taking the session lock; the badge and the
  needs-you/done separator colours use that copy (`Pane.agent`).
  `Hooks.StatusChanged` only wakes the render loop. Focus or typing calls
  `Acknowledge`; `next_needs_you` (Alt+A) jumps. bunk serves the notify
  socket like bunk-gui, and `bunk notify` runs the same CLI.
- **Nested sessions are refused.** `BUNK=1` is exported into every pane's
  environment; the binary exits at startup if it sees it set.
- **Closing a session ends it, whatever its child does.** `Session.Close`
  closes the PTY master and sends SIGHUP; a child still running after
  `HangupGrace` (1 s) is killed with its process group. On Linux the master
  is re-wrapped as a pollable file (`ptyfd_linux.go`), so Close ends
  ReadLoop even while a process the child left behind (`server &`) holds
  the PTY. Both front ends close a pane's session when its child exits
  (TUI: `removePane`). Resize the PTY with `session.Setsize`, not
  `pty.Setsize`: the latter reads the descriptor with `Fd`, which races
  with ReadLoop's Close and can hit a reused descriptor number
  (`TestResizeWhileOutputCloses`).

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
    Session: &session.Session{
        Term:            term,
        Cmd:             &exec.Cmd{}, // non-nil prevents Cwd() nil-deref in emitTitle
        ScrollbackLines: 100,
        Scrollback:      session.Ring{MaxLines: 100},
    },
    x: 0, y: 0, w: cols, h: rows,
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
    screen:  scr,
    root:    &Node{pane: p},
    active:  p,
    oscBuf:  newOSCBuffer(),
    theme:   testTheme(),
    hostOut: &buf, // what render sends to the host; nil means os.Stdout
}
```

**Clipboard:** `TestMain` (`main_test.go`) replaces `writeNativeClipboard`,
so no test writes the desktop clipboard. `clipboardHas(text)` reports what
bunk tried to copy.

**A script in a real pane:** `runFeatureScript(t, "03_cursor_unicode.sh",
cols, rows)` returns a `scriptPane`. Use `waitPrompt`, `pressEnter`,
`paste`, `resizeHost`, `finish`, `render`, then `find`, `row` and `rows`.
