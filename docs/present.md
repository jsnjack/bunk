# Presenting documents

bunk-gui can show Markdown and HTML documents in a side panel beside each
tab. An agent that writes a plan, a design, a report or an explanation for
the user runs `bunk present FILE` in its pane, and the document opens in the
side panel of the pane's tab. The user reads it there, selects a passage
and sends a question about it to the agent. The panel reloads the file
whenever it is saved and marks the sections that changed.

bunk's terminal UI has no side panel. There, and outside bunk, the same
command opens the document in the web browser.

## The panel

The panel sits to the right of the tab's panes, which keep their splits
and give up only the panel's width. Drag its edge to resize it; the width
stays while the window resizes.

- The header lists the tab's documents. Its buttons ask the agent about
  the selected text, open the document in the web browser, remove it from
  the list and hide the panel.
- "Show documents" in the tab's menu and in the main menu shows and hides
  the panel. The `documents` action has no default key; bind one in
  `[keys]`.
- The line at the bottom says when the document last reloaded, how many
  sections changed and where the file is, or why it could not be shown.
- A Markdown document reloads in place: the panel keeps the heading you
  were reading at the top and marks the headings whose sections changed.
  The marks fade at the next change. An HTML page loads again and scrolls
  back.
- Session restore brings back each tab's documents, the one shown, the
  panel's width and whether it was open.

To ask about a passage, select it and choose "Ask the agent about this…"
from the context menu, or the header's button. A popover shows the quote
and takes your question. The question is pasted into the input of the pane
that presented the document, else the tab's agent, without pressing Enter.
That pane takes the keyboard, and you send the question with Enter.

Pages run in WebKit's sandboxed web process with a private, in-memory
session. bunk-gui serves each document from a `bunk-present://` address of
its own, so one document's page cannot read another's files, and denies
every permission request (camera, notifications, clipboard). A link to a
web site opens in the browser when you click it; a page cannot navigate
anywhere by itself.

## The command

```
bunk present FILE
bunk present --hook AGENT -
bunk present skill | status | install AGENT
```

`bunk-gui present` takes the same arguments. Agents read `bunk present
--help`, which describes the formats and the feedback briefly.

`bunk present FILE` resolves FILE to an absolute path and checks that it is
a readable regular file of a supported kind. Then it does the first of these
that applies, prints one line and exits:

| Where it runs | What happens | Output | Exit status |
| :- | :- | :- | :- |
| A bunk-gui pane | The side panel of the pane's tab shows the file | `Showing docs/plan.md in bunk-gui's side panel.` | 0 |
| A host reached over ssh, without a bunk socket | Nothing | `Cannot show docs/plan.md here: this host is reached over ssh, …` | 0 |
| No `WAYLAND_DISPLAY` or `DISPLAY` | Nothing | `Cannot show docs/plan.md here: there is no graphical display …` | 0 |
| bunk's terminal UI, or outside bunk | The web browser opens the file | `Opened docs/plan.md in the web browser.` | 0 |
| Anywhere | FILE is missing, unreadable, not a regular file or of another kind | the reason, on stderr | 1 |

The path in the output is relative to the working directory when the file
lies inside it. An agent never sees a failure only because nothing can show
the file.

The browser gets a Markdown file as a page that bunk renders into
`$XDG_CACHE_HOME/bunk/present/<hash>/index.html` (`~/.cache/…` when
`XDG_CACHE_HOME` is unset), one directory per document, with the scripts
and styles it needs beside it. An HTML file opens as it is. bunk starts
`gio open`, else `xdg-open` (`open` on macOS), in a session of its own and
discards its output.

## Formats

| Kind | Extensions | What it shows |
| :- | :- | :- |
| Markdown | `.md`, `.markdown`, `.mdown`, `.mkd` | GitHub-flavoured Markdown with tables and task lists, code fences with syntax highlighting, ` ```mermaid ` fences as diagrams, `$inline$` and `$$display$$` math, images by paths relative to the file |
| HTML | `.html`, `.htm` | The page, served from its own directory |

The side panel loads nothing from the network, and pages cannot read files
through `file://` URLs. An HTML page's scripts, styles and images must be
inline or relative files in its directory. Nothing outside that directory
is served, nor files whose names start with `.`, and a page in the home
directory or in `/` gets no files beside it at all. Markdown pages get
Mermaid, KaTeX and the highlighting styles from bunk itself; an HTML page
draws its own diagrams, as inline SVG, and its own math.

Agents present only inside bunk: the skill tells them not to run `bunk
present` when `BUNK_NOTIFY_SESSION` is unset, because there it would open
a web browser on their own. The user can run it anywhere.

## Feedback

The user selects text in the panel and sends a question about it. bunk-gui
types it into the agent's input in this form:

```
About docs/plan.md › Session storage:
> the selected text
the user's question
```

The first line names the file and the section that holds the selection.
The agent answers in the terminal and, when the user asks for a change,
edits the file in place; the panel reloads and marks what changed.

## Hooks

The hook snippets of Claude Code, Codex and Copilot CLI
([agent-hooks.md](agent-hooks.md)) run `present --hook AGENT -` after each
tool call that writes a whole file, so bunk-gui learns about documents an
agent writes without being asked to present them:

| Agent | Hook | Tools | Where the files come from |
| :- | :- | :- | :- |
| Claude Code | PostToolUse | Write | `tool_input.file_path` |
| Codex | PostToolUse | apply_patch | the `*** Add File:` lines of the patch in `tool_input.command` |
| Copilot CLI | postToolUse | create, apply_patch | `toolArgs.path` (`toolArgs` may be an object or its JSON text), or the `*** Add File:` lines of the patch |

Edits do not count: a document the panel shows reloads when it changes, and
edits of files such as `AGENTS.md` or a changelog would otherwise open the
panel. A relative path is taken from the input's `cwd`. The hook keeps
Markdown and HTML files that exist, sends a request for each with `auto`
set, prints nothing and always exits 0. Outside bunk, and on hosts reached
over ssh, it sends nothing and never opens a browser.

bunk-gui's `[present]` table holds `auto`. With `auto = true`, a file that a
hook reports opens the side panel. With `auto = false`, the default, it is
only added to the tab's list of documents, where the user can open it. A
file the agent presents with `bunk present FILE` always opens the panel.

## The skill

Agents learn when and how to present from a skill, `bunk-present`, which
bunk installs into each agent's skills directory:

| Agent | File |
| :- | :- |
| Claude Code | `~/.claude/skills/bunk-present/SKILL.md` (`$CLAUDE_CONFIG_DIR/skills/…`) |
| Codex | `~/.codex/skills/bunk-present/SKILL.md` (`$CODEX_HOME/skills/…`) |
| Copilot CLI | `~/.copilot/skills/bunk-present/SKILL.md` (`$COPILOT_HOME/skills/…`) |

- `bunk present status` lists each agent's copy as installed, outdated (its
  text differs from the current skill) or missing.
- `bunk present install AGENT` writes it, creating its directories. A
  `SKILL.md` that is a symbolic link stays one, and its target gets the
  skill.
- `bunk present skill` prints it.

The skill tells the agent to present plans, specs, designs, reports and
explanations the user should read, not every file it touches and never code;
to keep lasting documents in the repository's `docs/` and throwaway ones in
a scratch directory; which formats work; and how the user's questions from
the panel arrive.

## Protocol

A present request travels over the notify socket like a report
([agent-hooks.md](agent-hooks.md#wire-format)), as one JSON line:

```json
{"v":1,"session":"0123456789abcdef0123456789abcdef","event":"present","path":"/home/u/src/app/docs/plan.md","auto":true}
```

| Field | Type | Meaning |
| :- | :- | :- |
| `event` | string | `present` |
| `path` | string | The document's absolute path, at most 2048 bytes, without control characters |
| `auto` | boolean | Optional: the request comes from a hook, not from the agent or the user |

Unlike a report, a request gets a one-line reply on the same connection,
and the server then closes it:

| Reply | Meaning |
| :- | :- |
| `shown` | bunk-gui shows the document |
| `unsupported` | The front end has no side panel (bunk's terminal UI) |
| `unknown` | No pane has the request's token |

A front end that predates present requests drops the line and closes the
connection without a reply, which the CLI reads as `unsupported`. A request
with a missing token or an invalid path gets no reply either. Requests never
travel in-band: on a host reached over ssh, the pane's session ignores a
`present` line in its output, as it ignores any event it does not know.
