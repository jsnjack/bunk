# Agent status and notifications

bunk and bunk-gui show each agent's state in its pane's badge, and bunk-gui
also shows it in the tab and the rail. This page explains where the state
comes from, how it shows and when it notifies you.
[agent-hooks.md](agent-hooks.md) covers the hooks that let agents report
their own state.

## States

Both front ends show each state with the same glyph and colour, in the pane
badges, the tabs and the rail:

| Glyph | State | Meaning |
|-------|-------|---------|
| ● (blue) | Working | The agent is busy, or a long command is running |
| ◐ (amber) | Needs you | The agent asks a question or a permission |
| ✓ (green) | Done | The agent's turn or a long command finished, and you have not looked yet |
| ✕ (red) | Failed | The agent failed, or a command exited non-zero |
| ○ (grey) | Idle or shell | The agent waits at its prompt, or the pane is a plain shell without a command mark |

## Where the state comes from

With hooks, the agent reports its own state, and the report takes priority
over inference. Without hooks, both front ends infer the state from these
sources:

- The foreground process names the agent. Both recognise 15 agent CLIs, such
  as claude, codex, copilot and gemini, and bunk-gui's `[[agents]]` adds more.
- Output in the last 2 s means working, and silence means idle. When an agent
  stops after at least 10 s of output, it is done instead, so agents without
  hooks notify you too.
- OSC 9/99/777 notifications and BEL from an agent mean needs you.
- While ssh or mosh runs in a pane, OSC 9/99/777 from the other end mean needs
  you too. Bells from there do not count, because a remote shell rings one on
  a failed tab completion.
- A failed command reported through OSC 133;D means failed.

Focusing the pane or typing into it clears done. A needs-you the agent
reported through its hooks stays while the agent still waits: focus and keys
such as arrows leave it, Enter answers it, and Esc or Ctrl+C cancel it. A
question from Claude Code (AskUserQuestion, a plan to approve) stays through
Enter too, because it can hold several questions, and ends when the agent
reports that you answered. A needs-you inferred from a bell or an OSC
notification clears on focus or typing.

## Tabs and the rail

A bunk-gui tab shows its first agent's name, repo ⎇ branch, container or SSH
host and notice. A tab split between several agents adds "+1" for each other
one and shows the most urgent state among them.

bunk-gui's collapsed rail draws each tab as a ring in the state colours. A tab
with one pane is a closed ring. A tab with split panes is the same ring cut
into one arc per pane, and each arc faces its pane: an agent that needs you on
the left of two stacked shells shows an amber arc on the left and grey arcs at
the top right and bottom right. A plain shell's arc is dimmed, so the agents
stand out. A tab with more than 6 panes also shows the pane count under its
ring.

With `rail_style = "map"`, or Map for "Collapsed side tabs show" in
Preferences, the rail shows each tab's glyph instead. A tab with 2 to 4 panes
then shows a small map of them: one cell per pane, placed as the splits place
the panes, each with its pane's glyph. Each split takes half of the map
wherever its divider sits, so the map keeps its shape while you drag a divider.
A tab with 5 or more panes shows its most urgent glyph over the pane count.

## Notifications

When an agent you are not looking at needs you, is done or failed, both front
ends notify you:

- bunk-gui shows a desktop notification per pane, such as "claude needs you"
  or "claude on build01 needs you" with the notice and repo ⎇ branch. Two
  agents split in one tab each send their own. A newer state replaces it, and
  it goes away when you focus the pane. Clicking it raises the window, selects
  the tab and focuses the pane.
- bunk notifies for a pane that is not active, or for any pane while the host
  terminal is not focused.
- On your desktop's machine, bunk shows desktop notifications through D-Bus
  with `gdbus`, which GLib installs. They follow the same rules as bunk-gui's,
  without the click: bunk cannot raise its terminal's window.
- Over SSH, without a session bus or without `gdbus`, bunk sends an OSC 777
  notification to the host terminal instead. foot, WezTerm, Ghostty and urxvt
  show it. GNOME Terminal and Ptyxis with VTE 0.84 do not.
- The same state notifies again only 10 s after the last notification, so an
  agent that flaps between states does not flood the desktop.
- `[notifications]` in either config.toml turns notifications off or picks the
  states, and bunk-gui's preferences window edits it.

## Long commands

Long commands notify you the same way. When a command that ran for at least
`command_after` seconds (default 10) ends in a pane you are not looking at, you
get a notification such as "tar finished after 2m 14s" with the command line.
It stays until you look at the pane.

- With shell integration that sends OSC 133 marks, a command runs from its
  start mark to the next prompt, so a loop or a list of commands counts as
  one. A non-zero exit status shows as "failed" with the status. Fedora's
  `vte.sh` sends the marks when `VTE_VERSION` is set, as it is in a terminal
  started from Ptyxis or GNOME Terminal.
- Without marks, a command runs while a program other than the shell holds
  the terminal, as bunk sees it once a second, and its exit status is unknown.
- `vte.sh` puts the end mark in `PS1`, so a `PS1` set later in `~/.bashrc`
  drops it and keeps only the start mark. bunk then treats commands as
  unmarked. To keep the marks, add them back after your `PS1` line:
  `PS1='\[\e]133;D;$?\e\\\e]133;A\e\\\]'"$PS1"'\[\e]133;B\e\\\]'`.
- An agent's own run does not notify as a command: the agent's states notify
  instead.
- Commands on the other end of ssh notify only when the remote shell sends
  the marks.

A command that has run for `command_after` seconds also marks its tab in
bunk-gui and gets a badge on its pane in both front ends: "● make running".
When it ends in a pane you are not looking at, the mark turns to "✓ done" or
"✕ failed" with how long it ran, and stays until you look at the pane. The
marks show with notifications turned off; `command_after = 0` turns them off
too.

- Sessions that run commands of their own are not marked: ssh and mosh, a
  container you entered, `sudo -i` or `su`, and a nested shell. Commands
  inside them are, when their shell sends OSC 133 marks.
- Full-screen programs such as vim, htop or less are not marked, and neither
  are agents, whose own states show.
- A REPL such as `python3` counts as a running command.

## Agents on other hosts

Agents on hosts you reach over ssh report to the pane or tab that runs ssh:

- Install bunk on the server and add the same hook snippets there.
- Plain `ssh` works. Without a socket, the hooks write their reports to the
  terminal, and bunk accepts them while ssh or mosh runs in the pane.
- `bunk ssh host` (or `bunk-gui ssh host`) also gives the remote shell the
  pane's token, so its reports count even when ssh runs behind another
  program.
- The tab shows the remote agent's name, repo and branch.
- bunk's TUI on the server, or inside a bunk-gui tab, shows its most urgent
  pane on the tab or pane around it.
- Inside tmux on the server, set `allow-passthrough all`. mosh drops the
  reports.
- [remote-agent-status.md](remote-agent-status.md) describes the design.
