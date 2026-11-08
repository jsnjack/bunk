# Agent status, hooks and notifications in the code

How `internal/session` infers an agent's state, how hook reports through
`internal/notify` override it, how bunk sets up hooks only when the user asks,
how agents on ssh hosts report in-band, and how `internal/alert` and both
front ends decide when to notify. Read it before changing agent detection,
`internal/notify`, `internal/alert`, `internal/desktop` or the notification
code of either front end. [agent-status.md](agent-status.md) and
[agent-hooks.md](agent-hooks.md) describe the same features for users.

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

## Where agent status comes from

- Agent status is inferred unless the agent reports it. `session.Status`
  names the foreground agent from `DefaultAgents` (add a CLI there, or via
  `Options.Agents`), counts output in the last 2 s as working, and treats
  OSC 9/99/777 notifications and BEL from an agent as "needs you". VTE's
  "Command completed" notices are ignored. Agent hooks can report state
  through `internal/notify` (`docs/agent-hooks.md`); `Session.Notify` takes
  priority over inference until `AcknowledgeAs`, `clear` or a foreground
  change.
  - A reported needs-you means the agent still waits, so looking at the
    pane leaves it: focus, Alt+A, a notification click and keys that answer
    nothing (`AckSeen`) keep it, Enter (`AckAnswer`) and Esc or Ctrl+C
    (`AckCancel`) end it. A held one (`Report.Held`, from the PreToolUse
    of AskUserQuestion or ExitPlanMode) survives Enter, which may only move
    to the next question, and ends with PostToolUse's report or a cancel.
    A plain needs-you from the same group while it waits keeps it held
    with its notice: Claude Code's `permission_prompt` notification follows
    AskUserQuestion by about 6 s. Every tool's PostToolUse and
    PostToolUseFailure report working, because in auto mode that
    notification can come for a call auto mode then approves; a subagent's
    tools (`agent_id` in the input) report nothing.
    Done, failed and inferred requests clear at any acknowledgement.
    bunk-gui draws no frame around a pane that needs the user; the badge,
    the tab and the rail show it. Every change of the agent state runs `Hooks.StatusChanged` once:
  the reader, its idle timer, the 1 s process tick and `Notify` share one
  record of the last announced state. Without hooks, Claude Code only sends
  OSC notifications to terminals it recognises; set its
  `preferredNotifChannel` to `terminal_bell`.
  - A known local agent whose working stretch lasted `Timing.Done`
    (`DoneAfter`, 10 s) is done, not idle, once its output stops. A stretch
    runs from the first output after `Timing.Idle` of silence, after the
    user's input (`WriteInput`) or after `Acknowledge`, to the last output.
  - The inferred done holds like a reported one: until `Acknowledge`, new
    output, `clear` or a foreground change. Its notice is empty, and
    `Status.Reported` is false.
  - An agent that has reported through hooks is never inferred done, until
    `clear` or until its process group leaves the foreground. The hook
    snippets report `working` as each turn starts, so a turn ends with the
    agent's own `done` alone and alerts once.
  - The hook snippets run `bunk-gui notify` when bunk-gui is on PATH, else
    `bunk notify`; both reach the socket `BUNK_NOTIFY_SOCKET` names. The
    Claude snippet reports AskUserQuestion and ExitPlanMode as needs-you
    from PreToolUse, with the question as the notice, and work again from
    PostToolUse. `notify done` reports working instead when the Stop input
    lists `background_tasks`: Claude resumes when they end. An artifact's
    comment watch ("live updates for artifact …") does not count, because
    it runs until the session ends.
  - Codex runs PermissionRequest before it picks a reviewer, so `notify
    needs-you -` reads the turn's `approvals_reviewer` from the last
    `turn_context` of the hook's `transcript_path` (`notify/codex.go`).
    Any reviewer but `user` (Codex's automatic reviewer) reports working;
    without a transcript or reviewer the request asks the user.

## Setting up agent hooks

- Users set up agent hooks with guidance, not behind their back: bunk
  writes an agent's configuration only when the user asks for it.
  - `notify.CheckHooks` reads each agent's configuration (honouring
    `CLAUDE_CONFIG_DIR`, `CODEX_HOME`, `COPILOT_HOME`) and calls an agent's
    hooks installed when every hook of the current snippet is there by
    event, matcher and command, outdated when only some bunk hooks are, and
    missing otherwise. Codex's `config.toml` counts beside its
    `hooks.json`, and every `*.json` in Copilot CLI's `hooks/`.
  - `bunk notify hooks` lists the states and next steps; `bunk notify hooks
    install AGENT` (`notify.InstallHooks`) sets them up. Codex's and Copilot
    CLI's files of bunk's own are written whole, replacing older bunk hooks.
    Settings files that hold other settings or hooks (Claude Code's and
    Gemini CLI's settings.json, a Codex hooks.json with the user's hooks)
    get a merge (`merge.go`): handlers whose command runs bunk's notify CLI
    (`bunkCommand`, any version) go, the snippet's are appended, and every
    other key, event, group and handler keeps its order. The old file is
    saved as `<file>.bunk-backup`, the mode and indentation stay, a
    symbolic link's target is written, and a file that is not JSON is
    refused. A Copilot CLI file with other hooks is refused too.
  - bunk-gui's Preferences show the same states with Install and Copy
    snippet buttons and a link to the setup guide.
  - bunk-gui offers the install in a banner above the panes (`banner.go`,
    under the restore banner): at start for agents whose bunk hooks or
    bunk-present skill are outdated, and when a tab first shows an agent
    whose hooks or skill are missing or outdated, unless the agent is
    remote; hooks that are only missing are left out for an agent that
    reported. It names each agent, what it lacks (`agentSetup`) and the
    files it changes; Set up or Update runs `InstallHooks` and
    `InstallSkill` as needed and toasts the result, Not now hides it until
    the next start. `hooks_hint = false` turns it off.
  - Window tests pass `--agent-home`, a directory with every agent's hooks
    and skill installed, so no setup banner comes between a scenario's
    steps; the hook tests pass an empty one or one with outdated hooks or
    an outdated skill.

## Agents on other hosts

- Agents on hosts reached over ssh report in-band
  (`docs/remote-agent-status.md`). Without a reachable socket, `bunk notify`
  writes the report line as `OSC 1337 ; SetUserVar=bunk_notify=<base64>` to
  the agent's terminal (`/dev/tty`, else the nearest parent's), wrapped for
  tmux or screen. The snippets run when `BUNK_NOTIFY_SESSION` or
  `SSH_CONNECTION` is set.
  - The session accepts an in-band report whose token
    `Options.KnownToken` knows (`notify.Server.Issued`) in any pane, and any
    other while `ssh`, `mosh` or `mosh-client` is the foreground process.
  - A report's `agent`, `host`, `repo`, `branch` and `cwd` replace the local
    ssh process's values in `Status` (`Status.Host` is new) until `clear` or
    a foreground change; acknowledging keeps them.
  - While ssh or mosh runs, OSC 9/99/777 mean needs-you without a known
    agent; bells from there never count.
  - bunk's TUI relays its most urgent pane (`relay.go`) when it inherited a
    token or runs over ssh, and sends `clear` on exit. `bunk ssh` exports the
    pane's token with a remote command.

## Agent status in bunk's TUI

- The TUI shows agent status per pane. `renderPane` reads
  `Session.Status` before taking the session lock; the badge and the
  needs-you/done separator colours use that copy (`Pane.agent`).
  `Hooks.StatusChanged` only wakes the render loop. Focus or typing calls
  `AcknowledgeAs` (`session.KeyAck` sorts keys into answers, cancels and
  the rest); `next_needs_you` (Alt+A) jumps. bunk serves the notify
  socket like bunk-gui, and `bunk notify` runs the same CLI.

## Notifications and command marks

- An agent notifies the user when nobody is looking at it.
  `internal/alert`'s Tracker decides for both front ends, from the status
  after every `Hooks.StatusChanged`, so inferred states, hook reports and
  any other source of a status change notify alike.
  - A source alerts when its agent enters a `[notifications]` state, or
    its notice changes there, while it is not watched. In bunk-gui a pane
    is watched while the window is active and the pane is the focused pane
    of the selected tab; in bunk a pane is watched while it is active and
    the host terminal has the focus (DECSET 1004).
  - The title names the agent and its host ("claude on gpu01 needs you"):
    `Status.Host` from a remote report, else `SSHHost`. The body is the notice, or a default for the state,
    and "repo ⎇ branch".
  - Watching the source, the state leaving those states, turning
    notifications off and the source going away withdraw the notification.
    The same state notifies again only 10 s (`alert.DefaultRepeat`) after
    the last one unless the user watched the source; until then it is held,
    and `Tracker.Next` says when to look again.
  - A command that ran `command_after` (`Settings.CommandAfter`) or longer
    notifies once when it ends while its pane is not watched: "tar
    finished after 2m 14s", or "failed" with a non-zero exit status, the
    command line as the body. It stays until the user watches the pane, a
    newer alert replaces it or command alerts are turned off; a later
    short command leaves it up.
  - Commands also get marks (`Tracker.Mark`), with notifications on or
    off. A command that has run `command_after` is marked running; a long
    one that ended while its pane was not watched is marked done or
    failed until the user watches it. `Tracker.Next` includes the moment a
    running command crosses `command_after`, so front ends wake then.
    Both front ends show the mark in the pane's badge (`badges.Input`
    `CommandState`, labelled by `badges.CommandLook`) where no agent runs;
    bunk-gui also shows it in the tab, in the agent states' order and
    colours.
  - `Status.Command` is the last command that ended (`command.go`). With
    OSC 133 marks (vte.sh, which runs only when `VTE_VERSION` is set), a
    command runs from 133;C to 133;D, loops and lists included, and has
    an exit status. Without marks, it runs while a group other than the
    shell's holds the foreground, as the process poll sees it, without an
    exit status. A 133;C marks a command only after a 133;D since the
    previous 133;C: vte.sh puts 133;D in PS1, so a PS1 set after it drops
    that mark alone, and a command its 133;C starts then ends through the
    poll. The poll names it after the first group it sees that
    started after the last prompt, so the ssh carrying a remote shell's
    marks names nothing. A command an agent ran in is not recorded: the
    agent's states notify instead.
  - `Status.Running` is the command running now, while it is one the user
    may wait on. Agents and full-screen programs (the alternate screen)
    are left out, and so is a foreground that runs a session of its own
    (`carrier`): ssh or mosh, a container entered, a shell without a
    script, sudo, su or doas starting a shell. A command its shell's
    marks start after that session appeared counts, such as one in a
    remote or nested shell with vte.sh.
  - bunk-gui keeps one notification per pane on org.freedesktop.Notifications
    (`desknotify.go`), keyed by the pane's ID. The user watches a pane while
    it is the focused pane of the selected tab in the active window, as in
    bunk. A click runs `app.show-pane` with that ID: the window takes the
    focus with the shell's activation token, selects the pane's tab and
    focuses the pane.
    GNotification is not used: on GNOME, GLib sends it through
    org.gtk.Notifications, where GNOME Shell needs an installed desktop file
    and runs the action through the application's bus name, which a
    non-unique application never owns; GLib's freedesktop backend drops the
    activation token. A missing server only logs at debug level.
  - The window is not marked as needing attention. GTK 4 has no urgency
    hint, and on Wayland a present without a token makes GNOME Shell post a
    second "bunk-gui is ready" notification.
  - bunk decides in the render pass, which reads every pane's status under
    `app.mu`, and each alert takes one of two ways, never both.
  - On the user's desktop, bunk keeps one desktop notification per pane on
    org.freedesktop.Notifications, with the same replace and withdraw rules
    as bunk-gui. It uses them when the session bus is reachable
    (`DBUS_SESSION_BUS_ADDRESS`, else the socket `$XDG_RUNTIME_DIR/bus`),
    `SSH_CONNECTION` is unset and `gdbus` is on PATH (`desktop.SessionBus`,
    `startDesktopAlerts`). `desktop.Notifier` runs `gdbus call` with typed
    GVariant text, on one goroutine at a time, with at most one waiting
    request per pane; gdbus's output goes to buffers, never the terminal.
    Quitting withdraws the notifications, waiting up to 2 s.
  - The desktop notifications have no actions. Receiving a click would need
    a process listening on the bus for as long as the notification lives,
    and bunk cannot raise its host terminal's window anyway.
  - A failed Notify call logs at debug level and sends that alert as OSC 777
    through `app.oscBuf` with the next frame. A call that gets no answer in
    3 s does not fall back, because the server may have shown it.
  - Otherwise, over SSH or without a bus or gdbus, bunk writes
    `alert.OSC777` to the host. Control and bidi characters go, ';' becomes
    ',', and the title and body are cut to 120 and 360 bytes. foot,
    WezTerm, Ghostty and urxvt show it; VTE 0.84 (GNOME Terminal, Ptyxis on
    Fedora 44) reads OSC 777 notify only as "Command completed".
  - Both front ends clean desktop text with `alert.DesktopText`: the same
    character rules and limits, the body escaped as markup for GNOME Shell.
