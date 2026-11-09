---
name: bunk-present
description: Show the user a document you wrote - a plan, spec, design, report, comparison, diagram or explanation, best as an interactive HTML page - in a panel beside the terminal, where they read it, click through it, ask about passages and see your edits live. Use it whenever you write such a document for the user to read while you run inside bunk; bunk-gui says so at session start, and bunk sets BUNK_NOTIFY_SESSION.
---

# Presenting documents in bunk

bunk-gui shows HTML pages and Markdown documents in a side panel beside
the terminal tab you run in. The user reads them there, clicks through
them, selects passages and asks you about them. Outside bunk-gui the same
command opens the file in a web browser.

## When to present

Present only inside bunk. In bunk-gui, a note at the start of the session
says so; otherwise run `printenv BUNK_NOTIFY_SESSION` once. When it is
unset, do not run the command: outside bunk it opens a web browser, which
you should not do on your own. The user can still run it themselves.

Present a file when you wrote something the user should read rather than
skim in the terminal:

- a plan, spec or design, before you implement it;
- a report, review or comparison at the end of a task;
- an explanation where a diagram, table or formula says more than prose.

Do not present every file you touch, nor source code, configuration or
logs on your own. Keep one document per topic and update it rather than
writing a new one. When the user asks to see a file, present it whatever
it is: images, PDFs, video, audio, code and other text show as previews.

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

The panel reloads the file whenever it is saved, so you do not need to
present a file again after editing it. An HTML page loads again and
scrolls back, so its scripts start over; a Markdown document keeps the
reader's place and marks the sections that changed.

Where to put files:

- Lasting documents, such as designs, specs and decisions, belong in the
  repository, usually under docs/.
- Throwaway explanations go in a scratch or temporary directory, not in
  the repository.

## Write HTML

Prefer one self-contained HTML page: the user can interact with it, which
Markdown cannot give. Use what helps the reader:

- tabs, or collapsible <details> sections, for alternatives and detail;
- tables the user can sort or filter;
- diagrams and charts as inline SVG, or Mermaid (below);
- controls that show behaviour: sliders, toggles, a step-through of a flow.

Keep it one file with inline <style> and <script>. Give it h1-h3 headings
with ids: the user's questions name the section they come from.

The panel serves bunk's own assets at bunk-present://assets/:

    <link rel="stylesheet" href="bunk-present://assets/bunk-doc.css">
    <script src="bunk-present://assets/bunk-doc.js" defer></script>

- bunk-doc.css gives the page the look of bunk's documents in the panel's
  theme; put the content in <article id="bunk-doc">.
- bunk-doc.js draws <pre class="mermaid"> diagrams and typesets TeX in
  <span class="math"> and <div class="math math-display"> with KaTeX.
- The theme's colours are CSS variables: var(--bunk-bg), var(--bunk-fg),
  var(--bunk-muted), var(--bunk-accent), var(--bunk-border) and
  var(--bunk-code-bg); --bunk-dark is 1 in a dark theme. Use them rather
  than fixed colours, so the page matches the panel.

Limits:

- The page is shown from its own directory. Other files it loads must be
  relative files in that directory. Nothing outside it is served, nor
  files whose names start with ".", and a page in the home directory or
  in / gets no files beside it at all.
- Nothing loads from the internet: no CDNs, web fonts or remote images, and
  no file:// URLs.

## Markdown

Markdown (.md) suits plain prose, notes and lists:

- GitHub-flavoured Markdown: headings, lists, tables, task lists, links.
- Code fences with a language get syntax highlighting.
- ```mermaid fences become diagrams.
- $inline$ and $$display$$ math.
- Images by a path relative to the Markdown file.

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
