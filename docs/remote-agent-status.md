# Remote agent status

This page records how an agent's status on a remote machine reaches the
local tab. It describes the problem, the options that were compared, the
decision and what is built. [agent-hooks.md](agent-hooks.md#agents-on-other-hosts)
has the setup steps.

The goal is this case. You ssh from a bunk-gui tab to a server and run
Claude Code there, directly or inside bunk's terminal UI. The local tab then
shows "claude needs you", as it does for a local agent.

## The problem

Locally, an agent's hooks run `bunk notify`, which reaches the front end over
a Unix socket. Agents without hooks are covered by inference from the
foreground process, output timing, bells and OSC 9/99/777.

Over ssh, neither path worked.

- The hooks on the server skipped the report. `BUNK_NOTIFY_SOCKET` and
  `BUNK_NOTIFY_SESSION` do not cross ssh, and the local socket is not
  reachable from the server anyway.
- The foreground process is `ssh`, so the session knew no agent. The session
  ignored OSC 9/99/777 and bells while it knew no agent. A spike below
  confirmed this.
- The repo and branch came from the working directory of the local `ssh`
  process, not from the remote agent.

A remote agent therefore showed as `ssh`, with the location `⇄ ssh build01`
and no state. A remote bunk TUI drew its own badges inside the tab, but the
tab rail still showed `ssh`.

The design had to cover these cases.

| Case | Wanted result |
| :- | :- |
| An agent runs in a remote shell | The local tab shows the agent's name, state, notice, repo and branch |
| A remote bunk TUI runs several agents | The local tab shows the most urgent of its panes |
| A local bunk TUI runs ssh | The TUI pane's badge shows the remote agent |
| A local bunk TUI runs inside a bunk-gui tab | The tab shows the TUI's most urgent pane instead of `bunk` |
| A remote agent needs you | A desktop notification appears, as for a local agent |

## What the spikes showed

The spikes ran on a Fedora workstation, on a local PTY and with tmux 3.8.
sshd was not running and mosh was not installed, so neither was tested end
to end. ssh copies the remote PTY's output to its own stdout unchanged. A
sequence that reaches a local PTY intact therefore reaches it intact over
ssh too.

| Spike | Result |
| :- | :- |
| A script named `ssh` prints OSC 9 and BEL in a bunk session | The status stays empty: no agent, no state and no notice |
| The same script named `claude` | The status is needs-you, with the OSC 9 text as the notice |
| A hook started with `setsid`, with stdio on `/dev/null`, opens `/dev/tty` | It fails with "No such device or address" |
| The same hook writes OSC 1337 to the terminal named by `/proc/<agent pid>/fd/1` | The sequence reaches the bunk session byte for byte |
| Claude Code's own Bash tool processes | They run in a new session with no controlling terminal, and `/dev/tty` fails |
| tmux 3.8 with default options (`-f /dev/null`) | It drops OSC 1337, 9, 99, 777 and a private OSC 7777, and passes BEL |
| The same tmux, report wrapped in `ESC P tmux; … ESC \` | With `allow-passthrough on` the report arrives unwrapped; without it, tmux drops it |

tmux also kept the OSC 2 title for its own status line instead of passing it
on. Agent hooks may run without a controlling terminal, as Claude Code's
tools do. The spike code was removed after the runs; the tests below now
cover the same ground.

## Options compared

| | Option 1: in-band report | Option 2: socket forward | Option 4: inference over ssh |
| :- | :- | :- | :- |
| How it works | The CLI writes the report line to its terminal as an OSC 1337 user variable | `ssh -R` forwards a remote Unix socket to the local notify socket | OSC 9/99/777 count as needs-you while ssh or mosh runs in the pane |
| Server setup | bunk binary and hooks | bunk binary and hooks | the agent must send OSC 9/99/777 |
| Server settings | none | Unix socket forwarding allowed, and AcceptEnv without a wrapper | none |
| States shown | all five, with agent, repo and branch | all five, with agent, repo and branch | needs-you only |
| Nested ssh and containers | the bytes pass every layer that copies them | a forward per hop, a mounted socket per container | works |
| tmux | needs `allow-passthrough all` | works until a reattach leaves a stale socket path | dropped |
| mosh | lost | lost | lost |
| Remote bunk TUI | relays through its output | relays as a socket client | no relay |
| Who can fake a report | see below | your user on the server | any output in an ssh pane |

Option 3 was a setup helper for either transport: `bunk ssh`, or bunk-gui
noticing ssh in a pane. bunk cannot change the environment of a remote shell
that already runs. An `ssh` wrapper on each pane's PATH would also catch git, rsync and scp.
Only the explicit command was kept.

Option 2 fails on servers that turn forwarding off and leaves socket files in
`/tmp`. mosh closes the ssh connection after it starts, so neither transport
survives it.

## Decision

The user chose these points, and they are built:

| Point | Decision |
| :- | :- |
| Transport | Option 1: `OSC 1337 ; SetUserVar=bunk_notify=<base64 of the JSON line> BEL` |
| Token | A report with a token this front end issued counts in any pane. A report without one counts only while ssh or mosh runs in the pane, so plain ssh works. |
| Delivering the token | `bunk ssh` exports the pane's token with a remote command, not with `SetEnv` |
| Inference over ssh | Option 4 for OSC 9/99/777; bells from the other end do not count |
| Acknowledgement | Focusing the local tab clears the local copy only; focus reports are a follow-up |
| Remote bunk TUI | Relays its most urgent pane, inside bunk-gui tabs and inside a local TUI |
| Desktop notifications | Remote reports change `Status` like local ones and carry `Status.Host` |

A report whose token this front end did not issue is treated like a report
without one. It counts while ssh or mosh runs in the pane, as a tokenless
report would. This keeps agents in a long-running remote tmux working after
a restart of bunk-gui, without letting anything more through.

## How it works

### The report

- `bunk notify` and `bunk-gui notify` report over the socket when
  `BUNK_NOTIFY_SOCKET` and `BUNK_NOTIFY_SESSION` are set and the socket
  answers.
- Otherwise they write the report in-band when `BUNK_NOTIFY_SESSION` or
  `SSH_CONNECTION` is set. sshd sets `SSH_CONNECTION` in every session, so
  plain ssh needs no setup beyond the hooks.
- The line is the socket protocol's JSON. It adds optional `agent`,
  `host`, `repo`, `branch` and `cwd` fields of up to 128 bytes each. Package
  `internal/notify/wire` holds the format for both the socket and the
  in-band path.
- The CLI names the agent by walking up its parent processes and matching
  them against the agent list. It takes the repo and branch from its working
  directory. It sets `host` only when `SSH_CONNECTION` is set.

### Opting in on the server

The hook snippets used to run only when `BUNK_NOTIFY_SESSION` was set. They
now run when `BUNK_NOTIFY_SESSION` or `SSH_CONNECTION` is set:

```
[ -z "$BUNK_NOTIFY_SESSION$SSH_CONNECTION" ] || ! b=$(command -v bunk || command -v bunk-gui) || "$b" notify working
```

One snippet works on the local machine and on servers, so dotfiles can share
it. When neither binary is installed the hook does nothing and succeeds,
instead of failing with "not found".

### Finding the terminal

A hook may have no controlling terminal, as the spike shows. The CLI looks
for a terminal in this order.

1. It opens `/dev/tty`.
2. It walks up at most 16 parent processes. It stops at the first whose
   stdin, stdout or stderr is a `/dev/pts/*` or `/dev/tty*` device. It opens that
   device with `O_NOCTTY`. This step needs `/proc`.
3. When it finds no terminal, it does nothing and exits 0.

The CLI writes the whole sequence in one `write` call. Linux handles one
write to a terminal under a lock, so other processes' writes do not split
it. Under tmux (`$TMUX`) the CLI wraps the sequence for tmux passthrough and
doubles each ESC. Under GNU screen (`$STY`) it uses screen's DCS
passthrough.

### Choosing the sequence

| Candidate | Current users | Other terminals |
| :- | :- | :- |
| OSC 1337 `SetUserVar=bunk_notify=…` | iTerm2 and WezTerm store user variables | Others ignore OSC 1337, which iTerm2 tools print widely |
| A private number such as OSC 7777 | None known | Ignored, but no registry stops another program from taking the number |
| OSC 99 with kitty's fields | kitty notifications | kitty shows a desktop notification for every report, including working |
| OSC 9 or OSC 777 `notify` | iTerm2, Windows Terminal, VTE, urxvt | These terminals show desktop notifications |

OSC 1337 `SetUserVar` fits best: the variable name keeps it apart, and the
base64 value needs no escaping. bunk's image decoder reads only the file
transfer forms, such as `File=`. tmux and mosh drop every candidate
unwrapped, so the number does not change compatibility.

### In the session

- The OSC scanner hands OSC 1337 payloads to the session next to OSC
  9/99/777. Only `SetUserVar=bunk_notify=` is read; images are unaffected.
- `Options.KnownToken` tells the session which tokens its front end issued.
  Both front ends pass `notify.Server.Issued`, which remembers every token
  registered since the server started.
- The session accepts a report with a known token in any pane. It accepts
  any other report while `ssh`, `mosh` or `mosh-client` is the foreground
  process.
- The report belongs to the foreground process group it arrived in, as a
  socket report does. In an ssh pane that is `ssh`, so the report ends when
  ssh exits.
- The report's agent, host, repo, branch and directory replace the values
  taken from the local `ssh` process. `Status.Host` is new. bunk-gui's tab
  keeps the location `⇄ ssh <destination>` as typed, and shows `⇄ <host>`
  only when it knows no destination.
- These fields hold until `clear` or a change of the foreground process.
  Acknowledging drops the state and notice but keeps them. Inference then
  treats the remote agent like a known local one: output timing gives
  working and idle.

### Inference over ssh

- While ssh or mosh runs in the foreground, OSC 9/99/777 mark the pane
  needs-you. The label is `agent` when no agent is known.
- Bells from the other end do not count, because a remote shell rings one on
  a failed tab completion.
- The request ends when ssh exits, and with a `clear` report. A remote bunk
  TUI sends an OSC 777 alert next to its relayed report, and the `clear`
  drops both.

### Remote bunk TUI

- A TUI started with `BUNK_NOTIFY_SESSION`, or on a host reached over ssh,
  relays its panes. It keeps the inherited token for the relay and still
  gives each pane its own.
- On every frame it finds the most urgent pane: needs you, then done,
  failed, working and idle. On a tie the active pane wins, then layout
  order.
- When that pane's report changes, the TUI writes one in-band report to its
  host terminal. Its OSC 777 alerts go out beside it. It writes from the
  render goroutine, so the report never splits tcell's output.
- The report carries that pane's agent, notice, repo, branch and directory,
  and the host when the TUI runs over ssh.
- When no pane has an agent, and when the TUI exits, it sends `clear`.
- The same relay shows a local TUI's agents on its bunk-gui tab. A local
  TUI also shows a remote TUI's agents on its pane badge.

### Acknowledgement

An agent in a remote shell needs nothing sent back. Focusing the local tab
drops the local report, as for a local agent. The remote agent keeps no
state to clear, and its next hook replaces the report.

A remote bunk TUI keeps state per pane. Acknowledging the local tab drops
only the local copy. The TUI reports again when its most urgent pane
changes, for example when you focus that pane inside the TUI.

### Desktop notifications

Remote reports change the session's status like local ones and run the
status-changed hook. `internal/alert` therefore notifies for them as for
local agents. Its `host` helper prefers `Status.Host` over `Status.SSHHost`. A
notification then names the machine the agent runs on, past any jump host:
"claude on build01 needs you".

### `bunk ssh`

`bunk ssh [ssh options] destination` runs ssh with `-t` and this remote
command:

```
env BUNK_NOTIFY_SESSION=<token> sh -c 'exec "${SHELL:-/bin/sh}" -l'
```

`env` sets the variable whatever the login shell is, since fish and csh have
no `export`.

- Outside bunk, it runs ssh unchanged.
- It leaves ssh unchanged for a command of the user's own. It does the same
  for `-N`, `-W`, `-O`, `-G`, `-V`, `-Q`, `-T`, `-s` and `-f`. Reports from such a
  session still count without a token while ssh runs in the pane.
- It also leaves ssh unchanged when `ssh -G` shows a `RemoteCommand` or a
  session type other than the default.
- A token that is not hex is not used, so the remote command never needs
  quoting.
- With the remote command, the server may skip its message of the day.
- `bunk-gui ssh` is the same command.

### Compatibility

| Layer | Result |
| :- | :- |
| ssh, ProxyJump, nested ssh | Works; tokenless reports need ssh in the local pane's foreground |
| `exec -t` in podman, docker or kubectl | The bytes pass; bunk must exist inside, and the token or `SSH_CONNECTION` must be passed in. Not tested |
| tmux | Works with `allow-passthrough all` (tested); without it reports are lost and nothing stray appears |
| GNU screen | Wrapped in screen's passthrough; not tested |
| mosh | Lost; mosh sends screen updates instead of the byte stream |
| A terminal other than bunk | iTerm2 and WezTerm store an unused user variable; other terminals ignore the sequence |

## Where the changes are

| Area | Change |
| :- | :- |
| internal/notify/wire | The report line, its in-band form and the tmux and screen wrapping |
| internal/notify | The in-band writer and terminal search (inband.go), `bunk ssh` (ssh.go), `Server.Issued`, the new snippet guard |
| internal/session | `Options.KnownToken`, in-band parsing, the remote fields, `Status.Host`, inference over ssh, `AgentOf` and `GitHead` for the CLI |
| internal/alert | `host` prefers `Status.Host` |
| bunk's TUI | The relay (relay.go), `KnownToken`, `bunk ssh` (cmd_ssh.go), a final `clear` on exit |
| bunk-gui | `KnownToken`, the tab location from `Status.Host`, `host` in the report JSON and the `status` script action, `bunk-gui ssh` |

## Follow-ups

- Send focus reports (DECSET 1004) from bunk-gui to the focused pane, as
  bunk's TUI already does. A remote TUI could then acknowledge its active
  pane on focus-in and report its most urgent pane again on focus-out.
- Let the relay report how many panes share the most urgent state. A tab
  could then show "2 need you".
- Use the relay's urgency order for bunk-gui's split tabs, which show the
  first pane that has an agent.
- Find the agent's terminal on macOS servers, which have no `/proc`.
- Test GNU screen and container exec end to end.

## Tests

| Test | What it checks |
| :- | :- |
| `wire` package tests | Encode and decode, field caps, the worst case under 4096 bytes, the in-band framing, tmux and screen wrapping |
| `TestCLI` (notify) | Socket, in-band with and without a token, a gone or crashed socket falling back in-band, tmux wrapping, no terminal |
| `TestHookSnippets` (notify) | The guard runs inside bunk and over ssh, and does nothing without a binary |
| `TestSSHArgs`, `TestSSHConfigRunsCommand` (notify) | Destination parsing, pass-through options, `ssh -G` checks |
| `TestInBandOverSSH` (notify) | A fake ssh runs a fake claude whose hook has no controlling terminal; the report names agent, host, repo and branch |
| `TestInBandThroughTmux` (notify) | A private tmux server passes the report with `allow-passthrough all` and drops it without |
| `TestInBandAccepted`, `TestInBandRemoteFields`, `TestRemoteFieldsEndWithForeground`, `TestRemoteInference`, `FuzzInBand` (session) | Acceptance rules, remote fields through acknowledge and clear, foreground changes, OSC 9 and BEL over ssh |
| `TestInBandThroughRun` (session) | A child on a real PTY reports in-band |
| `TestBunkNotifyInBand` (bunk) | `bunk notify` writes one report and nothing else to a terminal of its own |
| `TestAgentRelay`, `TestMostUrgent` (bunk) | Urgency order, ties, one report per change, `clear` on exit |
| `TestNestedTUIRelay` (bunk) | A real TUI in a session relays a pane's report, the acknowledgement and its exit |
| alert `For` cases (alert) | "claude on build01 needs you" from `Status.Host`, past a jump host |
| `TestGUIRemoteAgent` (bunk-gui) | A fake ssh pane's tokenless report on the tab, a tokenless report outside ssh dropped, a token report shown |
