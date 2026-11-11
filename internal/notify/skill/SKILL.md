---
name: bunk-present
description: Show the user a document you wrote - a plan, spec, design, report, comparison, diagram or explanation, best as an interactive HTML page - in a panel beside the terminal, where they read it, click through it, ask about passages, review it and reply, and see your edits live. Use it whenever you write such a document for the user to read while you run inside bunk; bunk-gui says so at session start, and bunk sets BUNK_NOTIFY_SESSION.
---

# Presenting documents in bunk

bunk-gui shows HTML pages and Markdown documents in a side panel beside
the terminal tab you run in. The user reads them there, clicks through
them, asks about passages, collects review comments and replies with the
page's buttons. Outside bunk-gui the same command opens a web browser.

## When to present

Present only inside bunk. In bunk-gui, a note at the start of the session
says so; otherwise run `printenv BUNK_NOTIFY_SESSION` once. When it is
unset, do not run the command: outside bunk it opens a web browser, which
you should not do on your own. The user can still run it themselves.

Present a file when you wrote something the user should read rather than
skim in the terminal: a plan, spec or design before you implement it; a
report, review or comparison at the end of a task; an explanation where a
diagram, chart, table or formula says more than prose.

Do not present every file you touch, nor source code, configuration or
logs on your own. Keep one document per topic and update it rather than
writing a new one. When the user asks to see a file, present it whatever
it is: images, PDFs, video, audio, code and other text show as previews.

## How

Run `bunk-gui present PATH`, or `bunk present PATH` where bunk-gui is not
installed. `PATH#heading-id` opens the document at that heading, to point
the user to what you changed. The command prints one line:

- "Showing PATH in bunk-gui's side panel." The user sees it now.
- "Opened PATH in the web browser." bunk's terminal UI has no panel.
- "Cannot show PATH here: ..." Tell the user where the file is.

In bunk-gui it waits until the page has drawn and then lists its problems:
diagrams, charts or math that do not parse, script errors, files it could
not load, anything blocked from the network. Fix them and run the command
again to check. All of this exits 0; a missing file or an unsupported
extension exits 1 with the reason on stderr.

The panel reloads the file whenever it is saved, so you need not present
it again after an edit, except to check it. An HTML page loads again and
keeps the sections, tabs and form values the user chose; a Markdown
document keeps the reader's place and marks the sections that changed.

Put lasting documents (designs, specs, decisions) in the repository,
usually under docs/; throwaway explanations in a scratch directory.

## Write HTML

Prefer one self-contained HTML page with inline <style> and <script>, h1-h3
headings with ids (questions name the section they come from), and bunk's
assets, which work with no network:

    <link rel="stylesheet" href="bunk-present://assets/bunk-doc.css">
    <script src="bunk-present://assets/bunk-doc.js" defer></script>

Put the content in <article id="bunk-doc">. Colour with the theme's CSS
variables, never fixed colours, so the page follows the panel's theme and,
in a browser, the user's light or dark mode: var(--bunk-bg), var(--bunk-fg),
var(--bunk-muted), var(--bunk-accent), var(--bunk-border),
var(--bunk-code-bg); --bunk-dark is 1 in a dark theme.

bunk-doc.js gives you, by markup alone:

- <table class="bunk-sort bunk-filter">: headers sort, a box filters rows.
- <div class="bunk-tabs"><section data-title="A">...</section>...</div>:
  tabs; class "bunk-steps" instead: a step-through with Back and Next.
- <div class="bunk-callout warning"><p class="bunk-callout-title">Warning</p>
  ...</div>: note, tip, important, warning, caution.
- <span class="bunk-badge good">: good, warn, bad, info.
- <nav class="bunk-toc"></nav>: an outline of the h2 and h3 headings.
- <pre class="bunk-diff">: a unified diff, its lines coloured.
- <pre class="mermaid">: a Mermaid diagram.
- <span class="math">x^2</span>, <div class="math math-display">: KaTeX.
- <pre class="bunk-chart" data-height="300">{ECharts option as JSON}</pre>:
  an interactive chart (tooltips, legend, dataZoom).
- <pre class="bunk-graphviz">digraph { a -> b }</pre>: a Graphviz graph.
- <pre><code class="language-go">: highlighted code; code gets a copy button.
- bunkDoc.use('d3').then(d3 => ...): D3 for custom visuals; also
  'echarts', 'graphviz', 'mermaid', 'katex', 'highlight'.

Let the user answer you from the page: a click on
<button data-bunk-reply="Go with option B">Choose B</button>, or
bunkDoc.reply(text) from your script, puts the text in their question
box to paste into your input. Use it for decisions and checklists.

Limits: other files the page loads must be relative files in its own
directory; files starting with "." and anything outside it are not served,
and a page in the home directory or in / gets no files beside it. Nothing
loads from the internet (no CDNs, web fonts, remote images) or from
file:// URLs. In a browser, fetch() of files fails: inline your data.

## Markdown

Markdown (.md) suits plain prose, notes and lists: GitHub-flavoured, with
tables (they sort), task lists and links; images by a path relative to the
file; ```lang fences highlighted; ```mermaid, ```chart (ECharts JSON),
```dot (Graphviz) and ```diff fences drawn; $inline$ and $$display$$ math;
GitHub's > [!NOTE], [!TIP], [!IMPORTANT], [!WARNING], [!CAUTION] callouts;
[TOC] alone for an outline; and <details><summary>, <kbd>, <sub>, <sup>,
<mark> (no other HTML, no attributes).

## Feedback from the panel

The user's messages arrive as your next input. A question about a passage:

    About docs/plan.md › Session storage:
    > the passage the user selected
    the user's question

A review, several comments at once:

    Review of docs/plan.md:

    1. Session storage
    > a passage
    a comment

A reply from one of the page's buttons: "About FILE › Section:" and the
button's text. Answer each comment, and when the user asks for changes,
edit the file in place: the panel reloads and marks what changed.
