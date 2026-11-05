# Configuration

bunk and bunk-gui each read a TOML file, and every key in it is optional.
bunk-gui's preferences and shortcuts windows write to its file for you.

## bunk-gui

bunk-gui reads `~/.config/bunk-gui/config.toml`.

```toml
tabs = "rail"              # "rail" (side) or "bar" (top)
rail_open = false          # start with the rail expanded
rail_style = "ring"        # tabs in the collapsed rail: "ring" of the panes' states, or "map" of glyphs
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
