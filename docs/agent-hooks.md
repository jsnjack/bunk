# Agent hooks

bunk shows what the agent in each pane or tab is doing: working, idle, needs
you, done or failed. bunk's terminal UI shows it on the pane's badge and
borders; bunk-gui shows it on its tabs. Without hooks, both infer the state
from output, bells and OSC 9/99/777 notifications. With the hooks below, the
agent reports its own state, and the report takes priority over inference.
The same hooks work for agents on hosts you reach over ssh; see
[Agents on other hosts](#agents-on-other-hosts).

Run `bunk notify hooks <agent>` (or `bunk-gui notify hooks <agent>`) to print
a snippet. The snippets on this page are that output; a test keeps them in
sync.

## Set up

Do this once per machine, and on every host you reach over ssh where an
agent runs.

1. See where each agent stands. `bunk notify hooks` (or `bunk-gui notify
   hooks`) lists every agent with its hooks file and its state: installed,
   outdated or missing, and the next step for the ones that need one.
   bunk-gui shows the same list in Preferences (Ctrl+,) under Agent hooks.
2. Install the hooks of each agent you use:

   | Agent | File | How |
   | :- | :- | :- |
   | Claude Code | `~/.claude/settings.json` | Merge the `"hooks"` key of `bunk notify hooks claude` |
   | Codex | `~/.codex/hooks.json` | `bunk notify hooks install codex`, then `/hooks` in Codex to trust the hooks |
   | Copilot CLI | `~/.copilot/hooks/bunk.json` | `bunk notify hooks install copilot` |
   | Gemini CLI | `~/.gemini/settings.json` | Merge the `"hooks"` key of `bunk notify hooks gemini` |

   - Codex and Copilot CLI read hooks from a file of their own, so `install`
     (or Install in bunk-gui's preferences) writes it. It replaces a file
     with older bunk hooks, and it leaves alone a file that holds hooks of
     your own: merge the snippet into that one by hand.
   - Claude Code and Gemini CLI keep hooks in their settings file, next to
     your other settings, so bunk never writes it. Copy the snippet (Copy
     snippet in bunk-gui, or the command above) and merge it: add the
     `"hooks"` key if the file has none, else add each event's entries to
     the lists already there.
   - `CLAUDE_CONFIG_DIR`, `CODEX_HOME` and `COPILOT_HOME` move those files,
     and bunk looks for them there.
3. Check again: `bunk notify hooks` shows `installed`. A running agent
   picks the hooks up when it starts, so restart it.

When bunk's snippets change in a new release, `bunk notify hooks` shows the
agent as outdated, with the hooks it lacks. Repeat step 2 for it.

- bunk-gui reminds you once per agent and window, with a toast, when a tab
  shows an agent whose hooks are missing or outdated. The toast's Set up
  button opens Agent hooks in the preferences. `hooks_hint = false` in
  bunk-gui's config.toml, or the switch in that section, turns the reminder
  off.
- The hooks files are plain files, so a dotfile manager can keep them in
  sync between machines. The Codex and Copilot files hold only bunk's hooks;
  the settings files of Claude Code and Gemini CLI hold your other settings
  too.
- Without hooks, bunk still shows the agent's state, inferred from its
  output; [How reports and inference combine](#how-reports-and-inference-combine)
  says what changes with hooks.

## How bunk's terminal UI shows the state

A badge at the top right of the pane names the agent and its state. The
needs-you and done badges are filled; the separators around the pane take
the same colour, over the focused pane's colour.

| State | Badge | Separators |
| :- | :- | :- |
| working | `● claude working`, blue text | unchanged |
| idle | `○ claude idle`, grey text | unchanged |
| needs you | `◐ claude needs you · <notice>`, dark on amber | amber |
| done | `✓ claude done · <notice>`, dark on green | green |
| failed | `✕ claude failed · <notice>`, red text | unchanged |

- The notice fills the room the other badges leave, up to two thirds of the
  row, and ends in "…" when cut. Narrow panes drop the notice, then the
  agent's name, then the label.
- Focusing the pane (Alt+arrows, a click, the jump key) or typing into it
  acknowledges the state and clears needs-you and done.
- Alt+A (`next_needs_you` in `[keys]`) focuses the next pane whose agent
  needs you, else the next one that is done. It leaves zoom first. Set it to
  `"none"` to send Alt+A to the pane instead.

## How a report travels

- Each front end, bunk's TUI and bunk-gui alike, listens on a socket at
  `$XDG_RUNTIME_DIR/bunk/notify-<pid>.sock`. The directory has mode 0700 and
  the socket 0600.
- Every pane gets `BUNK_NOTIFY_SOCKET` (the socket path) and
  `BUNK_NOTIFY_SESSION` (a random 128-bit token) in its environment. Values
  inherited from the parent process are removed first. A bunk TUI running
  inside a bunk-gui tab therefore reports to the TUI, not the GUI.
- The agent's hook runs `bunk-gui notify <event> [message | -]`, or
  `bunk notify …` where bunk-gui is not installed. Both binaries run the same
  CLI. It sends one JSON line and exits without waiting for a reply.
- The server accepts clients with its own UID only (`SO_PEERCRED`). It maps
  the token to its session and calls `Session.Notify`. Unknown tokens and
  malformed lines are dropped.
- The server reads one connection at a time. Reports arrive in the order the
  hooks connected, so `done` from Stop comes before `clear` from SessionEnd.
- Where no socket is reachable, as on a host reached over ssh, the CLI writes
  the line to its terminal. It travels as an OSC 1337 user variable. The
  session reads it from the pane's output. The section
  [Agents on other hosts](#agents-on-other-hosts) describes this path.

## Wire format

Each report is one JSON object on one line, at most 4096 bytes including the
newline. Unknown fields are ignored.

```json
{"v":1,"session":"0123456789abcdef0123456789abcdef","event":"needs-you","message":"Allow npm install?"}
```

| Field | Type | Meaning |
| :- | :- | :- |
| `v` | number | Protocol version, always `1` |
| `session` | string | Token from `BUNK_NOTIFY_SESSION`, at most 64 bytes |
| `event` | string | One of the events below |
| `message` | string | Optional notice text; the CLI cleans it and cuts it to 1024 bytes |
| `agent`, `host`, `repo`, `branch`, `cwd` | string | Optional, in in-band reports only: where a remote agent runs, each cut to 128 bytes |

In-band, a report travels as `ESC ] 1337 ; SetUserVar=bunk_notify=<base64 of
the line> BEL`, and `session` may be missing. iTerm2 and WezTerm store such a
sequence as a user variable; other terminals ignore it.

| Event | `Status.State` | Notice |
| :- | :- | :- |
| `working` | `AgentWorking` | none |
| `idle` | `AgentIdle` | none |
| `needs-you` | `AgentNeedsYou` | the message, or "Needs your attention" |
| `done` | `AgentDone` | the message, if any |
| `failed` | `AgentFailed` | the message, if any |
| `clear` | inference again | inferred |

## How reports and inference combine

- While a report holds, `Status` shows it and sets `Status.Reported`. Output
  timing, BEL and OSC notifications do not change it.
- `Session.Acknowledge` drops the report, as it drops an inferred attention
  request. Inference shows the pane until the next report. Both front ends
  acknowledge a needs-you or done pane when the user focuses it or types
  into it.
- The `clear` event drops the report the same way.
- A report belongs to the foreground process group it arrived from. When
  that group leaves the foreground, the report is dropped.
- A report from a process that bunk does not recognise labels the pane or
  tab `agent`.
- Inference counts a long working stretch that stops as done (see
  [Other agents](#other-agents)). An agent that has reported is never
  inferred done until `clear` or until its process group leaves the
  foreground, so a turn ends with its own `done` report alone.
- `Session.Notify` runs `Hooks.StatusChanged` once per change of `Status`,
  after releasing the session lock. A repeated report runs nothing.

## CLI

```
bunk notify <event> [message | -]
bunk notify hooks [agent]
```

`bunk-gui notify` takes the same arguments.

- The words after the event are joined with spaces to form the message.
- A message of `-` reads the hook input from stdin, for at most 2 seconds and
  4 MiB.
- When the message is a JSON object, the CLI takes the first non-empty field
  of `message`, `last_assistant_message`, `last-assistant-message`,
  `prompt_response` and `error`. Without one, Claude Code's PreToolUse input
  for AskUserQuestion gives its first question, and for ExitPlanMode "Approve
  the plan: " and the first line of the plan.
- A report prints nothing to stdout, because Claude Code adds
  UserPromptSubmit stdout to the prompt and Gemini CLI parses stdout as JSON.

| Exit status | When |
| :- | :- |
| 0 | Report sent, over the socket or to the terminal |
| 0 | `BUNK_NOTIFY_SESSION` and `SSH_CONNECTION` unset: not inside bunk and not over ssh |
| 0 | No socket reachable and no terminal found for the in-band report |
| 1 | Unknown event, bad arguments, or any other send error (message on stderr) |

A missing or refusing socket means that its front end has exited. The CLI
then writes the report to its terminal, which may still lead to a bunk pane.

The CLI never exits with 2. Claude Code, Codex and Gemini CLI read 2 as
"block this action", which would cancel a prompt or a stop.

Each snippet wraps the command in
`[ -z "$BUNK_NOTIFY_SESSION$SSH_CONNECTION" ] || …`. It runs inside bunk and
on hosts reached over ssh. In other terminals the hook succeeds without
running bunk. `! b=$(command -v bunk-gui || command -v bunk) || "$b" notify`
picks `bunk-gui` when it is on PATH and `bunk` otherwise. Either reaches the
pane's front end, because both send to the socket that `BUNK_NOTIFY_SOCKET`
names. bunk-gui comes first because an older bunk on PATH may lack the
`notify` command. On a server reached over ssh usually only bunk is
installed. When neither is installed, the hook does nothing and succeeds.

## Claude Code

Merge into `~/.claude/settings.json` (or a project's `.claude/settings.json`):

```json
{
  "hooks": {
    "UserPromptSubmit": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "[ -z \"$BUNK_NOTIFY_SESSION$SSH_CONNECTION\" ] || ! b=$(command -v bunk-gui || command -v bunk) || \"$b\" notify working",
            "timeout": 5
          }
        ]
      }
    ],
    "PreToolUse": [
      {
        "matcher": "AskUserQuestion|ExitPlanMode",
        "hooks": [
          {
            "type": "command",
            "command": "[ -z \"$BUNK_NOTIFY_SESSION$SSH_CONNECTION\" ] || ! b=$(command -v bunk-gui || command -v bunk) || \"$b\" notify needs-you -",
            "timeout": 5
          }
        ]
      }
    ],
    "PostToolUse": [
      {
        "matcher": "AskUserQuestion|ExitPlanMode",
        "hooks": [
          {
            "type": "command",
            "command": "[ -z \"$BUNK_NOTIFY_SESSION$SSH_CONNECTION\" ] || ! b=$(command -v bunk-gui || command -v bunk) || \"$b\" notify working",
            "timeout": 5
          }
        ]
      }
    ],
    "Notification": [
      {
        "matcher": "permission_prompt|elicitation_dialog|elicitation_url_dialog|agent_needs_input",
        "hooks": [
          {
            "type": "command",
            "command": "[ -z \"$BUNK_NOTIFY_SESSION$SSH_CONNECTION\" ] || ! b=$(command -v bunk-gui || command -v bunk) || \"$b\" notify needs-you -",
            "timeout": 5
          }
        ]
      }
    ],
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "[ -z \"$BUNK_NOTIFY_SESSION$SSH_CONNECTION\" ] || ! b=$(command -v bunk-gui || command -v bunk) || \"$b\" notify done -",
            "timeout": 5
          }
        ]
      }
    ],
    "StopFailure": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "[ -z \"$BUNK_NOTIFY_SESSION$SSH_CONNECTION\" ] || ! b=$(command -v bunk-gui || command -v bunk) || \"$b\" notify failed -",
            "timeout": 5
          }
        ]
      }
    ],
    "SessionEnd": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "[ -z \"$BUNK_NOTIFY_SESSION$SSH_CONNECTION\" ] || ! b=$(command -v bunk-gui || command -v bunk) || \"$b\" notify clear",
            "timeout": 5
          }
        ]
      }
    ]
  }
}
```

| Claude Code hook | Event | Message |
| :- | :- | :- |
| UserPromptSubmit | `working` | none |
| PreToolUse (AskUserQuestion, ExitPlanMode) | `needs-you` | the first question, or "Approve the plan: " and the plan's title |
| PostToolUse (AskUserQuestion, ExitPlanMode) | `working` | none |
| Notification (permission, elicitation, agent input) | `needs-you` | `message` from stdin |
| Stop | `done`, or `working` while `background_tasks` is not empty | `last_assistant_message` |
| StopFailure (API error) | `failed` | `last_assistant_message`, e.g. "API Error: Rate limit reached" |
| SessionEnd (exit, `/clear`, `/resume`) | `clear` | none |

- A multiple-choice question (AskUserQuestion) and a plan that waits for
  approval (ExitPlanMode) have no notification type of their own. PreToolUse
  runs before Claude Code shows either prompt, and PostToolUse runs after
  the user has answered. The hook prints nothing, so Claude Code takes no
  permission decision from it.
- A rejected plan or a question closed with Esc runs no PostToolUse. The
  keypress acknowledges the pane, so inference shows it until the next
  report.
- A turn can end while shells, subagents or monitors it started still run
  in the background. Claude Code lists them in the Stop input's
  `background_tasks` and resumes the session when they finish, so `notify
  done -` reports `working` until a later Stop lists none. Scheduled
  wakeups (`session_crons`, such as the next round of `/loop`) do not
  count, because they can be far off.
- Claude Code runs all Stop hooks at once. A long one, such as a test
  suite that blocks the stop when it fails, runs while the pane already
  shows done, and Claude resumes after a block with nothing to report
  working. Such a hook can report `working` a second after it starts, so
  that the report lands after done, and `done -` with its own input when
  it lets Claude stop. This repository's
  `.claude/hooks/check-all-on-stop.sh` does both, for Codex and Copilot
  CLI too. It blocks with `{"decision": "block", "reason": …}` on stdout,
  which all three agents take; Copilot CLI treats exit code 2 only as a
  warning.
- Stop does not run when the user interrupts a turn. The keypress
  acknowledges the pane, so inference takes over until the next prompt.
- The snippet leaves out the `idle_prompt` notification. It arrives 60
  seconds after `done` and would mark a finished pane again.
- Hooks inherit Claude Code's environment, so both variables reach them.
- With these hooks, the `preferredNotifChannel` setting no longer matters
  for bunk.

Claude Code documents hooks at <https://code.claude.com/docs/en/hooks>,
including events, input fields, matchers, exit codes and the environment.

## Codex

Save as `~/.codex/hooks.json`, then run `/hooks` in Codex once to trust the
hooks. Codex reads this file next to `config.toml` and merges the hooks of
both, so the file can be deployed whole:

```json
{
  "hooks": {
    "UserPromptSubmit": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "[ -z \"$BUNK_NOTIFY_SESSION$SSH_CONNECTION\" ] || ! b=$(command -v bunk-gui || command -v bunk) || \"$b\" notify working",
            "timeout": 5
          }
        ]
      }
    ],
    "PermissionRequest": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "[ -z \"$BUNK_NOTIFY_SESSION$SSH_CONNECTION\" ] || ! b=$(command -v bunk-gui || command -v bunk) || \"$b\" notify needs-you -",
            "timeout": 5
          }
        ]
      }
    ],
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "[ -z \"$BUNK_NOTIFY_SESSION$SSH_CONNECTION\" ] || ! b=$(command -v bunk-gui || command -v bunk) || \"$b\" notify done -",
            "timeout": 5
          }
        ]
      }
    ],
    "Interrupt": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "[ -z \"$BUNK_NOTIFY_SESSION$SSH_CONNECTION\" ] || ! b=$(command -v bunk-gui || command -v bunk) || \"$b\" notify idle",
            "timeout": 3
          }
        ]
      }
    ],
    "SessionEnd": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "[ -z \"$BUNK_NOTIFY_SESSION$SSH_CONNECTION\" ] || ! b=$(command -v bunk-gui || command -v bunk) || \"$b\" notify clear",
            "timeout": 3
          }
        ]
      }
    ]
  }
}
```

| Codex hook | Event | Message |
| :- | :- | :- |
| UserPromptSubmit | `working` | none |
| PermissionRequest | `needs-you`, or `working` for the automatic reviewer | "Approval requested", with the first line of a Bash command |
| Stop | `done` | `last_assistant_message` |
| Interrupt | `idle` | none |
| SessionEnd | `clear` | none |

- Codex skips new or changed user hooks until you review them with `/hooks`.
- Codex runs PermissionRequest before it routes the request to a
  reviewer. With `approvals_reviewer = "auto_review"`, a model approves
  or denies the request without asking, and no hook reports work again
  until the turn ends. The hook input does not name the reviewer, so
  `notify needs-you -` reads it from the last `turn_context` line of the
  transcript that `transcript_path` names, up to 64 MiB from its end.
  Any reviewer other than `user` reports `working`. Without a transcript
  or a reviewer in it, the request counts as asking the user.
- After the user answers an approval prompt, the keypress acknowledges
  the pane, so inference shows it until the next report.
- Codex releases without hooks support only the `notify` program, which
  reports `agent-turn-complete`. `notify` is a top-level key of
  `config.toml`, so place it above the first `[table]`:

  ```toml
  notify = ["sh", "-c", "[ -z \"$BUNK_NOTIFY_SESSION$SSH_CONNECTION\" ] || ! b=$(command -v bunk-gui || command -v bunk) || \"$b\" notify done \"$1\"", "sh"]
  ```

  Codex appends a JSON payload as the last argument. `sh` passes it on as
  `"$1"`, and the CLI takes its `last-assistant-message`.
- Codex allows Interrupt and SessionEnd hooks 3 seconds at most, so their
  snippet timeout is 3.
- With hooks and `notify` together, `done` arrives twice. The second report
  changes nothing and runs no hook.

Codex documents hooks at <https://learn.chatgpt.com/docs/hooks> (hook
events, inputs, trust) and `notify` at
<https://learn.chatgpt.com/docs/config-file/config-advanced>. The old
`developers.openai.com/codex/...` URLs redirect there.

## Copilot CLI

Save as `~/.copilot/hooks/bunk.json` (`$COPILOT_HOME/hooks/` when set):

```json
{
  "version": 1,
  "hooks": {
    "userPromptSubmitted": [
      {
        "type": "command",
        "bash": "[ -z \"$BUNK_NOTIFY_SESSION$SSH_CONNECTION\" ] || ! b=$(command -v bunk-gui || command -v bunk) || \"$b\" notify working",
        "timeoutSec": 5
      }
    ],
    "notification": [
      {
        "type": "command",
        "matcher": "permission_prompt|elicitation_dialog",
        "bash": "[ -z \"$BUNK_NOTIFY_SESSION$SSH_CONNECTION\" ] || ! b=$(command -v bunk-gui || command -v bunk) || \"$b\" notify needs-you -",
        "timeoutSec": 5
      }
    ],
    "agentStop": [
      {
        "type": "command",
        "bash": "[ -z \"$BUNK_NOTIFY_SESSION$SSH_CONNECTION\" ] || ! b=$(command -v bunk-gui || command -v bunk) || \"$b\" notify done",
        "timeoutSec": 5
      }
    ],
    "sessionEnd": [
      {
        "type": "command",
        "bash": "[ -z \"$BUNK_NOTIFY_SESSION$SSH_CONNECTION\" ] || ! b=$(command -v bunk-gui || command -v bunk) || \"$b\" notify clear",
        "timeoutSec": 5
      }
    ]
  }
}
```

| Copilot CLI hook | Event | Message |
| :- | :- | :- |
| userPromptSubmitted | `working` | none |
| notification (`permission_prompt`, `elicitation_dialog`) | `needs-you` | `message` from stdin |
| agentStop | `done` | none; the input has no message text |
| sessionEnd | `clear` | none |

- The snippet leaves out `errorOccurred`. Its input marks some errors as
  recoverable, and a retried error would show the pane as failed.
- Copilot CLI also runs the hooks of a repository's
  `.claude/settings.json`, after its own and one after another. They get
  Claude Code's input format and `CLAUDE_PROJECT_DIR`, and their `timeout`
  holds (Copilot CLI 1.0.104).
- The Copilot CLI docs do not say whether hooks inherit the CLI's
  environment. If no reports arrive, add a hook running `env > /tmp/hook-env`
  and look for `BUNK_NOTIFY_SESSION`.

Copilot CLI documents hooks at
<https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-hooks-reference>
(events, inputs, file locations) and
<https://docs.github.com/en/copilot/how-tos/copilot-cli/customize-copilot/use-hooks>.

## Gemini CLI

Merge into `~/.gemini/settings.json`:

```json
{
  "hooks": {
    "BeforeAgent": [
      {
        "hooks": [
          {
            "name": "bunk-working",
            "type": "command",
            "command": "[ -z \"$BUNK_NOTIFY_SESSION$SSH_CONNECTION\" ] || ! b=$(command -v bunk-gui || command -v bunk) || \"$b\" notify working",
            "timeout": 5000
          }
        ]
      }
    ],
    "Notification": [
      {
        "hooks": [
          {
            "name": "bunk-needs-you",
            "type": "command",
            "command": "[ -z \"$BUNK_NOTIFY_SESSION$SSH_CONNECTION\" ] || ! b=$(command -v bunk-gui || command -v bunk) || \"$b\" notify needs-you -",
            "timeout": 5000
          }
        ]
      }
    ],
    "AfterAgent": [
      {
        "hooks": [
          {
            "name": "bunk-done",
            "type": "command",
            "command": "[ -z \"$BUNK_NOTIFY_SESSION$SSH_CONNECTION\" ] || ! b=$(command -v bunk-gui || command -v bunk) || \"$b\" notify done -",
            "timeout": 5000
          }
        ]
      }
    ],
    "SessionEnd": [
      {
        "hooks": [
          {
            "name": "bunk-clear",
            "type": "command",
            "command": "[ -z \"$BUNK_NOTIFY_SESSION$SSH_CONNECTION\" ] || ! b=$(command -v bunk-gui || command -v bunk) || \"$b\" notify clear",
            "timeout": 5000
          }
        ]
      }
    ]
  }
}
```

| Gemini CLI hook | Event | Message |
| :- | :- | :- |
| BeforeAgent | `working` | none |
| Notification (`ToolPermission`) | `needs-you` | `message` from stdin |
| AfterAgent | `done` | `prompt_response` |
| SessionEnd | `clear` | none |

- Gemini CLI reads `timeout` in milliseconds.
- Gemini CLI can strip variables whose names contain words such as TOKEN,
  KEY or AUTH. `BUNK_NOTIFY_SOCKET` and `BUNK_NOTIFY_SESSION` avoid them, and
  a test checks the names.
- With `GITHUB_SHA` set or `SURFACE=Github`, Gemini CLI runs in strict mode
  and removes every variable not on its allow list, so reports stop.

Gemini CLI documents hooks at <https://geminicli.com/docs/hooks/reference/>
(events, inputs, exit codes) and <https://geminicli.com/docs/hooks/>
(configuration). Its environment redaction is in
[environmentSanitization.ts](https://github.com/google-gemini/gemini-cli/blob/main/packages/core/src/services/environmentSanitization.ts).

## Agents on other hosts

An agent on a server you reach over ssh reports to the local pane or tab
that runs ssh. [remote-agent-status.md](remote-agent-status.md) explains the
design.

Set up the server once:

- Install bunk there, so the hooks can run `bunk notify`.
- Add the same hook snippets as on your own machine.
- Inside tmux on the server, add `set -g allow-passthrough all` to
  `~/.tmux.conf`. tmux drops the reports without it, and with `on` it passes
  them only from the visible window.

Then connect in either of these ways.

| Command | What the remote shell gets | When the report counts |
| :- | :- | :- |
| `ssh build01` | nothing; the hooks run because sshd sets `SSH_CONNECTION` | while ssh or mosh runs in the pane |
| `bunk ssh build01` | the pane's `BUNK_NOTIFY_SESSION`, through a remote command | also when ssh hides behind another program, such as a wrapper script |

- The hook's CLI finds no socket and writes the report to the agent's
  terminal. Agents may start hooks without a controlling terminal. The CLI then uses
  the terminal of the nearest parent process that has one.
- ssh, jump hosts and `exec -t` in podman, docker or kubectl pass the report
  unchanged. GNU screen passes it wrapped in its own passthrough. mosh drops
  it, because mosh sends screen updates instead of the byte stream.
- The report names the agent, found among the hook's parent processes. It
  also names the host and the repo and branch of the hook's directory. The tab and the pane
  badge show them instead of the local `ssh` process's directory.
  `Status.Host` holds the host, and desktop notifications say "claude on
  build01 needs you".
- Any program can print the sequence. A report without a known token
  therefore counts only while ssh or mosh runs in the pane. A report with a
  token that this front end issued counts in any pane.
- `bunk ssh [ssh options] destination` adds `-t` and a remote command that
  exports the token. The command is
  `env BUNK_NOTIFY_SESSION=<token> sh -c 'exec "${SHELL:-/bin/sh}" -l'`.
  With a remote command, the server may skip its message of the day. ssh runs unchanged with a command of your own, or with `-N`, `-W`, `-T`,
  `-f` or `-s`. A `RemoteCommand` or `SessionType` in your ssh config does
  the same. `bunk-gui ssh` is
  the same command.
- bunk's TUI on the server, or inside a bunk-gui tab, relays its most urgent
  pane to the pane around it. The order is needs you, done, failed, working,
  then idle. When no pane has an agent and when it exits, it sends `clear`.
- Focusing the local tab or pane acknowledges the local copy only. A remote
  bunk TUI keeps its own state until you focus its pane.

## Other agents

- Agents without hooks are still covered by inference from output timing,
  BEL, OSC 9/99/777 and OSC 133;D.
- A known agent whose output ran for at least 10 seconds and then stopped
  for 2 seconds is done, without a notice, and alerts like a reported done.
  Gaps under 2 seconds keep the stretch going; the user's input starts a new
  one, so typing a long prompt never counts. A shorter stretch ends idle.
  The done state holds until the pane is acknowledged, new output arrives,
  `clear` is reported or the foreground process changes.
- Without hooks, a question or a permission prompt that follows a long
  stretch also shows as done, because inference cannot tell them apart.
- While ssh or mosh runs in a pane, OSC 9/99/777 from the other end mean
  needs you, labelled `agent`. Bells from there do not count, because a
  remote shell rings one on a failed tab completion.
- Any program can report its state by running `bunk notify <event>` from a
  wrapper script or a plugin.
