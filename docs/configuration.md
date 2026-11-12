# Configuration

bunk and bunk-gui each read a TOML file, and every key in it is optional.
bunk-gui's preferences and shortcuts windows write to its file for you.
Preferences (Ctrl+,) has a control for every key except `[keys]`, which its
Keyboard shortcuts row opens, and `[themes.NAME]` tables, which you write
by hand: its Open in editor button opens the file in your text editor.

## bunk-gui

bunk-gui reads `~/.config/bunk-gui/config.toml`.

```toml
tabs = "rail"              # "rail" (side) or "bar" (top)
rail_open = false          # start with the rail expanded
rail_style = "ring"        # tabs in the collapsed rail: "ring" of the panes' states, or "map" of glyphs
rail_peek_delay = 200      # ms Alt is held alone before the collapsed rail labels its tabs (0-2000)
font = "Noto Sans Mono"
font_size = 11             # points
text_zoom = 100            # % of font_size at start and after Ctrl+0
theme = "default"          # as in bunk, with [themes.NAME] tables of your own
hooks_hint = true          # offer to set up missing or outdated agent hooks in a banner
restore = true             # save the windows and offer them back at the next start
restore_scrollback = true  # save each pane's scrollback with them
new_dir = "~"              # where new tabs and windows start; "current" uses the active pane's directory

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

[present]                  # the document panel (docs/present.md)
auto = false               # open it for documents agent hooks report; false only lists them
```

- bunk-gui watches the file and applies edits to the running window.
- A bad value shows a toast, and the setting keeps its default. An unknown
  theme or an invalid `[themes.NAME]` table gives the default theme.
- The theme sets the terminal colours, the badges, the background behind the
  panes and the dividers between them. The rail, the bar and the dialogs keep
  their dark style.
- bunk-gui has no host terminal, so the `terminal` theme shows the colours of
  `default`.
- Colours a program sets with OSC 4, 10, 11 or 12 stay in front of the theme.
- `[keys]` uses bunk's action names and key syntax where both have the action.
- `"none"` or `""` turns an action off, so its keys reach the terminal.
- Keyboard passthrough works per pane, through Ctrl+F12 or the menu's "Send
  all keys to terminal".
- The shortcuts and preferences windows rewrite only the lines they change.

bunk-gui takes these flags:

| Flag | Effect |
| :- | :- |
| `--command CMD` | Run `CMD` with `sh -c` in the first tab; quit when it exits |
| `--tab CMD` | Open another tab running `CMD`; repeatable |
| `--layout rail\|bar` | Tab layout for this run |
| `--font "Noto Sans Mono 11"` | Font family and size for this run |
| `--theme NAME` | Theme for this run |
| `--size 120x36` | Initial size in cells |
| `--image-bytes N` | Image bitmaps each pane keeps, 64 MiB by default |
| `--input-bytes N` | Input each pane queues for a program that does not read it, 16 MiB by default |
| `-c`, `--config FILE` | Settings file instead of `~/.config/bunk-gui/config.toml` |
| `-d`, `--debug` | Log debug detail to stderr |
| `--trace` | Log everything to `/tmp/bunk-gui.log`, which is truncated on every start |
| `--version` | Print the version and exit |

## bunk

`bunk config init` writes a documented `~/.config/bunk/config.toml`.

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

bunk takes these flags: `-c`/`--config FILE`, `--theme NAME`, `-d`/`--debug`,
`--trace` (logs raw PTY bytes to `/tmp/bunk.log`) and `--version`.

### Starting bunk in every terminal

To start bunk in every new terminal, add this to `~/.bashrc` or `~/.zshrc`:

```bash
if [[ -t 1 ]] && [[ -z "$BUNK" ]] && [[ "$TERM_PROGRAM" != "bunk-gui" ]] && [[ -z "$SSH_TTY" ]] && command -v bunk &>/dev/null; then
    exec bunk
fi
```

bunk sets `BUNK=1` in every pane and refuses to start inside itself. bunk-gui
sets `TERM_PROGRAM=bunk-gui`, so its tabs get a plain shell; it has its own
splits.

## Notes for working on the code

How both programs read and validate their files, and where the code keeps each
setting and its default.

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
- `[notifications]` turns notifications on or off (`enabled`), picks
  the agent states that send one (`states`, default needs-you, done and
  failed), and sets how many seconds a command must run for its end to
  send one and for its pane to be marked (`command_after`, default 10; 0
  turns both off). A state name bunk does not know or a negative
  `command_after` fails at startup.

`config.go` is the authoritative reference for every field and its default.

bunk-gui reads `~/.config/bunk-gui/config.toml` (`--config`): `tabs`,
`rail_open`, `rail_style`, `rail_peek_delay`, `font`, `font_size`, `text_zoom`, `theme`, `hooks_hint`,
`restore`, `restore_scrollback`, `new_dir`, `[themes.NAME]`, `[[agents]]`, `[notifications]`, `[present]` and `[keys]`. `theme` and
`[themes.NAME]` work as in bunk's config, and `--theme` overrides `theme`
for one run. `new_dir` is where new tabs and windows start: `~` (the
default), `~/…`, an absolute path, or `current` for the active pane's
directory; a directory that is gone gives the home directory, and splits
start in their pane's directory. `rail_peek_delay` is how many
milliseconds Alt is held alone before the collapsed rail labels its tabs
(`railpeek.go`), 0 to 2000 and 200 by default; the delay lets Alt+A or a
shell's Alt+B finish without the labels flashing. Every key reloads live
(`configwatch.go`); turning `restore` on gives a window that started
without it a slot, so it saves from then on. A new key gets a control in
Preferences (`prefs.go`), a field in `prefsReport` and a step in
`TestGUIPreferences`.
`[notifications]` has the same keys as in bunk's config. `[keys]` uses
bunk's action names and key syntax where they overlap; "none" sends the key
to the terminal. The file is watched and reloads live, and a bad value shows
a toast and keeps the default. The shortcuts window (Ctrl+Shift+?) and the
preferences window (Ctrl+,) rewrite only the lines they change
(`cmd/bunk-gui/keysfile.go`). `cmd/bunk-gui/bindings.go` lists every action and
its default; Design Decisions says where the defaults come from.
