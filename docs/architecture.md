# Architecture: the file index

The files and packages of bunk and bunk-gui with what each holds, and the
local extensions of the vendored vt10x emulator. [AGENTS.md](../AGENTS.md) has
the one-line-per-package map; read this when you open a file you have not seen
before or change `internal/vt10x`.

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

## Files

```
main.go             Entry point — calls Execute()
cmd.go              cobra root command, run(), app initialization
cmd_config.go       "bunk config" subcommand tree
cmd_notify.go       "bunk notify": the agent hook CLI (notify.RunCLI)
cmd_ssh.go          "bunk ssh": ssh that exports the pane's token (notify.RunSSH)
cmd_present.go      "bunk present": shows a document in bunk-gui's panel, else
                    the browser (notify.RunPresent)
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
  command.go        Commands the shell runs, from OSC 133;C/D marks or the
                    process poll; the last one that ended (Status.Command)
  search.go         Case-insensitive search over scrollback and the live grid
  history.go        History: the primary screen and scrollback as SGR text
                    with OSC 8 links, which a restored pane replays
  status.go         Agent status: AgentSpec detection, attention requests
                    (OSC 9/99/777, BEL), failures (OSC 133;D), done after a
                    long working stretch, git branch, explicit agent
                    reports (Notify), OSC 9;4 progress (Status.Progress)
  winsize.go        PTY window size in cells and virtual pixels
  timing.go         Timing: per-session timer intervals (Options.Timing)
  ptyfd.go          Setsize: window-size ioctl that holds the PTY descriptor
  ptyfd_linux.go    Pollable PTY master, so Close ends ReadLoop
  writer.go         PTY writer: one queue for query replies and input,
                    written by one goroutine; caps, drops and refusals

internal/restore/   Saved windows for session restore: snapshot types, the
                    store of slots (one per window, held by a lock while it
                    runs), scrollback files, each agent's resume command

internal/badges/    Pane status badges for both front ends: which show, their
                    order, text and fitting (Layout); badgetest holds the cases
                    both front ends' parity tests draw

internal/theme/     Colour themes for both front ends: the built-in themes,
                    custom [themes.NAME] tables, [ui] overrides, and the
                    colours of badge roles (BadgeInks) and agent states

internal/boxdraw/   Cell masks for box drawing, blocks, shades, braille,
                    legacy computing symbols, powerline and branch symbols
                    (kitty's set); bunk-gui's atlas uses them, not the font

internal/notify/    Agent hook reports: Unix socket server (token → Session),
                    client, `notify` CLI (RunCLI), hook snippets per agent,
                    whether they are installed and installing the
                    standalone ones (hookstatus.go), the in-band writer
                    without a socket, `ssh` (RunSSH), present requests and
                    the `present` CLI (present.go, RunPresent), the
                    bunk-present skill and its install (skill.go,
                    skill/SKILL.md)
  wire/             The report line, its OSC 1337 in-band form, tmux/screen
                    wrapping; imported by notify and session alike

internal/present/   Documents for bunk-gui's panel: Markdown to an HTML page
                    (goldmark, Chroma, math), the page runtime (bunkDoc:
                    render, anchor, replace with change marks), Mermaid and
                    KaTeX embedded, standalone pages for the browser;
                    file kinds and previews of other files (preview.go)

internal/alert/     Agent and command notifications for both front ends:
                    when a state change or a long command's end notifies
                    (Tracker), the text, [notifications] settings, the
                    sanitised OSC 777 sequence and desktop text

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
  cmd_present.go    "bunk-gui present": shows a document in the tab's panel
                    (notify.RunPresent)
  logger.go         slog setup for --debug and --trace; GLib messages arrive through slog
  app.go            App struct, window activation, test-run hooks and statistics
  themes.go         The theme shown: terminal colours, the CSS of the content
                    background, dividers and badges; repaints on a change
  prefs_theme.go    The Theme section of Preferences: a choice per theme with
                    a preview of its colours
  progress.go       OSC 9;4 progress as a bar under each tab
  alerts.go         Desktop notifications per pane: when (internal/alert), the
                    show-pane action, --report records, preferences section
  desknotify.go     org.freedesktop.Notifications client: replace, withdraw,
                    clicks with their activation token
  newwindow.go      New window: another bunk-gui process in the pane's
                    directory, with an activation token
  scope.go          Each pane's child in a systemd scope of its own, so an
                    OOM kill in a pane leaves bunk-gui and other panes alone
  tabname.go        Tab names: rename dialog, double-click on a tab
  railpeek.go       Labels beside the collapsed rail's rings while Alt
                    is held alone
  ctxmenu.go        Context menus of a tab (right-click) and a pane
                    (right-click, Shift+right-click under a mouse
                    program); muting a tab, marking it seen, closing
                    with confirmation
  restore.go        Session restore: saves the window, the startup banner,
                    rebuilds tabs and splits, resumes agents (--restore)
  prefs_hooks.go    Agent hooks in Preferences (state, Install, Copy
                    snippet) and the toast for an agent without hooks
  panel.go          The document panel per tab: documents and previews,
                    reload in place, asking the agent, restore, its --do steps
  presentserve.go   The bunk-present:// scheme: assets, each document's
                    directory as its own origin, the HTML sandbox policy
  webview.go        WebKitGTK views (cgo): one ephemeral session, the
                    navigation policy, the context menu, script evaluation
packaging/          Desktop entry, icon, metainfo; fedora/ holds the RPM spec

assets/             README pictures from TestGUIScreenshots: bunk-gui shots and
                    crops, the states loop (gui-states.webp), bunk's TUI
                    (tui.png) and the social preview; make screenshots
docs/               features.md: every feature and key; configuration.md;
                    agent-status.md: states, rail, notifications, command
                    marks; agent-hooks.md: hook recipes and the notify
                    protocol; remote-agent-status.md: agents on hosts reached
                    over ssh; present.md: the document panel, `bunk present`,
                    its protocol, hooks and skill; development.md: packages,
                    builds, tests
scripts/            Terminal feature scripts (tested by terminal_features_test.go);
                    headless-gui.sh runs GUI tests; screenshots.sh renders assets/;
                    benchcheck/ runs and compares benchmarks (make bench-check)
testdata/           bench-baseline.txt: the timings bench-check compares with
```

## vt10x's local extensions

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
