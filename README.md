# bunk

A lightweight terminal multiplexer written in Go.
Splits your terminal into panes, reflows content on resize, and stays out of your way.

## Features

- **Smart auto-split** (F1) — splits vertically or horizontally based on actual pixel dimensions
- **Context-aware split** (Alt+F1) — re-opens the same container / SSH / sudo session in the new pane
- **Zoom** (F12) — toggles fullscreen on the active pane without closing others
- **Pane navigation** — Alt+Arrow keys move focus between panes
- **Inherited working directory** — new panes open in the same directory as the active pane
- **Content reflow** — text rewraps at the new column width on every split or close
- **Scrollback** — per-pane history with Shift+PgUp / Shift+PgDn; preserved through vim and other full-screen apps
- **Incremental search** — Ctrl+F searches across scrollback and live terminal
- **Mouse support** — click to focus, drag to select (clamped to the originating pane), double-click to select word, scroll wheel; drag to edge auto-scrolls into scrollback
- **Theming** — built-in themes: terminal, default, solarized-dark, dracula, nord
- **Container badges** — detects Toolbox, Distrobox, Podman, Docker, LXD/LXC, Incus, and kubectl/oc exec sessions; shows friendly container name
- **SSH badge** — `⇄ hostname` indicator when connected to a remote host
- **sudo / su badge** — visible indicator when elevated
- **Dynamic tab title** — updates the host terminal tab with the active pane's process, directory, and pane index (`bash: ~/project [2/4]`)

## Installation

```bash
grm install jsnjack/bunk
```

## Auto-launch in every terminal session

Add to your `~/.bashrc` (or `~/.zshrc`):

```bash
# Auto-launch bunk when opening a new terminal tab/window.
# The BUNK=1 variable (set by bunk itself) prevents recursive invocation
# inside panes that bunk spawns.
if [[ -t 1 ]] && [[ -z "$BUNK" ]] && command -v bunk &>/dev/null; then
    exec bunk
fi
```

## Key Bindings

| Key | Action |
|-----|--------|
| `F1` | Split active pane into host shell (auto direction) |
| `Alt+F1` | Split active pane, inheriting current context (container / SSH / sudo) |
| `F12` | Toggle fullscreen zoom on the active pane |
| `Ctrl+D` / `exit` | Close active pane |
| `Ctrl+Q` | Quit bunk |
| `Alt+←` `Alt+→` `Alt+↑` `Alt+↓` | Navigate between panes |
| `Shift+PgUp` / `Shift+PgDn` | Scroll back / forward in history |
| `Ctrl+F` | Enter search mode |
| `Enter` / `Ctrl+N` | Next search match |
| `Ctrl+P` | Previous search match |
| `Escape` | Exit search mode |
| `Ctrl+C` | Copy selection to clipboard (passes ^C through if nothing selected) |
| `Ctrl+V` | Paste from clipboard |
| Mouse click | Focus pane |
| Mouse drag | Select text (confined to the pane where the drag started) |
| Double-click | Select word |
| Drag to top/bottom edge | Auto-scroll into scrollback while selecting |

## Context-aware splitting (Alt+F1)

`F1` always opens a plain host shell in the new pane.

`Alt+F1` clones the active pane's context:

| Active session | New pane opens with |
|---|---|
| `podman exec mycontainer` | `podman exec -it mycontainer /bin/bash` |
| `podman run -it fedora:43` | `podman exec -it <resolved-id> /bin/bash` |
| `docker exec mycontainer` | `docker exec -it mycontainer /bin/bash` |
| `lxc exec myvm -- su --login username` | same command verbatim |
| `incus exec myvm -- /bin/bash` | same command verbatim |
| `toolbox enter my-toolbox` | `toolbox run --container my-toolbox /bin/bash` |
| `distrobox enter my-box` | `distrobox enter -n my-box -- /bin/bash` |
| `ssh user@server` | `ssh user@server` |
| `sudo -s` | `sudo -s` |

> **Note:** `kubectl exec` and `oc exec` sessions show a badge but Alt+F1 re-entry is not yet supported.

## Status badges

Each pane displays a compact badge in its top-right corner:

| Badge | Meaning |
|---|---|
| `⬡ mycontainer` | Inside a Podman / Docker / LXD / LXC / Incus container |
| `▣ my-toolbox` | Inside a Toolbox or Distrobox container |
| `⇄ myserver.com` | SSH session |
| `sudo` / `su` | Elevated shell |
| `-42` | Scrolled back 42 lines |
| `COPIED` | Selection just copied to clipboard |

## Configuration

Config file: `~/.config/bunk/config.toml`

Generate a commented default config:

```bash
bunk config init
```

Example:

```toml
# Built-in themes: terminal, default, solarized-dark, dracula, nord
# Use "terminal" to inherit colours from your terminal emulator.
theme = "default"

# Logging — only written when --debug or --trace is passed on the command line.
log_file  = "/tmp/bunk.log"

# Cell pixel aspect ratio (height / width).
# Controls split direction. Default 2.25 suits Noto Sans Mono 11pt on Ptyxis.
# Adjust if splits feel wrong for your font.
# cell_aspect = 2.25

[keybindings]
split         = "f1"         # split into host shell
split_context = "alt+f1"     # split, inheriting current context
copy          = "ctrl+c"
paste         = "ctrl+v"
search        = "ctrl+f"
quit          = "ctrl+q"

[ui]
active_border   = ""  # "#RRGGBB" — focused pane border colour
inactive_border = ""
scrollbar_thumb = ""
scrollbar_track = ""
```

## CLI flags

```
bunk [flags]
bunk config init [--force]

Flags:
  --config string    Config file path (default ~/.config/bunk/config.toml)
  --theme  string    Override theme name
  --debug            Enable debug logging (writes to log_file)
  --trace            Enable trace logging (very verbose; includes raw PTY bytes)

Subcommands:
  config init        Write a default config.toml (--force to overwrite)
```

## Debugging

Enable verbose logging:

```bash
bunk --debug
tail -f /tmp/bunk.log
```
