# Development

The design notes listed in [AGENTS.md](../AGENTS.md#where-the-details-are)
hold the full architecture, design decisions and test recipes. This page is
the short version: how the code is laid out, how to build it and how it is
tested.

## Packages

Both front ends run on one Go core. `internal/session` owns the PTY, the
emulator, scrollback, reflow, search and agent status. Each front end adds
input and drawing.

```
   bunk (package main)                bunk-gui (cmd/bunk-gui)
   tcell, any terminal                GTK4 window, OpenGL panes, tabs
          │                                │
          └───────────────┬────────────────┘
                 ┌────────┴─────────────────────────┐
                 ▼                                  ▼
          internal/session  ◀──────────────  internal/notify
          PTY, scrollback, reflow,           hook socket, notify CLI
          search, agent status
             │            │
             ▼            ▼
     internal/vt10x   internal/input
     emulator         key and mouse encoding
             │
             ▼
     internal/graphics
     image decoders, bitmap store
```

| Path | What it does |
| :- | :- |
| `/` (package main) | bunk TUI: tcell screen, BSP layout, borders, badges with agent status, search bar, config, `bunk notify` |
| `cmd/bunk-gui/` | bunk-gui: window, tabs, rail and bar, OpenGL pane view, images, links, find, shortcuts, preferences. It is a separate Go module with cgo. |
| `cmd/bunk-gui/gl/` | OpenGL 3.3 binding, loaded at run time through EGL |
| `internal/session/` | One terminal session: PTY, emulator, scrollback, reflow, search, selection, query replies, agent status |
| `internal/vt10x/` | VT/xterm emulator, a fork of hinshun/vt10x: parser, 16-byte cells, graphemes, rewrap |
| `internal/input/` | Key, mouse, focus and paste encoding: legacy, application modes, Kitty CSI u |
| `internal/notify/` | Agent hook reports: Unix socket server, the `notify` and `ssh` commands both binaries run, hook snippets per agent, reports written to the terminal where no socket is reachable |
| `internal/notify/wire/` | The report format: one JSON line, over the socket or as an OSC 1337 user variable in the terminal |
| `internal/alert/` | Agent notifications: when a state change notifies, the text, and the OSC 777 sequence bunk sends |
| `internal/desktop/` | bunk's desktop notifications through `gdbus`: one per pane, replaced and withdrawn off the render loop |
| `internal/graphics/` | Bounded SIXEL, Kitty and iTerm2 image decoders; the bitmap store behind full-resolution drawing |
| `internal/badges/` | Pane badges of both front ends: which show, their order, text and fitting |
| `internal/theme/` | Colour themes of both front ends: built-in and custom themes, `[ui]` overrides, badge colours |
| `internal/restore/` | bunk-gui's saved windows: snapshots, slots, scrollback files, each agent's resume command |
| `internal/boxdraw/` | Cell masks for the box drawing, block, braille and powerline characters bunk-gui draws itself |
| `third_party/tcell/` | tcell v2.13.9 with overline, keypad and keycap-width patches |
| `scripts/` | `headless-gui.sh`, `screenshots.sh`, `benchcheck/`, terminal feature scripts |
| `packaging/` | Desktop entry, icon, AppStream metainfo; `fedora/` holds the RPM spec |
| `docs/` | User guides and design notes |

## Building

bunk-gui needs Linux, Go 1.26, gcc, GTK 4, libadwaita and OpenGL 3.3. On
Fedora:

```bash
sudo dnf install golang gcc gtk4-devel libadwaita-devel
make build          # runs the tests, then writes bin/bunk and bin/bunk-gui
bin/bunk-gui
```

`make gui` rebuilds only `bin/bunk-gui`, without tests. `make rpm` builds a
Fedora RPM in `bin/rpmbuild` from a bunk-gui that `go build` compiles with the
build cache, so a rebuild takes seconds. It runs no tests; `make check-all` is
the gate. `make release` attaches this RPM to the GitHub release.

## Testing

```bash
make check         # bunk and the core: fmt, vet, race tests, build, lint
make gui-test      # bunk-gui: vet, race tests on a headless compositor, packaging checks
make check-all     # both; run it before calling work done
make fuzz          # every Fuzz target for FUZZTIME each (default 30s); not in check-all
make soak          # long leak and soak runs (2000 sessions, 500 MiB of output); not in check-all
make bench-check   # pinned benchmarks vs testdata/bench-baseline.txt; fails on a >10% slowdown
make bench-baseline # record a new baseline after an intended speed change
make screenshots   # render the README pictures into assets/
make run           # build bunk and open it with --trace in a new terminal
```

- GUI tests run on a private mutter from `scripts/headless-gui.sh`, so no
  windows open on the desktop.
- The compositor has one 2560x1600 monitor at 133%, its own D-Bus and its own
  clipboard.
- Pixel, idle-frame, allocation and latency checks are Go tests. Turn a manual
  check into a test.
- check-all counts fds, goroutines, child processes and heap around session,
  TUI and GUI cycles to find leaks.
- `go test -short` skips window tests.
- Scenario steps wait for UI states rather than fixed times.
  `BUNK_GUI_PARALLEL` (default 8) sets how many scenarios run at once.
- Window tests need mutter, gdctl and dbus-run-session. Packaging checks need
  desktop-file-validate and appstreamcli.

## README pictures

Every picture in the README comes from `TestGUIScreenshots`, which runs in
every `make gui-test`, so the scenarios keep working. `make screenshots` runs
it with `BUNK_SCREENSHOTS` set and writes the pictures to `assets/`.

- `screenshots_test.go` shoots the hero, the switcher, the bar, an image,
  find and the shortcuts window.
- `screenshots_features_test.go` shoots the collapsed rail as rings and as
  maps, the frames of the states loop, a pane's badges, the hooks and restore
  banners, the Agent hooks preferences and bunk's TUI running in a pane.
- Crops are cut from window shots with the geometry the snapshot reports.
- The script shrinks the PNGs with pngquant, oxipng, optipng or ImageMagick,
  turns the frames into `gui-states.webp` with ffmpeg or ImageMagick, and
  draws `social-preview.png`, the image GitHub shows for links to the
  repository. Upload it in the repository's Settings under Social preview.

## Debugging bunk

To debug a bunk that auto-starts from `.bashrc`, run the local build from a
terminal without it:

```bash
BUNK=1 gnome-terminal          # a terminal that does not start bunk
BUNK= ./bunk --trace           # inside it: the local build, logging to /tmp/bunk.log
```
