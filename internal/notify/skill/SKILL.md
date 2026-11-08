---
name: bunk-present
description: Show a Markdown or HTML document to the user in bunk-gui's side panel. Use it only when you run inside bunk (the environment variable BUNK_NOTIFY_SESSION is set) and you have written, or are about to write, a document for the user to read - a plan, spec, design, diagram, comparison, report or an HTML explanation.
---

# Presenting documents in bunk

bunk-gui shows Markdown and HTML files in a side panel beside the terminal
tab you run in. The user reads them there, selects passages and asks you
about them. Outside bunk-gui the same command opens the file in a web
browser.

## When to present

Present only inside bunk. When BUNK_NOTIFY_SESSION is unset, do not run the
command: outside bunk it opens a web browser, which you should not do on
your own. The user can still run it themselves.

Present a file when you wrote something the user should read rather than
skim in the terminal:

- a plan, spec or design, before you implement it;
- a report, review or comparison at the end of a task;
- an explanation where a diagram, table or formula says more than prose.

Do not present every file you touch, and never source code, configuration
or logs. Keep one document per topic and update it rather than writing a
new one.

## How

Run

    bunk-gui present PATH

or `bunk present PATH` where bunk-gui is not installed; both do the same.
The command prints one line:

- "Showing PATH in bunk-gui's side panel." The user sees it now.
- "Opened PATH in the web browser." bunk's terminal UI has no panel.
- "Cannot show PATH here: ..." Tell the user where the file is.

All three exit 0. Anything else, such as a missing file or an unsupported
extension, exits 1 with the reason on stderr; fix it and run again.

The panel reloads the file whenever it is saved and marks the sections
that changed, so you do not need to present a file again after editing it.

Where to put files:

- Lasting documents, such as designs, specs and decisions, belong in the
  repository, usually under docs/.
- Throwaway explanations go in a scratch or temporary directory, not in
  the repository.

## Formats

Markdown (.md, .markdown):

- GitHub-flavoured Markdown: headings, lists, tables, task lists, links.
- Code fences with a language get syntax highlighting.
- ```mermaid fences become diagrams.
- $inline$ and $$display$$ math.
- Images by a path relative to the Markdown file.

Use headings: the user's questions name the section they come from.

HTML (.html, .htm):

- The page is shown from its own directory. Scripts, styles and images must
  be inline or relative files in that directory. Nothing outside it is
  served, nor files whose names start with ".", and a page in the home
  directory or in / gets no files beside it at all.
- Nothing loads from the internet: no CDNs, web fonts or remote images, and
  no file:// URLs.
- Mermaid and math are Markdown features. An HTML page draws its own
  diagrams (inline SVG) and math.
- Prefer Markdown unless the page needs layout or interaction that Markdown
  cannot give.

## Feedback from the panel

The user can select text in the panel and send a question about it. It
arrives as your next input, in this form:

    About docs/plan.md › Session storage:
    > the passage the user selected
    the user's question

The first line names the file and the section the passage is in. Treat the
message as a question about that passage of that file. Answer it, and when
the user asks for a change, edit the file in place: the panel reloads and
marks what changed.
