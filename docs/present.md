# Presenting documents

bunk-gui can show documents in a side panel beside each tab. An agent that
writes a plan, a design, a report or an explanation for the user, best as
an interactive HTML page, runs `bunk present FILE` in its pane, and the
document opens in the side panel of the pane's tab. The user reads it
there, selects a passage and sends a question about it to the agent. The
panel reloads the file whenever it is saved.

The user can preview any other file there too: images, PDFs, video, audio,
and code or other text with syntax highlighting.

bunk's terminal UI has no side panel. There, and outside bunk, the same
command opens the document in the web browser, and other files in their
default application.

## The panel

The panel sits to the right of the tab's panes, which keep their splits
and give up only the panel's width. Drag its edge to resize it; the width
stays while the window resizes.

- The header lists the tab's documents. Its buttons go back to the
  previous document, ask the agent about the selected text, preview a
  file, open the document in the web browser, remove it from the list and
  hide the panel.
- While the panel has the keyboard (click into the document), the keys of
  Find (Ctrl+F) find in the document, Enter and Ctrl+P step through the
  matches and Esc closes the bar; the text size keys (Ctrl+=, Ctrl+-,
  Ctrl+0) zoom the panel's pages, from 50% to 300%. The status line shows a
  zoom other than 100%, and session restore keeps it.
- A link to another file beside the document, such as `[the spec](spec.md#api)`,
  opens that file in the panel as a document of its own, at the heading the
  link names. Back returns to the document you came from, and each
  document keeps your place while you read another. Links to hidden files
  or to files outside the document's directory do not open.
- "Show documents" in the tab's menu and in the main menu shows and hides
  the panel; in a tab without documents it asks for a file to preview.
- The header's folder button and "Open in panel…" in the tab's menu and
  the main menu open a file dialog in the focused pane's directory. Dropping files on a tab,
  from a file manager, previews them too. Questions about a file you
  opened go to the tab's agent.
- The `documents` and `open_in_panel` actions have no default keys; bind
  them in `[keys]`.
- The line at the bottom says when the document last reloaded, how many
  sections changed and where the file is, or why it could not be shown.
- A Markdown document reloads in place: the panel keeps the heading you
  were reading at the top and marks the headings whose sections changed.
  The marks fade at the next change. An HTML page loads again and scrolls
  back, and keeps the sections you opened (`<details>`), the tabs you
  chose (`role="tab"`) and what you typed or set in its form controls.
- A document reloads when its file changes, and when an image beside it
  that it shows does: a Markdown document reloads for the images it names,
  not for others saved beside it, such as screenshots. An HTML page
  reloads for any image beside it and for the scripts, styles, fonts and
  media there, not for other files, which change all the time in a
  directory such as `/tmp`. A preview reloads when its file changes.
- A page whose web process ends, as when it uses up its memory, loads
  again once. If it ends it again, the status line says so, and it stays
  down until its file changes or you show it again.
- Session restore brings back each tab's documents, the one shown, the
  panel's width and whether it was open. A hidden panel loads its document
  when you open it.

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
bunk present --session-start AGENT -
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
| bunk's terminal UI, or outside bunk | The web browser opens a document or text; an image, PDF, video or audio file opens in its default application | `Opened docs/plan.md in the web browser.`, or `Opened shot.png in its default application.` | 0 |
| Anywhere | FILE is missing, unreadable, not a regular file, or a kind bunk cannot show | the reason, on stderr | 1 |

The path in the output is relative to the working directory when the file
lies inside it. An agent never sees a failure only because nothing can show
the file.

In bunk-gui the command waits for the page to load and draw, at most about
5 seconds, and lists after its line what was wrong with it, so the agent
can fix its own page:

```
Showing docs/plan.md in bunk-gui's side panel.
The page has 2 problems:
- Mermaid diagram 1: Parse error on line 2: Expecting 'SEMI', 'NEWLINE', got 'EOF'
- cannot load img/flow.png: not found
Fix them in the file: the panel reloads it. Run this command again to check the page again.
```

It reports diagrams and formulas that do not parse (and, from the page
runtime, its other libraries), script errors and unhandled promise
rejections, files of the document's directory the page asked for and did
not get, and content the page's policy blocked, such as a script from the
network. The exit status stays 0. Run the command again after a fix: when
the file changed since the panel loaded it, the panel loads it again before
it checks. The panel's status line counts the problems too, and its tooltip
lists them.

The browser gets a Markdown or text file as a page that bunk renders into
`$XDG_CACHE_HOME/bunk/present/<hash>/index.html` (`~/.cache/…` when
`XDG_CACHE_HOME` is unset), one directory per file, and an HTML page that
uses bunk's assets or theme colours as a copy there that loads them from
disk. The pages share the scripts, styles and fonts in
`…/bunk/present/assets`, which bunk writes only when they are missing or
another version of bunk wrote them. bunk-gui's "Open in the browser" uses
the same pages. They follow the light or dark style that the system sets
(`prefers-color-scheme`), and switch, diagrams included, when it changes:
in the style of the panel's theme a page has the panel's colours, and in
the other style bunk's default light or dark colours. They print in the
light colours. Other HTML pages and
other files open as they are. bunk
starts `gio open`, else `xdg-open` (`open` on macOS), in a session of its
own and discards its output.

## Formats

| Kind | Extensions | What it shows |
| :- | :- | :- |
| HTML | `.html`, `.htm` | The page, served from its own directory, with bunk's assets and theme colours (below) |
| Markdown | `.md`, `.markdown`, `.mdown`, `.mkd` | GitHub-flavoured Markdown with tables and task lists, code fences with syntax highlighting, ` ```mermaid ` fences as diagrams, `$inline$` and `$$display$$` math, images by paths relative to the file, GitHub's alerts (`> [!NOTE]`, `[!TIP]`, `[!IMPORTANT]`, `[!WARNING]`, `[!CAUTION]`) as callouts, and the HTML tags below |
| Image | `.png`, `.jpg`, `.jpeg`, `.gif`, `.webp`, `.avif`, `.bmp`, `.ico`, `.svg` | The image, at most as wide as the panel, on a checkerboard |
| PDF | `.pdf` | WebKit's PDF viewer |
| Video, audio | `.mp4`, `.m4v`, `.webm`, `.ogv`, `.mov`; `.mp3`, `.ogg`, `.oga`, `.opus`, `.wav`, `.flac`, `.m4a`, `.aac` | A player, as far as GStreamer decodes the format |
| Text | any other file of UTF-8 text without NUL bytes, judged from its first 8 KiB | The text, highlighted by the file's name as Chroma knows it; files over 1 MiB plain |

Other files, such as binaries, cannot be shown: `bunk present` exits 1, and
bunk-gui shows a toast.

Markdown passes only a few HTML tags, without attributes: `<details>` (with
`open`) and `<summary>` for sections the reader opens, `<kbd>`, `<sub>`,
`<sup>`, `<mark>`, `<ins>`, `<del>`, `<s>`, `<small>`, `<br>`, `<hr>`, `<dl>`,
`<dt>` and `<dd>`. Other tags are dropped and their text kept; scripts,
styles, frames and their content are dropped whole. Leave a blank line
after `<summary>…</summary>` and before `</details>` for Markdown inside.

The side panel loads nothing from the network, and pages cannot read files
through `file://` URLs. An HTML page's scripts, styles and images must be
inline or relative files in its directory, or bunk's assets. Nothing
outside that directory is served, nor files whose names start with `.`,
and a page in the home directory or in `/` gets no files beside it at all.

HTML pages get bunk's assets at `bunk-present://assets/`, which Markdown
pages use too:

| Asset | What it gives the page |
| :- | :- |
| `bunk-doc.css` | The look of bunk's documents in the panel's theme, for content in `<article id="bunk-doc">`. Text keeps a readable width of 80 characters, and the article widens to fit a table up to the width of the window, so a wide table scrolls only when the window is too narrow |
| `bunk-doc.js` | Mermaid diagrams in `<pre class="mermaid">`, KaTeX math in `<span class="math">` and `<div class="math math-display">`, ECharts charts in `<pre class="bunk-chart">`, Graphviz graphs in `<pre class="bunk-graphviz">`, highlight.js for `<code class="language-…">`, and the kit below; each library loaded only when the page needs it |
| `mermaid.min.js`, `katex/katex.min.js`, `katex/katex.min.css`, `echarts.min.js`, `viz-global.js`, `d3.min.js`, `highlight/highlight.min.js` | The libraries themselves, for a page that loads one directly |

### Charts, graphs, code and D3

| Markup | What it draws |
| :- | :- |
| `<pre class="bunk-chart" data-height="300">{ECharts option as JSON}</pre>`, or a ` ```chart ` fence | An interactive chart with Apache ECharts: tooltips, legends that toggle series, zoom where the option asks for it. The JSON is an [ECharts option](https://echarts.apache.org/en/option.html); the background stays the page's, and animation is off unless the option turns it on |
| `<pre class="bunk-graphviz">digraph { a -> b }</pre>`, or a ` ```dot ` fence | A graph laid out by Graphviz, in the page's text colour unless the graph sets its own |
| `<pre><code class="language-go">` in an HTML page | Code highlighted by highlight.js, in GitHub's light or dark style; Markdown's code is highlighted by bunk |
| `bunkDoc.use('d3')` in a page's script | A promise of D3, or of `echarts`, `graphviz` (a Viz.js instance), `highlight`, `katex` or `mermaid`, loaded from bunk's assets; a page can also load `bunk-present://assets/d3.min.js` itself |

Charts and graphs follow the colours: drawn again when a page in a browser
switches between light and dark. A chart option that is not JSON, or a
graph that does not parse, counts as one of the page's problems, which
`bunk present` reports.

### The kit

`bunk-doc.js` and `bunk-doc.css` also give pages a few components, switched
on by markup, so an agent writes the content rather than the widgets:

| Markup | What the reader gets |
| :- | :- |
| `<table class="bunk-sort">` | Headers that sort the rows, numbers as numbers. Every Markdown table sorts |
| `<table class="bunk-filter">` | A box above the table that filters its rows. Markdown tables of more than 10 rows get one |
| `<div class="bunk-tabs"><section data-title="A">…</section>…</div>` | Tabs, one section shown; `data-selected` picks the first |
| `<div class="bunk-steps"><section data-title="…">…</section>…</div>` | A step-through: one step at a time, numbered, with Back and Next |
| `<div class="bunk-callout note">` (`tip`, `important`, `warning`, `caution`) with `<p class="bunk-callout-title">` | A callout; in Markdown, `> [!NOTE]` and the like |
| `<span class="bunk-badge good">` (`warn`, `bad`, `info`) | A small coloured label |
| `<nav class="bunk-toc"></nav>`, or `[TOC]` alone in Markdown | An outline of the h2 and h3 headings |
| `<pre class="bunk-diff">`, or a ` ```diff ` fence | A unified diff with added and removed lines coloured |
| `<pre><code>` | A copy button in the corner |

Tabs and steps are `role="tab"` buttons, so the panel keeps the one chosen
when the page reloads. A page whose colours follow the system, as in a
browser, gets a `bunk:theme` event on `document` when the scheme changes
(`detail.dark`), and `bunkDoc.dark()` says which it is now.

bunk-gui also puts the theme's colours first in every HTML page's head, as
`:root` custom properties: `--bunk-bg`, `--bunk-fg`, `--bunk-muted`,
`--bunk-accent`, `--bunk-border`, `--bunk-code-bg`, `--bunk-selection`, and
`--bunk-dark` (1 or 0). It sets `color-scheme` to match, so a page without
styles of its own shows light text on the dark theme. A page's own styles
override all of it. When a browser opens a page that uses the assets or the
colours, `bunk present` gives it a copy that loads them from disk
(`present.WriteStandalone`), with colours that follow the system's light or
dark style. The copy keeps the panel's limits: a Content-Security-Policy
lets it load nothing from the network.

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
toast names it, with a Show button that opens it, and "Show documents" in
the tab's menu and the main menu counts the documents listed since the
panel was last open, as in "Show documents (2 new)". Documents listed while
the toast is up share it. A
file the agent presents with `bunk present FILE` always opens the panel.

When Claude Code or Copilot CLI asks the user to approve a plan, the same
command shows the plan, and opens the panel whatever `auto` says, because
the user reads it now:

| Agent | Hook | Tool | Where the plan comes from |
| :- | :- | :- | :- |
| Claude Code | PreToolUse | ExitPlanMode | `tool_input.planFilePath`, the plan file Claude Code writes in plan mode (`~/.claude/plans/` unless `plansDirectory` moves it) |
| Copilot CLI | preToolUse | exit_plan_mode | `plan.md` in the session's state directory, `~/.copilot/session-state/<sessionId>/` (`$COPILOT_HOME/…`) |

Codex's plan mode writes no file, so there is nothing to show.

## Telling agents about the panel

An agent cannot see its environment, so it cannot tell by itself that it
runs in bunk-gui and that the panel is there. Three things tell it:

1. At session start, the hook snippets run `present --session-start AGENT -`.
   In a bunk-gui pane (`TERM_PROGRAM=bunk-gui` and the notify socket set) it
   prints a few sentences that the agent adds to its context: that the
   panel exists, which documents to show there and how, how the user's
   questions arrive, and that the bunk-present skill has the rest. It
   prints them in the form each agent reads: Claude Code's and Codex's
   `hookSpecificOutput.additionalContext`, Copilot CLI's
   `additionalContext`. In bunk's terminal UI, outside bunk and on hosts
   reached over ssh it prints nothing, so the agent spends no tokens on it.
   Claude Code runs the hook again after `/clear` and compaction.
2. The bunk-present skill ([The skill](#the-skill)) says when to present
   and how. Agents see its description all the time and load the rest when
   a task matches.
3. The user can ask: `/bunk-present` in Claude Code, `$bunk-present` in
   Codex, or "use the bunk-present skill", or simply "show it in the
   panel".

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
- bunk-gui offers it in the same banner as the hooks
  ([agent-hooks.md](agent-hooks.md#set-up)): when a tab first shows an
  agent whose skill is missing or outdated, and at start for an outdated
  one. Set up (Update) installs everything the banner names. Preferences →
  Documents has an Install button per agent too.

The skill tells the agent to present plans, specs, designs, reports and
explanations the user should read as interactive HTML pages, Markdown for
plain prose; not to present every file it touches, nor code on its own; to
keep lasting documents in the repository's `docs/` and throwaway ones in a
scratch directory; which formats work and which of bunk's assets HTML can
use; and how the user's questions from the panel arrive.

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
| `check` | boolean | Optional: reply once the page has loaded, with its problems; `bunk present FILE` sets it, hooks do not |

Unlike a report, a request gets a one-line reply on the same connection,
and the server then closes it:

| Reply | Meaning |
| :- | :- |
| `shown` | bunk-gui shows the document |
| `unsupported` | The front end has no side panel (bunk's terminal UI) |
| `unknown` | No pane has the request's token |

A front end that predates present requests drops the line and closes the
connection without a reply, which the CLI reads as `unsupported`.

A request with `check` (and without `auto`) gets its `shown` once the page
has loaded and drawn, or after 5 seconds, followed by a line
`problem: TEXT` for each problem, at most 12 of at most 300 bytes. The
server waits on a goroutine of its own, so reports keep flowing meanwhile.
A front end that does not check replies at once with `shown` alone, so a
client from before checks and a server from after them still agree. A request
with a missing token or an invalid path gets no reply either. Requests never
travel in-band: on a host reached over ssh, the pane's session ignores a
`present` line in its output, as it ignores any event it does not know.
