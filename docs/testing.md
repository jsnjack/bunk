# Testing, benchmarks and test recipes

The leak and soak tests, the benchmark gate, the fuzz targets, bunk-gui's
window tests and their scenarios, the terminal feature scripts, and recipes
for building panes, screens and apps in a test. Read it before writing a test
or changing one of these harnesses. The commands and the rules for running the
gate are in [AGENTS.md](../AGENTS.md#build--run).

The other files in docs/: [architecture.md](architecture.md) is the file
index, [terminal.md](terminal.md) covers the emulator, sessions and bunk's
TUI, [gui.md](gui.md) covers bunk-gui,
[badges-and-themes.md](badges-and-themes.md) the badges and themes both front
ends share, [agent-internals.md](agent-internals.md) agent status, hooks and
notifications in the code, and [testing.md](testing.md) the tests, benchmarks
and test recipes. [features.md](features.md),
[configuration.md](configuration.md), [agent-status.md](agent-status.md) and
[agent-hooks.md](agent-hooks.md) are the user guides,
[remote-agent-status.md](remote-agent-status.md) records how remote agent
status was designed, and [development.md](development.md) is the short tour of
the code.

## Leak and soak tests

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

## Benchmarks

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

## Fuzz targets

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

## Window tests

bunk-gui window tests run on a private headless mutter through
`scripts/headless-gui.sh`, never on the desktop. It gives one 2560x1600@120
virtual monitor at 133% on the GPU, with its own D-Bus and clipboard. Every
GUI check, including pixel, idle-frame, allocation and latency checks, lives
in the test suite; turn a manual check into a test before relying on it.
`go test -short` skips the window tests.
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

## Terminal feature scripts

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
  against their defaults without waiting, and `TestInferredDoneStretch`
  does the same for Done.
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
