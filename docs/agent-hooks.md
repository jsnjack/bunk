# Agent hooks

bunk-gui shows what the agent in each tab is doing: working, idle, needs you,
done or failed. Without hooks it infers the state from output, bells and
OSC 9/99/777 notifications. With the hooks below, the agent reports its own
state, and the report takes priority over inference.

Run `bunk-gui notify hooks <agent>` to print a snippet. The snippets on this
page are that output; a test keeps them in sync.

## How a report travels

- **Socket.** bunk-gui listens on `$XDG_RUNTIME_DIR/bunk-gui/notify-<pid>.sock`.
  The directory is 0700 and the socket 0600.
- **Session identity.** Every tab gets `BUNK_GUI_SOCKET` (the socket path) and
  `BUNK_GUI_SESSION` (a random 128-bit token) in its environment. Values
  inherited from the parent process are removed first.
- **Hook.** The agent runs `bunk-gui notify <event> [message | -]`. The CLI
  sends one JSON line and exits without waiting for a reply.
- **Server.** The server accepts clients with its own UID only (`SO_PEERCRED`).
  It maps the token to its session and calls `Session.Notify`. Unknown tokens
  and malformed lines are dropped.
- **Order.** The server reads one connection at a time. Reports arrive in the
  order the hooks connected, so `done` from Stop comes before `clear` from
  SessionEnd.

## Wire format

One JSON object per line, newline included at most 4096 bytes. Unknown
fields are ignored.

```json
{"v":1,"session":"0123456789abcdef0123456789abcdef","event":"needs-you","message":"Allow npm install?"}
```

| Field | Type | Meaning |
| :- | :- | :- |
| `v` | number | Protocol version, always `1` |
| `session` | string | Token from `BUNK_GUI_SESSION`, at most 64 bytes |
| `event` | string | One of the events below |
| `message` | string | Optional notice text; the CLI cleans it and cuts it to 1024 bytes |

| Event | `Status.State` | Notice |
| :- | :- | :- |
| `working` | `AgentWorking` | none |
| `idle` | `AgentIdle` | none |
| `needs-you` | `AgentNeedsYou` | the message, or "Needs your attention" |
| `done` | `AgentDone` | the message, if any |
| `failed` | `AgentFailed` | the message, if any |
| `clear` | inference again | inferred |

## How reports and inference combine

- **Priority.** While a report holds, `Status` shows it and sets
  `Status.Reported`. Output timing, BEL and OSC notifications do not change it.
- **Acknowledge.** `Session.Acknowledge` drops the report, as it drops an
  inferred attention request. Inference shows the tab until the next report.
- **Clear.** The `clear` event drops the report the same way.
- **Foreground change.** A report belongs to the foreground process group it
  arrived from. When that group leaves the foreground, the report goes.
- **Unrecognised agents.** A report from a process bunk does not recognise
  labels the tab `agent`.
- **Hook calls.** `Session.Notify` runs `Hooks.StatusChanged` once per change
  of `Status`, after releasing the session lock. A repeated report runs nothing.

## CLI

```
bunk-gui notify <event> [message | -]
bunk-gui notify hooks [agent]
```

- **Message words.** The words after the event are joined with spaces.
- **Hook input.** `-` reads stdin, for at most 2 seconds and 4 MiB.
- **JSON input.** When the message is a JSON object, the CLI takes the first
  non-empty field of `message`, `last_assistant_message`,
  `last-assistant-message`, `prompt_response` and `error`.
- **Output.** A report prints nothing to stdout. Claude Code adds
  UserPromptSubmit stdout to the prompt, and Gemini CLI parses stdout as JSON.

| Exit status | When |
| :- | :- |
| 0 | Report sent |
| 0 | `BUNK_GUI_SOCKET` or `BUNK_GUI_SESSION` unset: not inside bunk-gui |
| 0 | Socket missing or refusing connections: that bunk-gui has exited |
| 1 | Unknown event, bad arguments, or any other send error (message on stderr) |

The CLI never exits with 2. Claude Code, Codex and Gemini CLI read 2 as
"block this action", which would cancel a prompt or a stop.

Each snippet wraps the command in `[ -z "$BUNK_GUI_SESSION" ] || …`. In other
terminals the hook then succeeds without running bunk-gui, even where it is
not installed.

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
            "command": "[ -z \"$BUNK_GUI_SESSION\" ] || bunk-gui notify working",
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
            "command": "[ -z \"$BUNK_GUI_SESSION\" ] || bunk-gui notify needs-you -",
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
            "command": "[ -z \"$BUNK_GUI_SESSION\" ] || bunk-gui notify done -",
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
            "command": "[ -z \"$BUNK_GUI_SESSION\" ] || bunk-gui notify failed -",
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
            "command": "[ -z \"$BUNK_GUI_SESSION\" ] || bunk-gui notify clear",
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
| Notification (permission, elicitation, agent input) | `needs-you` | `message` from stdin |
| Stop | `done` | `last_assistant_message` |
| StopFailure (API error) | `failed` | `last_assistant_message`, e.g. "API Error: Rate limit reached" |
| SessionEnd (exit, `/clear`, `/resume`) | `clear` | none |

- **Interrupts.** Stop does not run when the user interrupts a turn. The
  keypress acknowledges the tab, so inference takes over until the next prompt.
- **Idle prompts.** The `idle_prompt` notification is left out. It arrives
  60 seconds after `done` and would mark a finished tab again.
- **Environment.** Hooks inherit Claude Code's environment, so both variables
  reach them.
- **Terminal notifications.** With these hooks, `preferredNotifChannel` no
  longer matters for bunk-gui.

Docs: <https://code.claude.com/docs/en/hooks> (events, input fields, matchers,
exit codes, environment).

## Codex

Add to `~/.codex/config.toml`, then run `/hooks` in Codex once to trust the
hooks:

```toml
# bunk-gui agent status. Run /hooks in Codex once to trust these hooks.
# Codex without hooks: use the notify program instead, as a top-level
# key above the first [table]:
# notify = ["bunk-gui", "notify", "done"]

[[hooks.UserPromptSubmit]]
[[hooks.UserPromptSubmit.hooks]]
type = "command"
command = '[ -z "$BUNK_GUI_SESSION" ] || bunk-gui notify working'
timeout = 5

[[hooks.PermissionRequest]]
[[hooks.PermissionRequest.hooks]]
type = "command"
command = '[ -z "$BUNK_GUI_SESSION" ] || bunk-gui notify needs-you Approval requested'
timeout = 5

[[hooks.Stop]]
[[hooks.Stop.hooks]]
type = "command"
command = '[ -z "$BUNK_GUI_SESSION" ] || bunk-gui notify done -'
timeout = 5

[[hooks.Interrupt]]
[[hooks.Interrupt.hooks]]
type = "command"
command = '[ -z "$BUNK_GUI_SESSION" ] || bunk-gui notify idle'
timeout = 5

[[hooks.SessionEnd]]
[[hooks.SessionEnd.hooks]]
type = "command"
command = '[ -z "$BUNK_GUI_SESSION" ] || bunk-gui notify clear'
timeout = 5
```

| Codex hook | Event | Message |
| :- | :- | :- |
| UserPromptSubmit | `working` | none |
| PermissionRequest | `needs-you` | "Approval requested" |
| Stop | `done` | `last_assistant_message` |
| Interrupt | `idle` | none |
| SessionEnd | `clear` | none |

- **Trust.** Codex skips new or changed user hooks until you review them with
  `/hooks`.
- **notify program.** Codex releases without hooks support only the `notify`
  program, which reports `agent-turn-complete`. Codex appends a JSON payload as
  the last argument; the CLI takes its `last-assistant-message`. `notify` is a
  top-level key, so place it above the first `[table]` in `config.toml`.
- **Both at once.** With hooks and `notify` together, `done` arrives twice. The
  second report changes nothing and runs no hook.

Docs: <https://learn.chatgpt.com/docs/hooks> (hook events, inputs, trust) and
<https://learn.chatgpt.com/docs/config-file/config-advanced> (`notify`). The
old `developers.openai.com/codex/...` URLs redirect there.

## Copilot CLI

Save as `~/.copilot/hooks/bunk-gui.json` (`$COPILOT_HOME/hooks/` when set):

```json
{
  "version": 1,
  "hooks": {
    "userPromptSubmitted": [
      {
        "type": "command",
        "bash": "[ -z \"$BUNK_GUI_SESSION\" ] || bunk-gui notify working",
        "timeoutSec": 5
      }
    ],
    "notification": [
      {
        "type": "command",
        "matcher": "permission_prompt|elicitation_dialog",
        "bash": "[ -z \"$BUNK_GUI_SESSION\" ] || bunk-gui notify needs-you -",
        "timeoutSec": 5
      }
    ],
    "agentStop": [
      {
        "type": "command",
        "bash": "[ -z \"$BUNK_GUI_SESSION\" ] || bunk-gui notify done",
        "timeoutSec": 5
      }
    ],
    "sessionEnd": [
      {
        "type": "command",
        "bash": "[ -z \"$BUNK_GUI_SESSION\" ] || bunk-gui notify clear",
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

- **Failures.** `errorOccurred` is left out. Its input marks some errors as
  recoverable, and a retried error would show the tab as failed.
- **Environment.** The docs do not say whether hooks inherit the CLI's
  environment. If no reports arrive, add a hook running `env > /tmp/hook-env`
  and look for `BUNK_GUI_SESSION`.

Docs: <https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-hooks-reference>
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
            "name": "bunk-gui-working",
            "type": "command",
            "command": "[ -z \"$BUNK_GUI_SESSION\" ] || bunk-gui notify working",
            "timeout": 5000
          }
        ]
      }
    ],
    "Notification": [
      {
        "hooks": [
          {
            "name": "bunk-gui-needs-you",
            "type": "command",
            "command": "[ -z \"$BUNK_GUI_SESSION\" ] || bunk-gui notify needs-you -",
            "timeout": 5000
          }
        ]
      }
    ],
    "AfterAgent": [
      {
        "hooks": [
          {
            "name": "bunk-gui-done",
            "type": "command",
            "command": "[ -z \"$BUNK_GUI_SESSION\" ] || bunk-gui notify done -",
            "timeout": 5000
          }
        ]
      }
    ],
    "SessionEnd": [
      {
        "hooks": [
          {
            "name": "bunk-gui-clear",
            "type": "command",
            "command": "[ -z \"$BUNK_GUI_SESSION\" ] || bunk-gui notify clear",
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

- **Timeouts.** Gemini CLI reads `timeout` in milliseconds.
- **Environment.** Gemini CLI can strip variables whose names contain words
  such as TOKEN, KEY or AUTH. `BUNK_GUI_SOCKET` and `BUNK_GUI_SESSION` avoid
  them; a test checks the names.
- **Strict mode.** With `GITHUB_SHA` set or `SURFACE=Github`, Gemini CLI
  removes every variable not on its allow list, and reports stop.

Docs: <https://geminicli.com/docs/hooks/reference/> (events, inputs, exit
codes), <https://geminicli.com/docs/hooks/> (configuration), and
[environmentSanitization.ts](https://github.com/google-gemini/gemini-cli/blob/main/packages/core/src/services/environmentSanitization.ts)
(environment redaction).

## Other agents

- **No hooks.** Inference still covers them: output timing, BEL, OSC 9/99/777
  and OSC 133;D.
- **Own scripts.** Any program can report: run `bunk-gui notify <event>` from
  a wrapper script or a plugin.
