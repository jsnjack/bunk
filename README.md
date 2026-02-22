# bunk

A lightweight terminal multiplexer written in Go.
Splits your terminal into panes, reflows content on resize, and stays out of your way.

## Features

- **Smart auto-split** (F1) — splits vertically or horizontally based on actual pixel dimensions
- **Pane navigation** — Alt+Arrow keys move focus between panes
- **Content reflow** — text rewraps at the new column width on every split or close
- **Scrollback** — per-pane history with Shift+PgUp / Shift+PgDn
- **Incremental search** — Ctrl+F searches across scrollback and live terminal
- **Mouse support** — click to focus, drag to select, double-click to select word, scroll wheel
- **Theming** — built-in themes: default, solarized-dark, dracula, nord
- **Container badges** — detects Toolbox, Distrobox, Podman, Docker, LXD, kubectl sessions
- **SSH / sudo badges** — visible indicator when elevated or remote

## Installation

```bash
grm install jsnjack/bunk
```

## Key Bindings

| Key | Action |
|-----|--------|
| `F1` | Split active pane (auto direction) |
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
| Mouse drag | Select text |
| Double-click | Select word |

## Configuration

Config file: `~/.config/bunk/config.toml`

Generate a commented default config:

```bash
bunk config init
```

Example:

```toml
# Built-in themes: default, solarized-dark, dracula, nord
theme = "default"

# Logging (set log_file = "" to disable)
log_file  = "/tmp/bunk.log"
log_level = "info"   # debug | info | warn | error

# Cell pixel aspect ratio (height / width).
# Controls split direction. Default 2.25 suits Noto Sans Mono 11pt on Ptyxis.
# Adjust if splits feel wrong for your font.
# cell_aspect = 2.25

[ui]
active_border   = ""  # "#RRGGBB" — focused pane border colour
inactive_border = ""
scrollbar_thumb = ""
scrollbar_track = ""
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

`exec bunk` replaces the shell process with bunk so no extra process is left running.

### GNOME Terminal (alternative)

*Preferences → Profile → Command → Run a custom command instead of my shell* → `bunk`

This applies per-profile, so you can keep a plain shell profile alongside a bunk one.

### Ptyxis

*Profile → Custom shell command* → `/home/you/.local/bin/bunk`

## CLI flags

```
bunk [flags]
bunk config init [--force]

Flags:
  --config string    Config file path (default ~/.config/bunk/config.toml)
  --theme  string    Override theme name
  --debug            Enable debug logging (overrides log_level in config)

Subcommands:
  config init        Write a default config.toml (--force to overwrite)
```

## Debugging

Enable verbose logging:

```bash
bunk --debug
tail -f /tmp/bunk.log
```

Or set in config:

```toml
log_level = "debug"
log_file  = "/tmp/bunk.log"
```
