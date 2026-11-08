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

## Where the details are

This file holds what every session needs: the package map, the rules that
cross packages, the commands, and the traps. The design of each area, with
the reasons behind it, is in `docs/`. Read the file for the area before
changing it:

| Working on | Read |
|---|---|
| a file you have not seen before; vt10x's local extensions | [docs/architecture.md](docs/architecture.md) |
| `internal/vt10x`, `internal/session`, `internal/graphics`, bunk's TUI (package main): parsing, cells, reflow, links, images, the PTY writer, closing a session | [docs/terminal.md](docs/terminal.md) |
| `cmd/bunk-gui`, `internal/restore`, `internal/boxdraw`: keys, pointer, windows, tabs and the rail, menus, session restore, drawing glyphs and images | [docs/gui.md](docs/gui.md) |
| `internal/badges`, `internal/theme`: the badges and colours both front ends share | [docs/badges-and-themes.md](docs/badges-and-themes.md) |
| agent detection, `internal/notify`, `internal/alert`, `internal/desktop`, notifications, installing hooks, agents over ssh | [docs/agent-internals.md](docs/agent-internals.md); the protocol in [docs/agent-hooks.md](docs/agent-hooks.md), the remote design in [docs/remote-agent-status.md](docs/remote-agent-status.md) |
| any test: leak and soak tests, benchmarks, fuzz targets, window-test scenarios, terminal feature scripts, test recipes | [docs/testing.md](docs/testing.md) |
| `config.go`, bunk-gui's settings and key bindings | [docs/configuration.md](docs/configuration.md#notes-for-working-on-the-code) |
| what users see: every feature and key, the agent states | [docs/features.md](docs/features.md), [docs/agent-status.md](docs/agent-status.md) |

This file stays under 30,000 bytes, and `make check` fails when it is over (AGENTS.universal.md, "AGENTS.md size"). When a change alters a flow, update that flow's file in `docs/`, not this one.

---

## Architecture

One line per package or directory; [docs/architecture.md](docs/architecture.md)
is the file index.

```
*.go (package main)  bunk's TUI: cobra commands (config, notify, ssh), the App and its event
                     loop, panes, BSP layout, rendering, badges, alerts, relay, search, config
internal/vt10x/      vendored VT100/xterm emulator (fork of hinshun/vt10x) with local extensions:
                     16-byte cells, graphemes, fast parse paths, rewrap, the OSC 8 link table
internal/graphics/   bounded SIXEL, Kitty and iTerm2 image decoders; the bitmap store
internal/termcorpus/ realistic output streams for the parser benchmarks
internal/input/      key, mouse, focus and paste encoding (legacy, application modes, Kitty CSI u)
internal/session/    one terminal session for both front ends: PTY, emulator, scrollback, reflow,
                     the PTY writer, OSC scanning, search, history, commands, agent status
internal/restore/    bunk-gui's saved windows: snapshots, slots, scrollback files, resume commands
internal/badges/     pane badges for both front ends (Layout); badgetest holds the parity cases
internal/theme/      colour themes for both front ends, [ui] overrides, badge and state colours
internal/boxdraw/    cell masks for box drawing, blocks, braille and powerline that bunk-gui draws
internal/notify/     agent hook reports: socket server, `notify` and `ssh` CLIs, hook snippets
                     and installing them; wire/ is the report line and its OSC 1337 form
internal/alert/      when an agent state or a long command notifies, for both front ends
internal/desktop/    bunk's TUI desktop notifications through gdbus; desktoptest is a fake gdbus
internal/leakcheck/  test helper: fds, goroutines, children and heap against a baseline
internal/ptychild/   test helper: a raw-mode child that floods DECRQM queries
third_party/tcell/   pinned tcell v2.13.9 with local patches (BUNK_PATCHES.md)
cmd/bunk-gui/        GTK4 front end: own Go module, cgo, OpenGL panes (gl/); make check skips it
packaging/           desktop entry, icon, metainfo; fedora/ holds the RPM spec
assets/              README pictures from TestGUIScreenshots (make screenshots)
docs/                user guides and the design notes in the table above
scripts/             terminal feature scripts, headless-gui.sh, screenshots.sh, benchcheck/
testdata/            bench-baseline.txt, the timings make bench-check compares with
```

---

## Key Flows

The core flows of bunk's TUI and the session both front ends share. Their
design decisions are in [docs/terminal.md](docs/terminal.md).

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

Two more areas run beside these:

- **bunk-gui** ([docs/gui.md](docs/gui.md)). Each window is its own GTK4
  process with its own notify socket. Each pane holds a `session.Session`,
  runs its child in a systemd scope of its own, and is drawn with OpenGL
  from a glyph atlas. The window saves its tabs, splits and scrollback for
  session restore.
- **Agent status** ([docs/agent-internals.md](docs/agent-internals.md)).
  `session.Status` infers each pane's agent state from the foreground
  process and its output. Hook reports through `internal/notify` (a Unix
  socket, or in-band over ssh) take priority. After every
  `Hooks.StatusChanged`, `internal/alert`'s Tracker decides for both front
  ends whether to notify.

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
make rpm      # Fedora RPM of a bunk-gui built with go build (cached), checked by packaging/fedora/check-rpm.sh
make release  # GitHub release v<version>: bunk tarballs after the core tests, the bunk-gui RPM after the window tests
make fuzz     # every Fuzz target in turn, FUZZTIME each (default 30s); not in check-all
make soak     # leak and soak tests at full length (SOAK=1); not in check-all
make bench-check    # key benchmarks pinned to one core; fails on a >10% slowdown
make bench-baseline # records testdata/bench-baseline.txt for bench-check
```

Every binary target stamps `main.Version` through `-ldflags` with the
version monova reports, or 0.0.0 without monova; `--version` prints it. The
RPM gets the same version as its package version.

Run `make check-all` before reporting any work as done. Work is done only
when the full suite is green; report its result, including failures.
Run `make check-all FRESH=1` before a merge or commit, because the plain form
reuses Go's test cache for unchanged packages and runs goimports only on
files changed since HEAD (gofmt still sees every file).

Claude Code, Codex and Copilot CLI run `make check-all` themselves when a
turn ends with changed code: `.claude/hooks/check-all-on-stop.sh` is their
Stop hook (`.claude/settings.json`, which Copilot CLI reads too, and
`.codex/hooks.json`). A failure sends the log tail back as the next prompt;
a green run stamps the tree in `.git/claude-check-all`.

While working, iterate with the tests of the package you change
(`go test -race ./internal/session -run TestX`) rather than the gate, and
run check-all once when done. Several agents running check-all at once slow
everything by ~2x, so run at most three in parallel.

Tests run with `-race` and are required to pass before any binary is built.
`make test` includes the locally patched tcell module and its terminfo tests.
Targets are serialized even with `make -j`; check-all runs the two test
suites as background jobs itself and builds only after both pass.

[docs/testing.md](docs/testing.md) has the leak and soak tests, the
benchmark gate (run `make bench-check` before merging a performance-sensitive
change), the fuzz targets (a failing one gets a named regression test), the
window-test scenarios, the terminal feature scripts (change a script and its
test together) and the test recipes.

---

## Configuration

bunk reads `~/.config/bunk/config.toml` (`bunk config init` writes a
documented one) and bunk-gui `~/.config/bunk-gui/config.toml`; `--config`
overrides either. A malformed or unreadable bunk file, or a missing
`--config` path, stops bunk at startup; a missing default file uses built-in
defaults. bunk-gui reloads its file live, and a bad value there shows a toast
and keeps the default. `config.go` is the reference for bunk's fields, and
`cmd/bunk-gui/bindings.go` for bunk-gui's actions and default keys. Details:
[docs/configuration.md](docs/configuration.md#notes-for-working-on-the-code).

---

## Design Decisions

One line each; the docs file named above each group has the full text.

bunk-gui ([docs/gui.md](docs/gui.md)):
- Default keys are the keys the user presses today in bunk's TUI inside
  Ptyxis. A change to a default needs the user's agreement;
  `TestDefaultKeys` lists every action's keys.
- Pointer, selection and focus handling matches bunk's TUI.
- A held divider shows the split's shares; a legacy event controller written
  in C sees the press, because gotk4 passes a GdkEvent to Go as a GObject.
- Each window is its own process with its own notify socket; New window
  starts bunk-gui again with an XDG activation token.
- Each pane runs in a systemd scope of its own (`scope.go`), so an OOM kill
  in a pane no longer closes bunk-gui with every pane.
- A tab with several agents keeps its first agent's label and shows the
  most urgent state; the collapsed rail draws each tab as a ring (or a map),
  and holding Alt alone labels the rings.
- Tabs can be named (`tabname.go`); tabs and panes have context menus
  (`ctxmenu.go`) whose popover hangs on the root box.
- Session restore keeps one slot per window in
  `$XDG_STATE_HOME/bunk-gui/windows` (files 0600, held by a lock) and offers
  the windows that were closed together.
- Image cells draw as textured quads. Box drawing and similar characters
  come from `internal/boxdraw`, not the font; glyphs wider than their cell
  are fitted; colour emoji sit on a colour page of the atlas.
- Panes get `TERM_PROGRAM=bunk-gui`, so shell start-up files can skip
  starting bunk's TUI there.

Both front ends ([docs/badges-and-themes.md](docs/badges-and-themes.md)):
- Add a badge in `internal/badges`, never in one front end, and add a case
  to `badgetest.Cases`; the parity tests draw every case in both.
- Themes come from `internal/theme`; an unset colour is left to the host
  terminal, and bunk-gui shows every theme filled from the default theme.

The terminal core ([docs/terminal.md](docs/terminal.md)):
- Keyboard passthrough (Ctrl+F12) is per pane.
- The PTY is one column narrower than the pane; the rightmost column is the
  scrollbar's.
- Cursor shape and cell content must be read under the same lock
  acquisition (`TestReadPTYSingleLock_ClaudeCursorRace`).
- vt10x coordinates are display cells: a wide character is a lead cell and
  a continuation, which rendering, search and selection skip.
- Query replies belong to the pane, in stream order, and are never
  delegated to the outer terminal.
- No PTY write blocks a front end or holds the session lock: every write
  goes through the session's writer, which caps replies and input.
- Graphics are ordinary cells, and no image escape reaches the host.
  bunk-gui keeps the bitmaps in a per-session store; bunk's TUI keeps none.
- Hyperlink IDs are swept and reused; rows kept outside the terminal must
  be marked through `WithLinkReferences`.
- Primary-screen reflow uses stored cells; a reflow change must keep
  `FuzzReflowEquivalence` and `internal/session/reflow_test.go` green.
- Cursor colour follows the active pane; synchronized updates are per pane;
  Kitty keyboard state belongs to the process group that negotiated it.
- Terminal cleanup runs synchronously in `main` after the event loop.
- Session timers are set per session (`Options.Timing`); tests shorten them
  per session, never through package variables.
- Nested sessions are refused: panes get `BUNK=1`.
- Closing a session ends it, whatever its child does: SIGHUP, then a kill of
  its process group after `HangupGrace`.

Agents ([docs/agent-internals.md](docs/agent-internals.md)):
- Agent status is inferred unless the agent reports it through hooks; a
  report wins until it is acknowledged or cleared, or the foreground
  changes.
- Users set up agent hooks with guidance, not behind their back: bunk
  writes an agent's configuration only when the user asks for it.
- Agents on hosts reached over ssh report in-band when no socket is
  reachable.
- The TUI reads `Session.Status` before taking the session lock.
- `internal/alert`'s Tracker decides when an agent or a long command
  notifies, for both front ends; bunk's TUI sends each alert as a desktop
  notification or as OSC 777, never both.

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
  `--maximize-after`, `--agent-home`) and the flags of windows it starts
  itself (`--restore`, `--offer-restore`) are hidden from `--help`.
- In bunk-gui, finish a D-Bus call with `callFinish` (`desknotify.go`), not
  gotk4's `DBusConnection.CallFinish`. gotk4 v0.4.1 sets a finalizer on the
  NULL reply of a failed call, and GLib logs a critical when the garbage
  collector runs it, which fails every window test. A test of a failed
  call runs `runFinalizers` before it checks the logs.
- Vendored vt10x has local patches. Prefer fixing bugs there over working
  around them in the main code.
- `TERMINAL_AUDIT.md` is the authoritative reference for which terminal
  features are implemented, partial, or missing. When investigating a
  rendering or input bug, consult it first; when fixing a feature, update it
  to reflect the new status.
- Tests never reach the user's desktop session:
  - bunk-gui window tests run on a private headless mutter through
    `scripts/headless-gui.sh`, never on the desktop. It gives one
    2560x1600@120 virtual monitor at 133% on the GPU, with its own D-Bus and
    clipboard. Run every GUI test through it, as `make gui-test`, `make
    soak` and `make bench-check` do, never with a bus of your own: the
    wrapper's bus can start no service, because a document portal started
    on a bus that shares the desktop's runtime directory unmounts the
    desktop's `doc` filesystem, and Flatpak apps then fail to start.
  - Desktop notifications are recorded in `--report` (`notifications`) and
    never reach a notification server on the test compositor. On the test
    compositor the window is recorded in `--report` (`windows`) and not
    started. `launchRefused` keeps the launcher off the test compositor;
    window tests read the links from `--report` (`opened`).
  - Window tests get their own state directory: the harness sets
    `XDG_STATE_HOME_TEST`, which `scripts/headless-gui.sh` turns into
    `XDG_STATE_HOME`. `TestGUIRestoreSession` saves, quits and restores
    with a fake `claude` on PATH that reports a session ID.
  - Window tests pass `--agent-home`, a directory with every agent's hooks
    installed, so no hooks banner comes between a scenario's steps; the
    hook tests pass an empty one or one with outdated hooks.
  - `TestMain` (`main_test.go`) replaces `writeNativeClipboard`, so no test
    writes the desktop clipboard. `clipboardHas(text)` reports what bunk tried
    to copy. `TestMain` also points `DBUS_SESSION_BUS_ADDRESS` at a socket that
    does not exist and puts a `gdbus` that only fails first on PATH, so no test
    reaches the session bus. Tests of desktop notifications use
    `desktoptest.New`, a fake gdbus that records its calls; bunk-gui's
    `TestTUIDesktopNotifierWithGdbus` runs the real gdbus against a fake server
    on a private bus.
- Resize the PTY with `session.Setsize`, not `pty.Setsize`: the latter reads
  the descriptor with `Fd`, which races with ReadLoop's Close and can hit a
  reused descriptor number (`TestResizeWhileOutputCloses`).
- Any change to vt10x's `parse`, `print`, `setChar`, the scan paths or the
  rune classes must keep the fast-path equivalence tests and the
  `FuzzParse`/`FuzzFastPaths` seeds green ([docs/architecture.md](docs/architecture.md#vt10xs-local-extensions)).
- A direct `cells[x]` write in vt10x's `row.go` must call `touch(x)` (or
  `touchAll` after shifting); write cells through `set`, `fill`,
  `writeASCII` or `setWrap` instead.

---

## Known Issues

- bunk-gui is Linux-only and builds only for the machine it runs on: cgo and
  GTK4 rule out cross builds (Makefile).
- bunk's OSC 777 fallback for alerts: foot, WezTerm, Ghostty and urxvt show
  it; VTE 0.84 (GNOME Terminal, Ptyxis on Fedora 44) reads OSC 777 notify
  only as "Command completed" (docs/agent-internals.md).
