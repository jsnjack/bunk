# Pane badges and themes

How both front ends get the same pane badges (`internal/badges`) and the same
colours (`internal/theme`), and the parity tests that keep them together. Read
it before adding or changing a badge, a theme or a badge colour.

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

## Pane badges

- Pane badges come from `internal/badges`, for both front ends.
  - `badges.Layout` decides which badges show, their order, text and fitting
    from plain pane state. Each badge has a `Role`. `badges.Places` gives
    the context badge's places, which bunk-gui's tabs list too.
  - The front ends only paint. `theme.BadgeInks` names each role's colours:
    palette slots, most backgrounds blended `theme.BadgeDim` (40%) toward
    the theme background, and fixed agent colours. The TUI resolves them in
    tcell colours (`badgeStyle`, status.go), and bunk-gui through
    `Theme.Badge` (`badgeLook`, cmd/bunk-gui/badge.go).
  - The where badge (`RoleWhere`) names where an agent or a program on
    the alternate screen works: repo ⎇ branch, else the directory with
    `~` (`badges.Where`, which bunk-gui's tabs use too). It shows only
    when every badge before it fits, takes the room left before the
    notice up to two thirds of the row, is cut to fit or left out alone,
    and sits left of the context badge. While ssh or mosh runs,
    `badges.WhereOf` gives nothing unless a remote agent reported its
    directory, because the local one says nothing about the remote
    program. A multiplexer in the foreground (bunk, tmux, zellij, screen)
    gets none either: the badge would cover the badges it draws itself.
    The theme oracle predates it and skips its role.
  - Add a badge in `internal/badges`, never in one front end, and add a case
    to `badgetest.Cases`. `TestBadgeParityTUI` and `TestBadgeParityGUI` both
    draw every case, so the front ends cannot drift apart.
  - bunk-gui draws the badges as GtkInscriptions in the terminal font, over
    the pane's texture. A badge change draws no pane frame. The row syncs
    after each frame and on status, passthrough, zoom, find and flash events.
  - The find bar keeps the top right, and the badges sit right below it while
    it is open. A badge that appears while a query is typed never moves the
    bar.

## Themes

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
