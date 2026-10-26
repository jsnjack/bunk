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
cmd_ssh.go          "bunk ssh": ssh that exports the pane's token (notify.RunSSH)
app.go              App struct, event loop, key handling, triggerRedraw()
input_modes.go      Host focus forwarding
input_tcell.go      tcell key/mouse events → internal/input (keyToBytesMode, mouseToBytes)
pane.go             Pane (embeds *session.Session): placement, render state, NewPane, Hooks
render.go           render(), renderPane(), vtColor(), border/scrollbar drawing
graphics.go         Image colour blending
layout.go           BSP tree: Node, split/remove/resize math
osc.go              Host passthrough buffer for OSC 7/52/133
mouse.go            Mouse events → PTY byte sequences
status.go           Draws the pane badges (rules in internal/badges) in theme
                    colours; agent-state separator colours
alerts.go           Agent notifications: desktop ones through D-Bus on the
                    user's desktop (internal/desktop), else OSC 777 to the
                    host terminal
relay.go            Relays the most urgent pane to the host terminal in-band
                    when bunk runs inside a bunk front end or over ssh
search.go           In-pane search UI (matching is session.FindAll)
config.go           TOML config, the theme in tcell colours, keybinding resolution
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
  timing.go         Timing: per-session timer intervals (Options.Timing)
  ptyfd.go          Setsize: window-size ioctl that holds the PTY descriptor
  ptyfd_linux.go    Pollable PTY master, so Close ends ReadLoop
  writer.go         PTY writer: one queue for query replies and input,
                    written by one goroutine; caps, drops and refusals

internal/badges/    Pane status badges for both front ends: which show, their
                    order, text and fitting (Layout); badgetest holds the cases
                    both front ends' parity tests draw

internal/theme/     Colour themes for both front ends: the built-in themes,
                    custom [themes.NAME] tables, [ui] overrides, and the
                    colours of badge roles (BadgeInks) and agent states

internal/notify/    Agent hook reports: Unix socket server (token → Session),
                    client, `notify` CLI (RunCLI), hook snippets per agent,
                    the in-band writer without a socket, `ssh` (RunSSH)
  wire/             The report line, its OSC 1337 in-band form, tmux/screen
                    wrapping; imported by notify and session alike

internal/alert/     Agent notifications for both front ends: when a state
                    change notifies (Tracker), the text, [notifications]
                    settings, the sanitised OSC 777 sequence and desktop text

internal/desktop/   Desktop notifications for bunk's TUI through the gdbus
                    command: session bus choice (SessionBus), one per pane,
                    replace and withdraw off the render loop (Notifier);
                    desktoptest holds the fake gdbus tests use

internal/leakcheck/ Test helper: fds, goroutines, child processes and live heap
                    of the process, compared with a baseline after a settle wait

internal/ptychild/  Test helper: a raw-mode child that floods numbered DECRQM
                    queries and reads its input only when the test says so

third_party/tcell/  Pinned tcell v2.13.9 with overline/keypad/keycap-width patches
                    (local go.mod replacement; see BUNK_PATCHES.md)

cmd/bunk-gui/       GTK4 GUI front end (own Go module; cgo; not built by make check)
  main.go           Entry point: calls Execute(); Version
  cmd.go            cobra root command: flags, logging, notify socket, runApp()
  cmd_notify.go     "bunk-gui notify": the agent hook CLI (notify.RunCLI)
  cmd_ssh.go        "bunk-gui ssh": ssh that exports the tab's token (notify.RunSSH)
  logger.go         slog setup for --debug and --trace; GLib messages arrive through slog
  app.go            App struct, window activation, test-run hooks and statistics
  themes.go         The theme shown: terminal colours, the CSS of the content
                    background, dividers and badges; repaints on a change
  prefs_theme.go    The Theme section of Preferences: a choice per theme with
                    a preview of its colours
  alerts.go         Desktop notifications per pane: when (internal/alert), the
                    show-pane action, --report records, preferences section
  desknotify.go     org.freedesktop.Notifications client: replace, withdraw,
                    clicks with their activation token
packaging/          Desktop entry, icon, metainfo; fedora/ holds the RPM spec

assets/             TUI demo gif; bunk-gui screenshots (regenerate with make screenshots)
docs/               agent-hooks.md: hook recipes and the notify protocol;
                    remote-agent-status.md: agents on hosts reached over ssh
scripts/            Terminal feature scripts (tested by terminal_features_test.go);
                    headless-gui.sh runs GUI tests; screenshots.sh renders assets/;
                    benchcheck/ runs and compares benchmarks (make bench-check)
testdata/           bench-baseline.txt: the timings bench-check compares with
```

The vendored vt10x has these local extensions:
- It supports SGR 2/8/9/21/53/58 and 4:N underline styles.
- `Cursor.Shape` stores the shape set by DECSCUSR.
- `ModeSetPaste` tracks DECSET 2004, and `ModeSync` tracks DECSET 2026.
- `QueryPrivateMode(n)` returns the DECRQM status byte for any tracked
  private mode.
- A private-parameter SGR guard stops `\x1b[?4m` from misfiring as SGR 4.
- Cells use display-cell widths and wide-glyph continuations. `ReplaceScreen`
  preserves terminal modes, attributes, callbacks, and hyperlink identities
  during reflow.
- It supports DEC 12 cursor blink and DEC 2027 grapheme widths (enabled by
  default). `Glyph.Combining` stores an immutable grapheme suffix, bounded
  to 1 KiB/cell.
  With DECRST 2027, a zero-width code point (VS16, keycap) joins a cell
  without widening it; `renderPane` then paints only the base character.
- In `Color`, values 0-255 are palette indexes. Truecolour comes from
  `vt10x.RGB` and carries bit 24, so `38;2;0;0;255` never equals palette 255.
  Decode with `c < 256` (palette), then `c < DefaultFG` (RGB in the low 24
  bits). The `Default*` values start at `1<<25`.
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

1. At startup, `main()` → `Execute()` → `run()` loads config, initialises
   slog, queries cell aspect, probes host OSC colours, initialises tcell,
   spawns the first `Pane` and enters `App.eventLoop()`.
2. For pane I/O, `Session.Run` starts three goroutines per pane: `ReadLoop`
   (PTY → vt10x), `waitForExit` and `trackFgProcess`. bunk attaches through
   `session.Hooks`: `Applied` runs under the session lock after each chunk,
   and `Output` runs after it to pace repaints. One shared `renderLoop`
   drains `app.redraw` (buffered 1). Replies and input reach the PTY through
   the session's writer (`writer.go`); its goroutine runs while bytes are
   queued.
3. On resize, `App.handleResize()` coalesces host resizes and updates the
   BSP tree. Emulator reflow finishes before the PTY receives its new size
   and SIGWINCH; both are deferred together during resize bursts.
   `Session.ResizeAndReflow()` joins soft-wrapped rows from scrollback and
   the live grid, repacks their cells at the new width, and preserves
   terminal state.
4. For OSC passthrough, bounded `ptyStream` framing runs before `osc.go`
   scanning. OSC 7/52/133 reach the host through `app.oscBuf`; OSC 8 links
   are stored on glyphs and emitted with their rendered text.

Locks are taken in a fixed order: `app.mu` before the session lock
(`p.Lock()`, the mutex embedded in `session.Session`). Never acquire
`app.mu` while holding a session lock.

---

## Build & Run

```bash
make check    # full validation gate (fmt → vet → race tests → build → lint)
make test     # tests only (race-enabled)
make build    # bin/bunk_{linux,darwin}_{amd64,arm64} after the core tests, bin/bunk-gui after the window tests
make gui      # bin/bunk-gui only, without tests (quick rebuild while iterating)
make run      # builds local binary and opens a debug session with --trace
make gui-test # bunk-gui: vet, race tests, packaging validation
make check-all # fmt+vet, then core and GUI tests in parallel, then build+lint
make check-all FRESH=1 # the same, uncached and goimports on every file
make rpm      # Fedora RPM of bunk-gui in bin/rpmbuild, checked by packaging/fedora/check-rpm.sh
make fuzz     # every Fuzz target in turn, FUZZTIME each (default 30s); not in check-all
make soak     # leak and soak tests at full length (SOAK=1); not in check-all
make bench-check    # key benchmarks pinned to one core; fails on a >10% slowdown
make bench-baseline # records testdata/bench-baseline.txt for bench-check
```

Every binary target stamps `main.Version` through `-ldflags` with the
version monova reports, or 0.0.0 without monova; `--version` prints it. The
RPM spec stamps the package version the same way.

Leak and soak tests check that sessions, panes and the notify socket release
everything they take. Each takes a `leakcheck.Snapshot`, runs a workload and
calls `leakcheck.Check`, which waits for the counts to settle and lists new
descriptors and child processes and the goroutine stacks on failure.
`TestCheckCatchesLeaks` injects each kind of leak to prove the detectors.
bunk-gui's `mark` action records the same counts plus live panes and their
GL textures in `--report`.

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
A helper child that is the test binary itself sets
`GORACE=atexit_sleep_ms=0`; otherwise a -race build sleeps 1 s at exit.

Run `make bench-check` before merging a performance-sensitive change. It
compares the key benchmarks with `testdata/bench-baseline.txt`. Neither
bench target is part of `check` or `check-all`, because timings depend on load.
It also runs bunk-gui's `TestGUIFrame` with `BUNKGUI_LATENCY_P99=1`, which
adds the p99 bound on key-to-render latency; check-all keeps only the p50
bound, because one stalled key under load decides p99.
- `scripts/benchcheck` builds each test binary once. It runs every vt10x
  benchmark, session `BenchmarkOutput` and `BenchmarkReflow`, and bunk-gui
  `BenchmarkFullScreenFrame`. Write and Output include the `termcorpus`
  streams. A run takes about 4 minutes at the default count.
- Rounds are interleaved: each of the `BENCH_COUNT` rounds (10) runs every
  suite once, so a burst of load hits all benchmarks alike.
- Every run is pinned with `taskset -c $(BENCH_CPU)` (CPU 0, a fast Zen 5
  core) and `GOMAXPROCS=1`. The benchmarked paths are single-threaded, so
  garbage collection runs on the measured core.
- A disturbed run is repeated up to twice, and the least disturbed attempt
  is kept. Any of three signs marks a run as disturbed. CPU time under 90%
  of wall time means a shared core. A clock under 80% of the top clock means
  a power limit or heavy load. An SMT sibling (CPU 12) over 50% busy shares
  the core's caches.
- The verdict compares medians of `cpu-ns/op` where both files have it
  (reflow), and of `ns/op` otherwise. A benchmark fails only when two
  conditions hold together: a Mann-Whitney U test gives p < 0.05, and the
  median grew by over `BENCH_THRESHOLD` percent (10).
- New or missing benchmarks, faster results and warnings are not failures.
- These conditions give a warning: a load average above `BENCH_MAX_LOAD`
  (4; the benchmark adds 1); disturbed runs that were kept; a different Go
  version, CPU, kernel, power profile or core clock; changed suite settings;
  and an allocation-free benchmark that now allocates.
- Load on other cores lowers the pinned core's clock from 5.1 GHz: load 10
  gives about 4.6 GHz, and load 50 about 3.7 GHz. Compute-bound times grow
  by as much. Same-code runs under different load differ by up to about 10%.
  Rerun a failure that comes with a clock or load warning.
- `BENCH_SUITES=vt10x,session-output` runs a subset of the suites, and the
  rest show as missing. `BENCH_TIME` overrides every suite's benchtime,
  which changes results, so compare only runs with equal settings.
- The GUI frame benchmark runs with `-test.short`, without a display, into a
  fake rasterizer. When bunk-gui does not build, it is skipped with a note.
  Its TestMain sets `runtime.MemProfileRate = 1`, which slows allocating code.
- After an intended speed change, run `make bench-baseline` on a quiet
  machine to record a new baseline. Commit `testdata/bench-baseline.txt`
  with the change. Its header records Go, CPU, kernel, power settings,
  commit, date and load.
- `go run ./scripts/benchcheck compare BASE CUR` exits 1 on a regression and
  2 on an error. `bin/bench-current.txt` keeps the last run.

Fuzz targets cover every parser of program output and user input:
`internal/vt10x` (FuzzParse, FuzzResize), `internal/session` (FuzzPTYStream,
FuzzOSCScanner, FuzzStatusOSC, FuzzQueryReplies, FuzzXTGETTCAP,
FuzzFindAndSelect, FuzzPTYWriterOrder), `internal/input` (FuzzEncodeKey, FuzzEncodeMouse),
`internal/graphics` (FuzzGraphicsDecoder, FuzzStore) and `internal/alert`
(FuzzOSC777, program text in the notification sent to the host). Their seed corpora in
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
Scenarios drive bunk-gui with `--do` steps that run in sequence. A `wait`
step holds until the UI reaches a state (`cmd/bunk-gui/wait.go`): a field of
the `--report` snapshot, frames drawn, no frames for a while, or injected
input received. Use `sleep` only where a check measures time (idle frames,
typing latency, CSS fades in screenshots). At most `BUNK_GUI_PARALLEL`
scenarios run at once (default 8); those with a higher `cost` start first.
Desktop notifications are recorded in `--report` (`notifications`) and
never reach a notification server on the test compositor. `decoy open`
gives the keyboard focus to a second window, so the main window is
inactive, and `alert-click ID` clicks the notification of the pane with
that ID as the server would. `make gui-test` builds with `-race -gcflags=all=-d=checkptr=0`,
as the window tests' bunk-gui does, because gotk4's GLib callbacks trip
checkptr.

Run `make check-all` before reporting any work as done. Work is done only
when the full suite is green; report its result, including failures.
Run `make check-all FRESH=1` before a merge or commit, because the plain form
reuses Go's test cache for unchanged packages and runs goimports only on
files changed since HEAD (gofmt still sees every file).

While working, iterate with the tests of the package you change
(`go test -race ./internal/session -run TestX`) rather than the gate, and
run check-all once when done. Several agents running check-all at once slow
everything by ~2x, so run at most three in parallel.

Tests run with `-race` and are required to pass before any binary is built.
`make test` includes the locally patched tcell module and its terminfo tests.
Targets are serialized even with `make -j`; check-all runs the two test
suites as background jobs itself and builds only after both pass.

The terminal feature scripts are `terminal_features.sh` and the files in
`scripts/terminal_features/`.
- All seven groups are automated. `terminal_features_test.go` has one
  `TestTerminalFeatures*` test per group, and `make test` runs them in under
  1 s.
- Each test runs the real script with bash on a PTY in a bunk pane
  (`features_harness_test.go`). It uses bunk's hooks, key handling, paste
  and render path, answers each pause, and checks the simulation screen,
  cursor, printed replies and host output (`App.hostOut`).
- Change a script and its test together. The tests read 06's assertion
  names and 05's "Bunk typically reports" lines from the scripts.
- Run a group by hand only to judge the host terminal: fonts, emoji shaping,
  visible underline styles, clicking links and the real clipboard.
  `TERMINAL_FEATURES_AUTO=1` skips the pauses, and 04 writes the clipboard.

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

bunk reads `~/.config/bunk/config.toml`; `--config` overrides the path.
Generate a documented default with `bunk config init`.
Malformed/unreadable files and missing explicit `--config` paths fail at startup;
a missing default file uses built-in defaults.

These are the key fields agents may need to know about:
- `theme` is a built-in name (`terminal`, `default`, `solarized-dark`,
  `dracula`, `nord`) or the name of a custom theme. A `[themes.NAME]` table
  defines one with `background`, `foreground` and 16 `palette` colours;
  `cursor` and the border and scrollbar colours are optional
  (`internal/theme`, `Def`). An unknown name or an invalid table is logged
  at startup, and bunk uses the default theme.
- `scrollback` is the per-pane scrollback line cap (default 10 000).
- `log_file` is the destination for `--debug` / `--trace` output (default
  `/tmp/bunk.log`).
- `cell_aspect` is the fallback height/width ratio when the host terminal
  doesn't answer the pixel-size query.
- `[keys]` overrides split / zoom / quit / search / copy / paste /
  next_needs_you; "none" disables an action and sends its key to the pane.
- `[ui]` holds hex colour overrides for borders and scrollbar. An invalid
  value is logged, and the theme's colour stays.
- `[notifications]` turns notifications on or off (`enabled`) and picks
  the agent states that send one (`states`, default needs-you, done and
  failed). A state name bunk does not know fails at startup.

`config.go` is the authoritative reference for every field and its default.

bunk-gui reads `~/.config/bunk-gui/config.toml` (`--config`): `tabs`,
`rail_open`, `font`, `font_size`, `text_zoom`, `theme`, `[themes.NAME]`,
`[[agents]]`, `[notifications]` and `[keys]`. `theme` and `[themes.NAME]`
work as in bunk's config, and `--theme` overrides `theme` for one run.
`[notifications]` has the same keys as in bunk's config. `[keys]` uses
bunk's action names and key syntax where they overlap; "none" sends the key
to the terminal. The file is watched and reloads live, and a bad value shows
a toast and keeps the default. The shortcuts window (Ctrl+Shift+?) and the
preferences window (Ctrl+,) rewrite only the lines they change
(`cmd/bunk-gui/keysfile.go`). `cmd/bunk-gui/bindings.go` lists every action and
its default; Design Decisions says where the defaults come from.

---

## Design Decisions

- bunk-gui's default keys are the keys the user presses today in bunk's
  TUI inside Ptyxis. They are bunk's TUI defaults plus Ptyxis's shortcuts,
  with the user's own Ptyxis keys: F2 opens a tab and Ctrl+Left/Right
  switch tabs. A change to a default needs the user's agreement;
  `TestDefaultKeys` lists every action's keys.
  - Ctrl+C copies only while text is selected, and ends the selection, so
    the next Ctrl+C reaches the program; with nothing selected it sends ^C.
    Ctrl+Shift+C always copies. The rule follows `keyCombo.collides`: a copy
    key a terminal program may use falls through without a selection.
  - While the find bar has the keyboard, Ctrl+C and Ctrl+V edit its text,
    and Ctrl+N, Ctrl+P, Enter and Esc step through matches and close it.
    Elsewhere Ctrl+N, Ctrl+P, Enter and Esc reach the program.
  - Ctrl+Shift+PgUp/PgDn move the selected tab in `App.tabs`, which orders
    the rail, the bar, Alt+1…0 and the report's tabs.
  - The shortcuts window marks keys terminal programs use (Ctrl+C, Alt+A,
    F2 and others) with ⚠. Passthrough (Ctrl+F12) sends all of them to the
    program.

- A bunk-gui tab can hold several agents in split panes. Its label keeps
  the name, repo and location of the first pane with an agent
  (`Tab.agentPane`), so it does not change as the agents change state.
  "+N" counts the agents in its other panes. The dot, colour and needs-you
  highlight show the most urgent state among them (`stateOrder`), and the
  rail shows that agent's notice as "agent: notice". The tooltip lists
  every agent. Notifications are per pane, as in the TUI, and Alt+A
  focuses the pane that needs the user.

- Keyboard passthrough is per pane. Ctrl+F12 (`[keys].passthrough`) toggles
  forwarding of all other keys through the normal terminal encoder. The toggle
  remains reserved and exits bunk search. Mouse handling is unchanged. The PASS
  badge shares the status layout, takes highest priority, and invalidates the
  render overlay when toggled.

- Pane badges come from `internal/badges`, for both front ends.
  - `badges.Layout` decides which badges show, their order, text and fitting
    from plain pane state. Each badge has a `Role`.
  - The front ends only paint. `theme.BadgeInks` names each role's colours:
    palette slots, most backgrounds blended `theme.BadgeDim` (40%) toward
    the theme background, and fixed agent colours. The TUI resolves them in
    tcell colours (`badgeStyle`, status.go), and bunk-gui through
    `Theme.Badge` (`badgeLook`, cmd/bunk-gui/badge.go).
  - Add a badge in `internal/badges`, never in one front end, and add a case
    to `badgetest.Cases`. `TestBadgeParityTUI` and `TestBadgeParityGUI` both
    draw every case, so the front ends cannot drift apart.
  - bunk-gui draws the badges as GtkInscriptions in the terminal font, over
    the pane's texture. A badge change draws no pane frame. The row syncs
    after each frame and on status, passthrough, zoom, find and flash events.
  - The find bar keeps the top right, and the badges sit right below it while
    it is open. A badge that appears while a query is typed never moves the
    bar.

- Themes come from `internal/theme`, for both front ends.
  - `theme.NewRegistry` holds the built-in themes and a config's
    `[themes.NAME]` tables, and `Registry.Select` picks one and applies
    `[ui]` overrides. Problems come back as a `*theme.Error`, and the theme
    falls back to the default one. The TUI logs them; bunk-gui shows the
    settings toast.
  - A `theme.Color` is 0xRRGGBB with a set bit. Its zero value, `Unset`,
    leaves the colour to the host terminal; the `terminal` theme leaves
    every colour unset, and the TUI probes the host's colours.
  - bunk-gui has no host terminal, so it shows every theme
    `Filled` from the default theme. There, `terminal` looks like
    `default`, the colours bunk-gui had before themes existed. bunk-gui
    forces libadwaita's dark style, so a theme that followed the desktop's
    light or dark preference would always be dark.
  - In bunk-gui the theme sets the ANSI palette, the default foreground,
    background and cursor, the badge colours, the content background and
    the dividers between panes (`InactiveBorder`). The rail, the bar and
    the dialogs keep libadwaita's dark style. Programs' OSC 4/10/11/12
    colours stay in front of the theme.
  - A theme change in bunk-gui (Preferences, a config edit) restyles the
    CSS and repaints each pane once: `Pane.themeChanged` reloads the
    colours and marks every row dirty. An idle window then draws no frames.
  - `TestThemesMatchOldConfig` compares the TUI's colours with the code
    from before the move (`theme_oracle_test.go`) for every built-in theme.
    `TestThemeParityTUI` and `TestThemeParityGUI` check both front ends
    against `internal/theme`, so they show the same colours.
  - In the `terminal` theme, the TUI blends a dimmed badge background from
    unset colours as tcell's RGB of them, -1 per channel, which gives white.
    The oracle keeps this behaviour.

- The PTY is one column narrower than the pane. The rightmost column
  (`p.x + p.w - 1`) is reserved for the scrollbar; the PTY never writes there.
- Cursor shape and cell content must be read under the same lock. Both live
  inside `p.term`; `render()` must read them within the same `p.mu`
  acquisition so that `readPTY` cannot write between the two reads.
  `TestReadPTYSingleLock_ClaudeCursorRace` guards this.
- vt10x coordinates are display cells. Wide characters occupy a lead cell
  (`Glyph.Width == 2`) and a continuation (`-1`); `-2` marks padding before a
  wide-character wrap. Padding is only ever the last cell of a wrapped row;
  `repairWideRow` turns padding that a shift or resize moves into a blank.
  Rendering, search, and selection skip continuations.
  Combining marks, variation selectors, and multi-codepoint emoji stay together
  in the lead cell through render, copy, search, and reflow. Mode 2027 can switch
  back to per-codepoint widths for legacy applications.
- Query replies belong to the pane. They are generated in stream order in
  both primary and alternate screens, including SSH/mosh panes; they are never
  delegated to the outer terminal. Unknown host colour defaults remain unknown.
  XTGETTCAP key replies use the normal input encoder with the current pane modes
  and Kitty flags; its terminal-name reply matches the TERM exported to panes.
- No PTY write blocks a front end or holds the session lock. Every write
  to a session's PTY goes through its writer (`internal/session/writer.go`).
  That covers query replies, graphics replies, keys, pastes, mouse and focus.
  - Each session has one FIFO queue. A goroutine starts when bytes are
    queued and exits when the queue is empty. `go` on a bound func
    allocates nothing.
  - Bytes leave in the order they were queued. Each reply and each input
    event is queued whole, so no reply lands inside a key or a paste.
  - `CaptureAndWrite` queues replies under the session lock, and the writer
    goroutine writes them without it. `WriteInput` returns at once.
  - Unwritten replies are capped at `ReplyBytes` (64 KiB). A reply that
    would pass the cap is dropped whole and counted in `WriteStats`.
  - Queued input is capped at `Options.InputBytes`, which defaults to
    `DefaultInputBytes` (16 MiB). An event that would pass the cap is
    refused whole with `ErrInputFull`.
  - When input is refused, bunk flashes "input not sent" on the pane.
    bunk-gui shows a toast, at most one per pane every 3 s; `--input-bytes`
    sets its cap.
  - A drained queue keeps at most 4 KiB, and a paste's buffer is freed.
  - Closing the PTY drops the queue and ends a blocked write. Close does
    this, and ReadLoop does it when the child exits.
  - In the TUI, tcell delivers host pastes as key events. bunk collects
    them between the paste markers and queues one event. A paste without an
    end marker is sent after `pasteTimeout` (1 s).
  - For tests, `WriteStats` reports queued, dropped and refused bytes, and
    `WaitWritten` waits for an empty queue before a test reads the PTY.
- Graphics are ordinary cells. Static SIXEL, Kitty, and iTerm2 images become
  half-block glyphs with immutable `Glyph.Image` samples. They clip, erase, scroll,
  and reflow with the grid; source bitmaps are not retained by scrollback. No image
  escape is forwarded to the host. Kitty file/shared-memory access is rejected.
  PTYs and CSI 14/16/18 replies use the session's cell pixels: virtual in bunk's
  TUI (8 wide, height from cell aspect); real device pixels in bunk-gui, which
  sets `Options.CellPixels` and calls `Session.SetCellPixels` on zoom or scale.
  Transfers are capped at 8 MiB, decoded images at 4 million pixels / 4096 per
  axis, and Kitty caches at 32 MiB / 32 images. Reflow retains cell samples
  without replaying image transfers.
- Image bitmaps live in a per-session store, and cells name them by ID.
  `Options.ImageBytes` (bunk-gui: 64 MiB per pane, `--image-bytes`) creates a
  `graphics.Store`; bunk's TUI keeps none. A cell's `ImageCell` holds
  `Placement.Bitmap` (store ID) and its own `Col`/`Row` in the placement, in
  the side table, so references survive scrolling, scrollback and reflow.
  The store counts each bitmap once, sweeps placements no cell refers to
  (`MarkImages` over both screens and the scrollback) after Kitty deletion,
  ED 3, RIS, reflow, every 64 additions and before evicting, then evicts the
  least recently drawn. Cells without a bitmap draw their samples.
  `reflow_test.go` checks every image cell's reference after each reflow.
- bunk-gui draws image cells as textured quads (`cmd/bunk-gui/images.go`).
  Each placement gets a texture, uploaded on first draw. Drawing uses
  texelFetch when the cell size matches the placement's and linear
  filtering otherwise. Textures of dropped placements are freed; textures
  not on screen are freed oldest first past `--image-gpu-bytes` (256 MiB per
  pane). Links are handled in `links.go`. Ctrl+hover underlines OSC 8 links
  and plain URLs, and Ctrl+click opens them through `gtk.UriLauncher` behind
  `openURL`; Ctrl+Shift+click does the same while the application reports
  the mouse. `launchRefused` keeps the launcher off the test compositor;
  window tests read the links from `--report` (`opened`).
- Hyperlink IDs are swept and reused. A cell's `Glyph.Link` is a uint16
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
- Primary-screen reflow uses stored cells. Soft-wrap markers join physical
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
- Cursor colour follows the active pane. OSC 12 overrides and OSC 112 resets
  are emitted through tcell; shutdown restores the host cursor colour.
- Synchronized updates are per pane. Other panes continue repainting;
  an abandoned update is released after one second.
- Kitty keyboard state belongs to its negotiating foreground process group.
  Polling clears it only after that owner leaves the foreground.
- Terminal cleanup runs synchronously in `main` after the event loop,
  because background goroutines can't be relied on to finish their cleanup
  before the process exits.
- Agent status is inferred unless the agent reports it. `session.Status`
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
- Agents on hosts reached over ssh report in-band
  (`docs/remote-agent-status.md`). Without a reachable socket, `bunk notify`
  writes the report line as `OSC 1337 ; SetUserVar=bunk_notify=<base64>` to
  the agent's terminal (`/dev/tty`, else the nearest parent's), wrapped for
  tmux or screen. The snippets run when `BUNK_NOTIFY_SESSION` or
  `SSH_CONNECTION` is set.
  - The session accepts an in-band report whose token
    `Options.KnownToken` knows (`notify.Server.Issued`) in any pane, and any
    other while `ssh`, `mosh` or `mosh-client` is the foreground process.
  - A report's `agent`, `host`, `repo`, `branch` and `cwd` replace the local
    ssh process's values in `Status` (`Status.Host` is new) until `clear` or
    a foreground change; acknowledging keeps them.
  - While ssh or mosh runs, OSC 9/99/777 mean needs-you without a known
    agent; bells from there never count.
  - bunk's TUI relays its most urgent pane (`relay.go`) when it inherited a
    token or runs over ssh, and sends `clear` on exit. `bunk ssh` exports the
    pane's token with a remote command.
- The TUI shows agent status per pane. `renderPane` reads
  `Session.Status` before taking the session lock; the badge and the
  needs-you/done separator colours use that copy (`Pane.agent`).
  `Hooks.StatusChanged` only wakes the render loop. Focus or typing calls
  `Acknowledge`; `next_needs_you` (Alt+A) jumps. bunk serves the notify
  socket like bunk-gui, and `bunk notify` runs the same CLI.
- An agent notifies the user when nobody is looking at it.
  `internal/alert`'s Tracker decides for both front ends, from the status
  after every `Hooks.StatusChanged`, so inferred states, hook reports and
  any other source of a status change notify alike.
  - A source alerts when its agent enters a `[notifications]` state, or
    its notice changes there, while it is not watched. In bunk-gui a pane
    is watched while the window is active and the pane is the focused pane
    of the selected tab; in bunk a pane is watched while it is active and
    the host terminal has the focus (DECSET 1004).
  - The title names the agent and its host ("claude on gpu01 needs you"):
    `Status.Host` from a remote report, else `SSHHost`. The body is the notice, or a default for the state,
    and "repo ⎇ branch".
  - Watching the source, the state leaving those states, turning
    notifications off and the source going away withdraw the notification.
    The same state notifies again only 10 s (`alert.DefaultRepeat`) after
    the last one unless the user watched the source; until then it is held,
    and `Tracker.Next` says when to look again.
  - bunk-gui keeps one notification per pane on org.freedesktop.Notifications
    (`desknotify.go`), keyed by the pane's ID. The user watches a pane while
    it is the focused pane of the selected tab in the active window, as in
    bunk. A click runs `app.show-pane` with that ID: the window takes the
    focus with the shell's activation token, selects the pane's tab and
    focuses the pane.
    GNotification is not used: on GNOME, GLib sends it through
    org.gtk.Notifications, where GNOME Shell needs an installed desktop file
    and runs the action through the application's bus name, which a
    non-unique application never owns; GLib's freedesktop backend drops the
    activation token. A missing server only logs at debug level.
  - The window is not marked as needing attention. GTK 4 has no urgency
    hint, and on Wayland a present without a token makes GNOME Shell post a
    second "bunk-gui is ready" notification.
  - bunk decides in the render pass, which reads every pane's status under
    `app.mu`, and each alert takes one of two ways, never both.
  - On the user's desktop, bunk keeps one desktop notification per pane on
    org.freedesktop.Notifications, with the same replace and withdraw rules
    as bunk-gui. It uses them when the session bus is reachable
    (`DBUS_SESSION_BUS_ADDRESS`, else the socket `$XDG_RUNTIME_DIR/bus`),
    `SSH_CONNECTION` is unset and `gdbus` is on PATH (`desktop.SessionBus`,
    `startDesktopAlerts`). `desktop.Notifier` runs `gdbus call` with typed
    GVariant text, on one goroutine at a time, with at most one waiting
    request per pane; gdbus's output goes to buffers, never the terminal.
    Quitting withdraws the notifications, waiting up to 2 s.
  - The desktop notifications have no actions. Receiving a click would need
    a process listening on the bus for as long as the notification lives,
    and bunk cannot raise its host terminal's window anyway.
  - A failed Notify call logs at debug level and sends that alert as OSC 777
    through `app.oscBuf` with the next frame. A call that gets no answer in
    3 s does not fall back, because the server may have shown it.
  - Otherwise, over SSH or without a bus or gdbus, bunk writes
    `alert.OSC777` to the host. Control and bidi characters go, ';' becomes
    ',', and the title and body are cut to 120 and 360 bytes. foot,
    WezTerm, Ghostty and urxvt show it; VTE 0.84 (GNOME Terminal, Ptyxis on
    Fedora 44) reads OSC 777 notify only as "Command completed".
  - Both front ends clean desktop text with `alert.DesktopText`: the same
    character rules and limits, the body escaped as markup for GNOME Shell.
- Session timers are set per session. `Options.Timing` holds the
  foreground poll (`ProcessPoll`, 1 s), idle (`IdleAfter`, 2 s), hang-up
  grace (`HangupGrace`, 1 s), recently-left (2 s) and sync-release
  (`SyncUpdateTimeout`, 1 s) intervals. A zero field takes the default;
  both front ends leave it zero. Tests shorten it per session, never
  through package variables, so parallel tests cannot race on it.
- Nested sessions are refused. `BUNK=1` is exported into every pane's
  environment; the binary exits at startup if it sees it set.
- Closing a session ends it, whatever its child does. `Session.Close`
  closes the PTY master and sends SIGHUP; a child still running after
  `HangupGrace` (1 s) is killed with its process group. On Linux the master
  is re-wrapped as a pollable file (`ptyfd_linux.go`), so Close ends
  ReadLoop even while a process the child left behind (`server &`) holds
  the PTY. Both front ends close a pane's session when its child exits
  (the TUI in `removePane`). Resize the PTY with `session.Setsize`, not
  `pty.Setsize`: the latter reads the descriptor with `Fd`, which races
  with ReadLoop's Close and can hit a reused descriptor number
  (`TestResizeWhileOutputCloses`).

---

## Gotchas

- The TUI owns stderr, so do not write logs there. All diagnostic output goes
  through `slog` to the trace file (`/tmp/bunk.log`). Errors that the user
  needs to see surface through the UI.
- bunk-gui logs through `slog` as well. Warnings and errors go to stderr;
  `-d`/`--debug` adds debug detail there, and `--trace` writes every record
  to `/tmp/bunk-gui.log`, truncated on start. gotk4 forwards GLib and GTK
  messages to `slog.Default`, so they follow the same levels, and the window
  tests fail on any `level=WARN` or `level=ERROR` line on stderr.
- Both front ends log the notify socket as `Listening on <path>` whatever
  the log level: bunk-gui on stderr and in its trace log, bunk's TUI only in
  its log file.
- Only `-d` and `-c` have short forms (plus cobra's `-h`); every other flag
  is long-form. bunk-gui's test flags (`--do`, `--report`, `--verify`,
  `--wait-focus`, `--alloc-probe`, `--dump`, `--dump-size`,
  `--maximize-after`) are hidden from `--help`.
- Vendored vt10x has local patches. Prefer fixing bugs there over working
  around them in the main code.
- `TERMINAL_AUDIT.md` is the authoritative reference for which terminal
  features are implemented, partial, or missing. When investigating a
  rendering or input bug, consult it first; when fixing a feature, update it
  to reflect the new status.

---

## Test recipes

Build a minimal Pane without a PTY like this:
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

Set up a simulation screen like this:
```go
scr := tcell.NewSimulationScreen("UTF-8")
scr.Init()
defer scr.Fini()
scr.SetSize(w, h)
// scr.GetContent(x, y) → (mainc rune, combc []rune, style, width)
```

Build a minimal App for calling render() like this:
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

For short timers, session tests pass `fastTiming` (`helpers_test.go`) in
`Options.Timing`, and bunk's tests set `App.timing` or `paneSpec.timing`.
- A timer test loops over `timings(fast)` so that the defaults stay
  covered. That gives a `short` subtest, plus a `default` one unless
  `-short`, run in parallel. `TestStatusTiming` checks Idle and RecentlyLeft
  against their defaults without waiting.
- Wait for a state rather than a time. Poll `Status()`, a hook or the PTY
  until the state arrives, with a deadline of seconds.
- Independent slow tests call `t.Parallel()`. They start after every serial
  test of the package ends.
- Keep a test serial if it counts process-wide descriptors, goroutines,
  children or heap (`leakcheck`, `runtime.GC`), uses `testing.AllocsPerRun`,
  bounds a latency, swaps a package variable, or calls `t.Setenv`.

`TestMain` (`main_test.go`) replaces `writeNativeClipboard`, so no test
writes the desktop clipboard. `clipboardHas(text)` reports what bunk tried
to copy. `TestMain` also points `DBUS_SESSION_BUS_ADDRESS` at a socket that
does not exist and puts a `gdbus` that only fails first on PATH, so no test
reaches the session bus. Tests of desktop notifications use
`desktoptest.New`, a fake gdbus that records its calls; bunk-gui's
`TestTUIDesktopNotifierWithGdbus` runs the real gdbus against a fake server
on a private bus.

To run a script in a real pane, call
`runFeatureScript(t, "03_cursor_unicode.sh", cols, rows)`, which returns a
`scriptPane`. Use `waitPrompt`, `pressEnter`, `paste`, `resizeHost`,
`finish`, `render`, then `find`, `row` and `rows`.
