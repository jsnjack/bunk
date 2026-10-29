<div align="center">

# bunk

bunk adds split panes to any terminal. bunk-gui is a native GTK4 terminal for running coding agents side by side.

![bunk-gui with five agent tabs in the rail: working, needs you, idle, done and failed; the selected tab splits an agent and a shell](assets/gui-hero.png)

</div>

---

## Main idea

A coding agent spends minutes on a task, then waits for an answer. With several agents running, you lose time checking each terminal.

- bunk-gui gives each agent a tab with its repo, branch, container or SSH host, and state.
- Each tab shows one of five states: working, idle, needs you, done or failed.
- A tab split between several agents keeps the first agent's name with "+1" for each other one, and shows the most urgent state among them.
- bunk shows the same states on a badge in each pane.
- One key jumps to the next agent that waits for you: Alt+A in both bunk and bunk-gui.
- Every pane is a real PTY, so agents, vim, btop and shells behave as they do in any terminal.

Both front ends run on one Go core. `internal/session` owns the PTY, the emulator, scrollback, reflow, search and agent status. Each front end adds input and drawing.

| | bunk | bunk-gui |
| :- | :- | :- |
| Runs in | Any terminal: local, SSH, containers | Its own window on Linux (GTK4, libadwaita) |
| Draws with | tcell, in the host terminal's cells | Its own OpenGL renderer, in device pixels |
| Organises work as | Split panes, with agent status per pane | Tabs of split panes, with agent status per tab |
| Use it for | Splits wherever a terminal runs | Several agents side by side on a desktop |

bunk runs inside any terminal emulator, so it works over SSH and on servers. bunk-gui has its own window because only a native window gets real pixels, every key event and the GPU.

bunk-gui draws each pane into a texture at the widget's device-pixel size, so text stays on the pixel grid at 133% and other fractional scales.

## bunk-gui

<table>
<tr>
<td width="50%"><img src="assets/gui-switcher.png" alt="Tab switcher listing needs-you first"></td>
<td width="50%"><img src="assets/gui-bar.png" alt="Tabs in a bar on top"></td>
</tr>
<tr>
<td>Switcher (Ctrl+Shift+O): each tab's repo, branch, agent and folder, needs-you first; filter by agent, repo or branch</td>
<td>Tabs on top: <code>tabs = "bar"</code></td>
</tr>
<tr>
<td><img src="assets/gui-find.png" alt="Find bar with highlighted matches and a collapsed rail of state rings"></td>
<td><img src="assets/gui-shortcuts.png" alt="Keyboard shortcuts window"></td>
</tr>
<tr>
<td>Find (Ctrl+F) with the rail collapsed</td>
<td>Shortcuts window (Ctrl+Shift+?): click a key to rebind it, + adds one</td>
</tr>
</table>

bunk-gui's default keys are bunk's keys plus those of Ptyxis, GNOME's terminal, with F2 for a new tab and Ctrl+Left/Right or Ctrl+Up/Down to switch tabs.

| Feature | Default keys | Details |
| :- | :- | :- |
| New / close tab | F2 / Ctrl+Shift+W | A new tab starts in the current pane's directory |
| New window | Ctrl+Shift+N | Starts another bunk-gui in the current pane's directory; GNOME's app menu has New Window too |
| Switch tab | Ctrl+Right / Ctrl+Left, Ctrl+Down / Ctrl+Up, Alt+1…9, Alt+0 | Alt+0 selects tab 10 |
| Move tab | Ctrl+Shift+PgUp / Ctrl+Shift+PgDn, or drag | Moves the selected tab in the rail or the bar; any tab can be dragged to a new place |
| Rename tab | | Double-click a tab, or choose "Rename tab…" in the menu; `rename_tab` in `[keys]` binds a key. The name replaces the agent or program name, and an empty name brings it back |
| Rail | Ctrl+Shift+B | Expands or collapses the side rail; the menu or `tabs` switches to the bar |
| Agent status | | Dot, state, repo ⎇ branch, container or SSH host, and the agent's notice; see [Agent status and hooks](#agent-status-and-hooks) |
| Switcher | Ctrl+Shift+O | Lists needs-you tabs first; type to filter, Up and Down pick a row, Enter opens it |
| Next needing you | Alt+A | Visits needs-you tabs, then done tabs, and acknowledges each |
| Notifications | | A desktop notification when an agent in a tab you are not looking at needs you, is done or failed, or when a command there that ran 10 s or longer ends; clicking it opens the tab |
| Split | F1 / Alt+F1 | Side by side or stacked by pane shape; Alt+F1 opens a shell in the same container. While you drag a divider, a label shows each side's share |
| Zoom pane | F12 | |
| Move between panes | Alt+arrows | |
| Find | Ctrl+F or Ctrl+Shift+F | Enter or Ctrl+N: next; Ctrl+P: previous; Esc keeps the scroll position; Ctrl+C and Ctrl+V edit the bar's text |
| Scrollback | Shift+PgUp / Shift+PgDn | |
| Copy / paste | Ctrl+C / Ctrl+V, Ctrl+Shift+C / Ctrl+Shift+V | Ctrl+C copies a selection and ends it; with nothing selected it reaches the program. An image pastes as the path of a PNG file holding it; with nothing to paste, Ctrl+V reaches the program. Selecting copies to the primary selection; middle click pastes it |
| Select all | Ctrl+Shift+A | Selects the scrollback and the screen, and copies them to the primary selection |
| Text size | Ctrl+= / Ctrl+- / Ctrl+0 | |
| Images | | SIXEL, Kitty and iTerm2 images drawn at full resolution, 1:1 in device pixels |
| Links | Ctrl+click | Opens OSC 8 links and plain http, https and file URLs; Ctrl+hover underlines them |
| Passthrough | Ctrl+F12 | Per pane: every other key goes to the program; a PASS badge shows it |
| Shortcuts window | Ctrl+Shift+? / Ctrl+? | Rebind or turn off any action, and give it more than one key; ⚠ marks keys terminal programs may use |
| Preferences | Ctrl+, | Layout, rail, font, text zoom, theme, notifications, agent hooks and extra agent CLIs |
| Agent hooks | | Preferences shows which agents report their own state, installs the Codex and Copilot CLI hooks and copies the others; a toast reminds you once when an agent runs without them |
| Options menu | F10 | The ≡ button at the bottom of the rail, or at the right of the bar |
| Fullscreen | F11 | |
| Themes | | bunk's themes; picking one in Preferences repaints the panes at once |
| Quit | Ctrl+Q / Ctrl+Shift+Q | Asks first while a pane runs more than an idle shell |

Programs in a pane see real cell pixels through TIOCGWINSZ and CSI 14/16/18 t. bunk-gui draws their images 1:1:

![A load-test chart from kitten icat, drawn 1:1 in a shell pane next to the agent tabs](assets/gui-image.png)

- Each pane keeps up to 64 MiB of image bitmaps (`--image-bytes`), and the images drawn least recently fall back to half-block cells.
- When a program stops reading its input, the window and other panes keep working. Its pane queues up to 16 MiB of input (`--input-bytes`) and refuses more with a toast.
- When a program reads the mouse, Ctrl+Shift+click opens a link instead of Ctrl+click.

## bunk

![bunk demo](assets/bunk_demo.gif)

| Feature | Default keys | Details |
| :- | :- | :- |
| Split | F1 | Direction from the pixel cell aspect ratio, not the column count |
| Split in the same context | Alt+F1 | New shell in the same container, SSH host or sudo |
| Zoom pane | F12 | Other panes stay open |
| Move between panes | Alt+arrows | |
| Scrollback | Shift+PgUp / Shift+PgDn | 10 000 lines per pane, kept through vim and other alt-screen programs |
| Search | Ctrl+F | Enter or Ctrl+N: next; Ctrl+P: previous; Esc: exit |
| Copy / paste | Ctrl+C / Ctrl+V | Ctrl+C reaches the program when nothing is selected |
| Passthrough | Ctrl+F12 | Per pane: every other key goes to the program |
| Agent status | | A badge per pane: working, idle, needs you (amber), done (green) or failed, with the agent's notice |
| Next needing you | Alt+A | Focuses the next pane whose agent needs you, else one that is done |
| Notifications | | A desktop notification when an agent in a pane you are not looking at needs you, is done or failed, or when a command there that ran 10 s or longer ends; over SSH, OSC 777 to the host terminal |
| Re-initialise host terminal | Alt+R | Recovers after binary output corrupts the host |
| Quit | Ctrl+Q | Ctrl+D or `exit` closes one pane |
| Mouse | | Click focuses, drag selects, double click selects a word; Shift overrides program mouse mode |
| Reflow | | Text rewraps at the new width on every split and resize |
| Themes | | `terminal` (host colours), `default`, `solarized-dark`, `dracula`, `nord`, or your own `[themes.NAME]` |

Alt+F1 supports Podman, Docker, LXD/LXC, Incus, Toolbox, Distrobox, SSH and sudo/su.

Each pane shows these badges in its top-right corner:

| Badge | Meaning |
| :- | :- |
| `◐ claude needs you · Allow npm install?` | The agent's state and notice; needs you and done fill the badge |
| `⬡ mycontainer` | Podman, Docker, LXD, LXC or Incus container |
| `▣ my-toolbox` | Toolbox or Distrobox container |
| `⇄ myserver.com` | SSH session |
| `sudo` / `su` | Elevated shell |
| `PASS` | Keyboard passthrough on; shown first when badges do not fit |
| `ZOOM` | Pane zoomed with F12 |
| `-42` | Scrolled back 42 lines |
| `COPIED` | Selection copied to the clipboard |

The separators around a pane that needs you or is done take the badge's colour.

## Terminal emulation

Both front ends share the emulator, so they support the same sequences.

- Text can use truecolour, SGR underline styles and colours, overline, dim and strike-through.
- Programs can use OSC 8 links, the OSC 52 clipboard, OSC 133 command status, bracketed paste and focus reports.
- bunk forwards OSC 7, OSC 8 links and OSC 133 to the host terminal.
- Input supports the Kitty keyboard protocol, application cursor and keypad modes, and SGR mouse reports.
- Wide characters, combining marks and emoji sequences stay together through copy, search and reflow.
- Both front ends show SIXEL, Kitty and iTerm2 static images. bunk-gui draws them at full resolution, and bunk draws them as half-block cells.
- bunk-gui draws box drawing, block, braille and powerline characters itself, so lines and blocks join across cells without seams.
- Each pane answers DA, DECRQM, DECRQSS and XTGETTCAP itself, also over SSH.
- On one core, a session parses agent transcripts at about 180 MB/s and plain text at 400 MB/s.

[TERMINAL_AUDIT.md](TERMINAL_AUDIT.md) lists every feature with its status and limits.

## Install

### bunk

```bash
grm install jsnjack/bunk
```

You can also download a binary from [Releases](https://github.com/jsnjack/bunk/releases) (Linux and macOS, amd64 and arm64).

To start bunk in every new terminal, add this to `~/.bashrc` or `~/.zshrc`:

```bash
if [[ -t 1 ]] && [[ -z "$BUNK" ]] && [[ "$TERM_PROGRAM" != "bunk-gui" ]] && [[ -z "$SSH_TTY" ]] && command -v bunk &>/dev/null; then
    exec bunk
fi
```

bunk sets `BUNK=1` in every pane and refuses to start inside itself. bunk-gui sets `TERM_PROGRAM=bunk-gui`, so its tabs get a plain shell; it has its own splits.

### bunk-gui

On Fedora x86_64, download `bunk-gui-<version>-1.*.x86_64.rpm` from [Releases](https://github.com/jsnjack/bunk/releases) and install it; dnf pulls in GTK 4 and libadwaita:

```bash
sudo dnf install ./bunk-gui-*.x86_64.rpm
```

To build it, bunk-gui needs Linux, Go 1.26, gcc, GTK 4, libadwaita and OpenGL 3.3. On Fedora:

```bash
sudo dnf install golang gcc gtk4-devel libadwaita-devel
make build          # runs the tests, then writes bin/bunk and bin/bunk-gui
bin/bunk-gui
```

`make gui` rebuilds only `bin/bunk-gui`, without tests. `make rpm` builds a Fedora RPM in `bin/rpmbuild` from a bunk-gui that `go build` compiles with the build cache, so a rebuild takes seconds. It runs no tests; `make check-all` is the gate. `make release` attaches this RPM to the GitHub release.

| Flag | Effect |
| :- | :- |
| `--command CMD` | Run `CMD` with `sh -c` in the first tab; quit when it exits |
| `--tab CMD` | Open another tab running `CMD`; repeatable |
| `--layout rail\|bar` | Tab layout for this run |
| `--font "Noto Sans Mono 11"` | Font family and size for this run |
| `--theme NAME` | Theme for this run |
| `--size 120x36` | Initial size in cells |
| `-c`, `--config FILE` | Settings file instead of `~/.config/bunk-gui/config.toml` |
| `-d`, `--debug` | Log debug detail to stderr |
| `--trace` | Log everything to `/tmp/bunk-gui.log`, which is truncated on every start |
| `--version` | Print the version and exit |

## Configuration

### bunk

`bunk config init` writes a documented `~/.config/bunk/config.toml`. Every key is optional.

```toml
theme = "default"      # terminal, default, solarized-dark, dracula, nord, or a [themes.NAME] below
# scrollback = 10000   # lines per pane
# cell_aspect = 2.25   # cell height / width when the host does not report pixels

[ui]                   # "#RRGGBB" overrides; empty uses the theme
active_border = ""

[notifications]        # for panes you are not looking at
enabled       = true
states        = ["needs-you", "done", "failed"]
command_after = 10     # seconds a command must run to be marked and notify when it ends; 0 = never

[themes.paper]         # a theme of your own: background, foreground and 16 palette colours
background = "#FDF6E3"
foreground = "#586E75"
palette = ["#073642", "#DC322F", "#859900", "#B58900", "#268BD2", "#D33682", "#2AA198", "#EEE8D5",
           "#002B36", "#CB4B16", "#586E75", "#657B83", "#839496", "#6C71C4", "#93A1A1", "#FDF6E3"]
# cursor, active_border, inactive_border, scrollbar_thumb and scrollbar_track are optional

[keys]                 # empty keeps the default; "none" sends the key to the pane
split          = "f1"
split_context  = "alt+f1"
zoom           = "f12"
passthrough    = "ctrl+f12"
next_needs_you = "alt+a"
search         = "ctrl+f"
quit           = "ctrl+q"
```

bunk takes these flags: `-c`/`--config FILE`, `--theme NAME`, `-d`/`--debug`, `--trace` (logs raw PTY bytes to `/tmp/bunk.log`) and `--version`.

### bunk-gui

bunk-gui reads `~/.config/bunk-gui/config.toml`. Every key is optional.

```toml
tabs = "rail"              # "rail" (side) or "bar" (top)
rail_open = false          # start with the rail expanded
rail_style = "ring"        # tabs in the collapsed rail: "ring" of the panes' states, or "map" of glyphs
font = "Noto Sans Mono"
font_size = 11             # points
text_zoom = 100            # % of font_size at start and after Ctrl+0
theme = "default"          # as in bunk, with [themes.NAME] tables of your own
hooks_hint = true          # a toast when an agent runs without bunk's hooks

[keys]                     # one key or a list; actions in cmd/bunk-gui/bindings.go
next_tab = ["ctrl+right", "ctrl+pgdn"]
copy = "ctrl+shift+c"      # Ctrl+C always goes to the terminal
zoom = "none"              # F12 goes to the terminal

[[agents]]                 # another agent CLI to recognise; repeatable
name = "myagent"
processes = ["myagent"]
scripts = ["@me/myagent"]  # for agents that run under node, python, bun, deno or ruby

[notifications]            # desktop notifications for tabs you are not looking at
enabled = true
states = ["needs-you", "done", "failed"]
command_after = 10         # seconds a command must run to be marked and notify when it ends; 0 = never
```

- bunk-gui watches the file and applies edits to the running window.
- A bad value shows a toast, and the setting keeps its default. An unknown theme or an invalid `[themes.NAME]` table gives the default theme.
- The theme sets the terminal colours, the badges, the background behind the panes and the dividers between them. The rail, the bar and the dialogs keep their dark style.
- bunk-gui has no host terminal, so the `terminal` theme shows the colours of `default`.
- Colours a program sets with OSC 4, 10, 11 or 12 stay in front of the theme.
- `[keys]` uses bunk's action names and key syntax where both have the action.
- `"none"` or `""` turns an action off, so its keys reach the terminal.
- Keyboard passthrough works per pane, through Ctrl+F12 or the menu's "Send all keys to terminal".
- The shortcuts and preferences windows write to this file and rewrite only the lines they change.

## Agent status and hooks

Both front ends infer each agent's state from these sources:

- The foreground process names the agent. Both recognise 15 agent CLIs, such as claude, codex, copilot and gemini, and bunk-gui's `[[agents]]` adds more.
- Output in the last 2 s means working, and silence means idle. When an agent stops after at least 10 s of output, it is done instead, so agents without hooks notify you too.
- OSC 9/99/777 notifications and BEL from an agent mean needs you.
- While ssh or mosh runs in a pane, OSC 9/99/777 from the other end mean needs you too. Bells from there do not count, because a remote shell rings one on a failed tab completion.
- A failed command reported through OSC 133;D means failed.

Focusing the pane or typing into it acknowledges the state and clears needs you and done.

Both front ends show each state with the same glyph and colour, in the pane badges, the tabs and the rail:

| Glyph | State | Meaning |
|-------|-------|---------|
| ● (blue) | Working | The agent is busy, or a long command is running |
| ◐ (amber) | Needs you | The agent asks a question or a permission |
| ✓ (green) | Done | The agent's turn or a long command finished, and you have not looked yet |
| ✕ (red) | Failed | The agent failed, or a command exited non-zero |
| ○ (grey) | Idle or shell | The agent waits at its prompt, or the pane is a plain shell without a command mark |

bunk-gui's collapsed rail draws each tab as a ring in these colours. A tab with one pane is a closed ring. A tab with split panes is the same ring cut into one arc per pane, and each arc faces its pane: an agent that needs you on the left of two stacked shells shows an amber arc on the left and grey arcs at the top right and bottom right. A plain shell's arc is dimmed, so the agents stand out. A tab with more than 6 panes also shows the pane count under its ring.

With `rail_style = "map"`, or "A map of the panes" in Preferences, the rail shows each tab's glyph instead. A tab with 2 to 4 panes then shows a small map of them: one cell per pane, placed as the splits place the panes, each with its pane's glyph. Each split takes half of the map wherever its divider sits, so the map keeps its shape while you drag a divider. A tab with 5 or more panes shows its most urgent glyph over the pane count.

When an agent you are not looking at needs you, is done or failed, both front ends notify you:

- bunk-gui shows a desktop notification per pane, such as "claude needs you" or "claude on build01 needs you" with the notice and repo ⎇ branch. Two agents split in one tab each send their own. A newer state replaces it, and it goes away when you focus the pane. Clicking it raises the window, selects the tab and focuses the pane.
- bunk notifies for a pane that is not active, or for any pane while the host terminal is not focused.
- On your desktop's machine, bunk shows desktop notifications through D-Bus with `gdbus`, which GLib installs. They follow the same rules as bunk-gui's, without the click: bunk cannot raise its terminal's window.
- Over SSH, without a session bus or without `gdbus`, bunk sends an OSC 777 notification to the host terminal instead. foot, WezTerm, Ghostty and urxvt show it. GNOME Terminal and Ptyxis with VTE 0.84 do not.
- The same state notifies again only 10 s after the last notification, so an agent that flaps between states does not flood the desktop.
- `[notifications]` in either config.toml turns notifications off or picks the states, and bunk-gui's preferences window edits it.

Long commands notify you the same way. When a command that ran for at least `command_after` seconds (default 10) ends in a pane you are not looking at, you get a notification such as "tar finished after 2m 14s" with the command line. It stays until you look at the pane.

- With shell integration that sends OSC 133 marks, a command runs from its start mark to the next prompt, so a loop or a list of commands counts as one. A non-zero exit status shows as "failed" with the status. Fedora's `vte.sh` sends the marks when `VTE_VERSION` is set, as it is in a terminal started from Ptyxis or GNOME Terminal.
- Without marks, a command runs while a program other than the shell holds the terminal, as bunk sees it once a second, and its exit status is unknown.
- `vte.sh` puts the end mark in `PS1`, so a `PS1` set later in `~/.bashrc` drops it and keeps only the start mark. bunk then treats commands as unmarked. To keep the marks, add them back after your `PS1` line: `PS1='\[\e]133;D;$?\e\\\e]133;A\e\\\]'"$PS1"'\[\e]133;B\e\\\]'`.
- An agent's own run does not notify as a command: the agent's states notify instead.
- Commands on the other end of ssh notify only when the remote shell sends the marks.

A command that has run for `command_after` seconds also marks its tab in bunk-gui and gets a badge on its pane in both front ends: "● make running". When it ends in a pane you are not looking at, the mark turns to "✓ done" or "✕ failed" with how long it ran, and stays until you look at the pane. The marks show with notifications turned off; `command_after = 0` turns them off too.

- Sessions that run commands of their own are not marked: ssh and mosh, a container you entered, `sudo -i` or `su`, and a nested shell. Commands inside them are, when their shell sends OSC 133 marks.
- Full-screen programs such as vim, htop or less are not marked, and neither are agents, whose own states show.
- A REPL such as `python3` counts as a running command.

With hooks, the agent reports its own state, and the report takes priority over inference. Set them up once per machine:

```bash
bunk notify hooks                  # each agent: installed, outdated or missing, and the next step
bunk notify hooks install codex    # Codex and Copilot CLI: write the hooks file; trust it with /hooks in Codex
bunk notify hooks claude           # Claude Code and Gemini CLI: print the snippet to merge into settings.json
```

- In bunk-gui, Preferences (Ctrl+,) → Agent hooks does the same: it shows each agent's state, installs the Codex and Copilot CLI hooks with a click and copies the snippets of Claude Code and Gemini CLI, whose settings file bunk never writes.
- bunk-gui reminds you with a toast, once per agent and window, when an agent runs without its hooks. `hooks_hint = false` turns the reminder off.
- [docs/agent-hooks.md](docs/agent-hooks.md#set-up) walks through the setup for each agent.

- `bunk notify` and `bunk-gui notify` take the same arguments, and the snippets call bunk-gui when it is installed, else bunk.
- The Claude Code snippet also reports a question (AskUserQuestion) or a plan waiting for approval as needs you, with the question as the notice. A turn that ends while a background shell or subagent still runs stays working until Claude finishes after it.
- Each pane gets `BUNK_NOTIFY_SOCKET` and `BUNK_NOTIFY_SESSION` in its environment. Sockets live under `$XDG_RUNTIME_DIR/bunk/`.
- [docs/agent-hooks.md](docs/agent-hooks.md) has snippets for Claude Code, Codex, Copilot CLI and Gemini CLI, the wire format and exit codes.

Agents on hosts you reach over ssh report to the pane or tab that runs ssh:

- Install bunk on the server and add the same hook snippets there.
- Plain `ssh` works. Without a socket, the hooks write their reports to the terminal, and bunk accepts them while ssh or mosh runs in the pane.
- `bunk ssh host` (or `bunk-gui ssh host`) also gives the remote shell the pane's token, so its reports count even when ssh runs behind another program.
- The tab shows the remote agent's name, repo and branch.
- bunk's TUI on the server, or inside a bunk-gui tab, shows its most urgent pane on the tab or pane around it.
- Inside tmux on the server, set `allow-passthrough all`. mosh drops the reports.
- [docs/remote-agent-status.md](docs/remote-agent-status.md) describes the design.

## Packages

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
| `internal/theme/` | Colour themes of both front ends: built-in and custom themes, `[ui]` overrides, badge colours |
| `third_party/tcell/` | tcell v2.13.9 with overline, keypad and keycap-width patches |
| `scripts/` | `headless-gui.sh`, `screenshots.sh`, terminal feature scripts |
| `packaging/` | Desktop entry, icon, AppStream metainfo; `fedora/` holds the RPM spec |
| `docs/` | `agent-hooks.md`: hook snippets and the notify protocol |

## Development

```bash
make check         # bunk and the core: fmt, vet, race tests, build, lint
make gui-test      # bunk-gui: vet, race tests on a headless compositor, packaging checks
make check-all     # both; run it before calling work done
make fuzz          # every Fuzz target for FUZZTIME each (default 30s); not in check-all
make soak          # long leak and soak runs (2000 sessions, 500 MiB of output); not in check-all
make bench-check   # pinned benchmarks vs testdata/bench-baseline.txt; fails on a >10% slowdown
make bench-baseline # record a new baseline after an intended speed change
make screenshots   # render the README screenshots into assets/
make run           # build bunk and open it with --trace in a new terminal
```

- GUI tests run on a private mutter from `scripts/headless-gui.sh`, so no windows open on the desktop.
- The compositor has one 2560x1600 monitor at 133%, its own D-Bus and its own clipboard.
- Pixel, idle-frame, allocation and latency checks are Go tests. Turn a manual check into a test.
- check-all counts fds, goroutines, child processes and heap around session, TUI and GUI cycles to find leaks.
- `go test -short` skips window tests.
- Scenario steps wait for UI states rather than fixed times. `BUNK_GUI_PARALLEL` (default 8) sets how many scenarios run at once.
- Window tests need mutter, gdctl and dbus-run-session. Packaging checks need desktop-file-validate and appstreamcli.
- `TestGUIScreenshots` renders the README scenario in every `make gui-test`.
- `make screenshots` copies the PNGs to `assets/` and shrinks them with pngquant, oxipng, optipng or ImageMagick.

To debug a bunk that auto-starts from `.bashrc`, run the local build from a terminal without it:

```bash
BUNK=1 gnome-terminal          # a terminal that does not start bunk
BUNK= ./bunk --trace           # inside it: the local build, logging to /tmp/bunk.log
```

## License

MIT, see [LICENSE](LICENSE).
