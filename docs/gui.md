# bunk-gui

The design decisions of bunk-gui (`cmd/bunk-gui`), the GTK4 front end: its
keys, pointer and focus handling, windows and processes, tabs and the rail,
context menus, session restore, and how it draws images, box drawing, wide
glyphs and colour emoji. Read it before changing `cmd/bunk-gui` or
`internal/restore`, and before changing a default key.

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

## Default keys

- bunk-gui's default keys are the keys the user presses today in bunk's
  TUI inside Ptyxis. They are bunk's TUI defaults plus Ptyxis's shortcuts,
  with the user's own Ptyxis keys: F2 opens a tab and Ctrl+Left/Right
  switch tabs. Ctrl+Up/Down switch tabs too, at the user's request, to
  match the rail's vertical list. A change to a default needs the user's
  agreement; `TestDefaultKeys` lists every action's keys.
  - Ctrl+C copies only while text is selected, and ends the selection, so
    the next Ctrl+C reaches the program; with nothing selected it sends ^C.
    Ctrl+Shift+C always copies. The rule follows `keyCombo.collides`: a copy
    key a terminal program may use falls through without a selection. Any
    key sent to the program ends the selection too, as in bunk's TUI.
  - Ctrl+V and Ctrl+Shift+V paste text. An image goes as the path of a PNG
    file in the temporary directory, as in bunk's TUI, and agents attach
    it. PNG bytes on the clipboard, as screenshot tools offer them, go to
    the file as they are; other images are decoded and saved as PNG. With
    neither on the clipboard, Ctrl+V sends ^V; Ctrl+Shift+V sends nothing.
  - While the find bar has the keyboard, Ctrl+C and Ctrl+V edit its text,
    and Ctrl+N, Ctrl+P, Enter and Esc step through matches and close it.
    Elsewhere Ctrl+N, Ctrl+P, Enter and Esc reach the program.
  - Ctrl+Shift+PgUp/PgDn move the selected tab in `App.tabs`, which orders
    the rail, the bar, Alt+1…0 and the report's tabs. Dragging a tab moves
    it too (`tabdrag.go`): a gesture on the tab list claims the press past
    8 px, so the tab's button never clicks. A line marks the slot, and the
    release moves the tab. Chrome rebuilds wait while a tab is pressed, so
    a status change cannot replace the button being clicked.
  - The shortcuts window marks keys terminal programs use (Ctrl+C, Alt+A,
    F2 and others) with ⚠. Passthrough (Ctrl+F12) sends all of them to the
    program. Each key is a chip: a click captures a key in its place, ×
    removes it, and + adds one. A row's chips are rebuilt only when its
    keys change, because a button replaced under the pointer can miss the
    next click.
  - The switcher lists the tabs in their order (`App.tabs`), and a
    status change never moves a row: a notice takes the directory's
    place on the second line, and the state label has a fixed width.
    Up and Down move a cursor, which starts on the first match and stays
    on its tab as rows are rebuilt; Enter opens its tab. A row's × closes
    its tab as the rail's × does, without asking; the switcher stays open,
    the filter field keeps the keyboard, and a cursor whose tab closed
    stays at its place (`TestGUISwitcherClose`).

## Pointer and focus

- bunk-gui's pointer and focus handling matches bunk's TUI. The
  scrollbar thumb can be dragged while it shows (`grabThumb`). A selection
  dragged to the top or bottom row keeps scrolling (`edgeScroll`), and the
  wheel during a drag moves the selection's end with the view. Shift+PgUp
  and Shift+PgDn scroll half a page. Focus events (DECSET 1004) go to the
  focused pane of the selected tab in the active window
  (`focusreport.go`). `TestGUIScrollbarDrag`, `TestGUISelectionDrag` and
  `TestGUIFocusReports` check them.

## Divider shares

- While a divider is held, bunk-gui shows the split's shares on it
  (`splitratio.go`). A legacy event controller written in C sees the press
  first, because gotk4 v0.4.1 passes a GdkEvent to Go handlers as a
  GObject and GLib asserts. GtkPaned's `position` updates the label, and
  the release hides it. `gtk_widget_measure` includes the margins that
  place the label, so they are subtracted. bunk's TUI has no divider to
  drag.

## One process per window

- Each bunk-gui window is its own process. The application is non-unique,
  and each process has its own notify socket. New window (Ctrl+Shift+N,
  the menu) starts bunk-gui again in `new_dir` (home by default), in a new
  session, with an XDG activation token so the window takes the focus. The
  desktop entry's `new-window` action gives GNOME Shell's app menu the same
  item: Shell offers no New Window for a running GTK application without
  one. On the test compositor the window is recorded in `--report`
  (`windows`) and not started.

## A systemd scope per pane

- Each bunk-gui pane runs in a systemd scope of its own (`scope.go`), as
  VTE's terminals do. GNOME starts bunk-gui in
  `app-gnome-<app ID>-<PID>.scope` with `OOMPolicy=stop`. A program that
  the kernel's OOM killer ended in a pane therefore stopped that scope,
  which closed bunk-gui with every pane.
  - `newPane` asks the user's systemd on the session bus
    (`StartTransientUnit`) to move the child into
    `bunk-gui-<PID>-pane-<ID>.scope` in `app.slice`.
  - `OOMPolicy=continue` ends only the process the kernel picked, so the
    pane's shell and agents go on. `CollectMode=inactive-or-failed` removes
    the unit when its last process ends, also after a kill.
  - systemd moves the child about 15 ms after it starts. A process forked
    before then stays in bunk-gui's scope.
  - Without systemd on the bus, as on the window tests' bus, the pane stays
    in bunk-gui's scope, logged at debug level. `TestPaneScope` checks the
    call against a fake systemd.
  - bunk's TUI is unchanged: its panes share the host terminal's scope.

## Tabs with several agents, and the rail

- A bunk-gui tab can hold several agents in split panes. Its label keeps
  the name and repo of the first pane with an agent (`Tab.agentPane`),
  so it does not change as the agents change state. Its location lists
  every pane's places as the pane's context badge names them
  (`badges.Places`: the container, then the ssh or mosh host; sudo and su
  are left out), so the tab and the badges agree (`tabLoc`). Each kind
  shows once, with its names in pane order, the agent's first: "⬡ fedora
  · ⇄ ti-ci, int-build". The open rail wraps it onto a second line, the
  switcher cuts it at 36 characters, and the tooltip holds all of it
  (`TestGUITabHosts`).
  "+N" counts the agents in its other panes. The dot, colour and needs-you
  highlight show the most urgent state among them (`stateOrder`), and the
  rail shows that agent's notice as "agent: notice". The tooltip lists
  every agent. Notifications are per pane, as in the TUI, and Alt+A
  focuses the pane that needs the user. The collapsed rail draws every
  tab as one ring (`railring.go`, `rail_style = "ring"`, the default):
  closed for one pane, else an equal arc per pane in its state's
  colour, a plain shell's arc dimmed, around an empty centre. Each arc
  faces its pane (`ringArcs`): with each split halved, as in the map,
  the arcs go round in the order of the panes' directions from the
  tab's middle, turned to lie as close to them as they can, so panes
  side by side get the left and right halves. Past 6 panes the count shows under it.
  With `rail_style = "map"` (Preferences offer both) a tab shows its
  state glyph, and a tab of 2 to 4 panes is drawn as a map (`railMap`): a cell per pane, placed as the
  splits place the panes with each split halved, holding its agent's or
  command's dot, or ○ for a plain shell as in a one-pane tab. More
  panes show the most urgent dot over the pane count. A click still
  selects the tab. A pane without an agent takes part with its command
  mark (`alert.Mark`): running, or done or failed
  until the user looks at it, labelled by the command's name.
  - Holding Alt alone for `rail_peek_delay` (200 ms by default) labels each ring
    (`railpeek.go`) with its Alt+N key, dot, name or agent, repo ⎇
    branch and places, as the expanded rail does. Another key first,
    such as Alt+A or a shell's Alt+B, shows none. The labels float in
    `App.overlay`, so the panes keep their size, and take no clicks.
    Under them a scrim, the switcher's backdrop, dims everything right
    of the rail, so pane text does not crowd the labels; it starts at
    the rail's edge, so the rings stay bright beside their labels.
    Alt+N under them switches tabs. Releasing Alt, an inactive window,
    a dialog, the switcher and leaving the collapsed rail hide them.
    after-paint places them beside their items, because GTK knows the
    items' places only once it lays them out. `GtkFixed` gives a child
    its minimum width, an ellipsizing label's "…", so each label asks
    for its natural width; `TestGUIRailPeek` checks none is cut.

## Tab names

- The user can name a bunk-gui tab (`tabname.go`): a double-click on it,
  "Rename tab…" in the menu, or `rename_tab`, which has no default key.
  The name (`Tab.name`) replaces the agent or process name in the rail,
  the bar and the switcher; the dot, state, repo ⎇ branch, location and
  notice stay, and the tooltip starts with the name, then the agent. An
  empty name gives the automatic one back. The double-click is watched
  on the tab list, like tab dragging: the first click selects the tab and
  rebuilds the items, so the tab is the one under GTK's first press. The
  dialog's sheet maps its content a frame after it opens, and GTK drops
  keys for unmapped widgets although the entry already has the focus;
  window tests wait for `rename.ready` before they type.

## Context menus

- bunk-gui has context menus (`ctxmenu.go`). Their popover hangs on the
  root box, because refreshChrome replaces the tab items under it.
  - A right-click on a tab, caught on the tab list like the rename and
    drag gestures, opens its menu without selecting it: Rename…, Copy
    directory, Mark as seen, Mute notifications, Show documents, Open in
    panel…, Close tab and Close other tabs.
  - Mark as seen does what looking at each pane does (`AckSeen`, and
    the tracker sees the panes watched once): done and inferred needs-you
    clear, command marks clear and notifications go. A reported needs-you
    stays, as the agent still waits.
  - A muted tab's panes run the tracker with notifications off, so a
    shown notification is withdrawn and marks stay. Muting lasts for the
    window's life and is not restored.
  - A right-click in a pane opens its menu, the link under the pointer
    first. While the program reports the mouse, the right-click goes to
    it and Shift+right-click opens the menu, as in VTE.
  - Closing from a menu asks while a pane runs more than an idle shell
    (`Pane.busy`, as quitting does); the close_tab key does not ask.
  - The switcher marks the selected tab's row as current, and its cursor
    starts on the first other row, so the switcher's key and Enter switch
    away. `TestGUITabMenu`, `TestGUIPaneMenu` and
    `TestGUISwitcherCurrent` check them.

## The document panel

bunk-gui shows documents that agents present in a side panel per tab
(`panel.go`, `docs/present.md`). The panel sits beside the tab's panes
in `Tab.page`, a GtkPaned: the pane tree is untouched, and hiding the
panel gives the panes their width back.

- Documents are HTML and Markdown (`present.Kind.Document`), which
  agents write and hooks report; the skill and the session note ask for
  HTML first, because it can be interactive. Any other file the user
  previews (`present.Preview`): images, video and audio in a page that
  loads the file, text highlighted by its name, all served at
  `doc-N/NAME?bunk-preview` (`present.RenderPreview`); a PDF loads as it
  is into WebKit's PDF viewer. A response WebKit cannot show is refused,
  not downloaded, and the status line says so.
- "Open in panel…" (`open_in_panel`, the header, the tab's menu, the
  main menu, `documents` in a tab without documents) opens a GtkFileDialog
  in the focused pane's directory; files dropped on `Tab.page` preview
  too. Their questions go to the tab's agent.
- HTML pages get bunk's assets (`present.AssetsURL`, allowed by
  `htmlCSP`) and the theme's colours first in their head
  (`present.ThemeCSS`, `present.InsertHead`); the page runtime works on
  any page. A browser gets a copy of such a page that loads the assets
  from disk (`present.NeedsPage`, `WriteStandalone`), with a policy first
  in its head that keeps it off the network (`standaloneHTMLCSP`).
- Browser copies follow their file: `openExternal` marks the document
  (`panelDoc.browser`), and each change of its file writes the copy again
  off the main loop (`refreshCopy`). `WriteStandalone` writes a new
  `version.js` (`bunkVersion("…")`) with every page, and the copy's
  `bunk-live.js` loads it as a script every 1.5 s, since a file: page
  cannot fetch, and reloads when the version changes.
- Pages for a browser follow the system's colour scheme
  (`present.Options.System`): the panel's theme for its own darkness and
  bunk's default theme for the other, the dark one under
  `@media screen and (prefers-color-scheme: dark)` so print is light, each
  code style only in its own scheme, and no `data-bunk-theme`, so the runtime reads `--bunk-dark`.
  When the scheme changes, the runtime draws Mermaid diagrams again from
  their kept source (`data-bunk-src`). Pages in the panel keep the theme.
- `bunk-doc.css` keeps text at a readable width, the measure
  (`--bunk-measure`, 80 characters with the padding), and makes
  `#bunk-doc` at least that wide. The article grows to fit a table, or
  a block that holds one, up to the width of the page, so a wide table
  scrolls only when the panel or browser window is too narrow for it.
  Other blocks keep to the measure; headings and rules span the article
  without widening it (`contain: inline-size`). File previews
  (`.bunk-file`) fill the page. `TestGUIPresentMarkdown` measures both
  widths.
- A document counts as loaded only once a load of its address committed
  (`panel.committed`): a load that a newer one cancelled still finishes.
- `bunk present FILE` (or `bunk-gui present`) sends a present request
  over the notify socket with the pane's token. bunk-gui answers
  "shown" and opens the file in the panel of that pane's tab. bunk's TUI
  has no presenter and answers "unsupported", so the CLI opens the
  browser: a Markdown file rendered by `present.WriteStandalone`.
- Documents show in WebKitGTK 6 views (`webview.go`, cgo). One
  ephemeral network session and web context serve them all; WebKit runs
  page content in its sandboxed web process. Permission requests are
  denied, and a navigation leaves the document only when the user
  clicks a link, which opens in the browser.
- While the keyboard is in the panel (`App.focusedPanel`), the search
  action opens a find bar over the page (`panelFind`, WebKit's find
  controller: case-insensitive, wrapping, counted through
  `counted-matches`), and the text size actions zoom the panel's pages
  through `zoomSteps` (`panel.zoom`, kept by session restore). Passthrough
  of the focused pane does not hold keys there. The keys stay the user's:
  no action got a new default key.
- A link the user clicks to another file of the document's origin is
  presented as a document of its own (`panel.follow`), under the panel's
  limits (`docFile`), and scrolls to the link's fragment (`anchor`). Had
  it loaded in place, the panel would reload and ask about the first file
  while showing the second. Navigations the user did not start, by
  scripts or in frames, still load in place: WebKit does not say whether
  one is the main frame's. Back (`panel.history`) shows the documents
  shown before; each document keeps its scroll position (`lastY`), read
  just before another document loads.
- Pages load through `bunk-present://` (`presentserve.go`): `assets/`
  serves the page runtime, Mermaid, KaTeX, ECharts, Graphviz (Viz.js),
  D3 and highlight.js embedded in
  `internal/present`; `doc-N/` serves document N's directory, each
  document its own origin. Hidden files, symbolic links out of the
  directory and, for a document in the home directory or `/` (compared
  with symbolic links resolved), every file but the document itself are
  refused. HTML gets a Content-Security-Policy, as a header and first
  in its head, that blocks the network, `file://` and form submission.
  Markdown passes only attribute-free structural tags of raw HTML
  (`present/rawhtml.go`, `<details open>` aside) and turns GitHub's
  alerts into callouts (`callout`). Files are read and rendered
  off the main loop: a 2 MiB Markdown file with code takes 1.5 s.
- The directory is watched (GFileMonitor). A change of the file, of an
  image the Markdown names (`mentions`) or, for HTML, of an image or a
  page's code, style, font or media reloads after
  250 ms: Markdown swaps the article in place (`bunkDoc.replace`),
  keeps the reader at the same heading and marks the sections whose
  `data-bunk-hash` changed; HTML loads again and scrolls back. A page
  whose web process ends loads again once until its file changes.
- The page runtime's kit (`bunk-doc.js`, `kit`): components switched on
  by markup (sortable and filterable tables, tabs, steps, an outline,
  diffs, copy buttons) and set up once per element (`data-bunk-kit`), so
  `bunkDoc.replace` and repeated renders leave them be. Markdown pages
  carry `class="bunk-md"` on the article, which makes every table
  sortable and long ones filterable. Copy uses the async clipboard API
  from the click, which needs no clipboard access setting; where it fails,
  the code is selected for Ctrl+C.
- Page problems (`present/problems.go`): every view runs
  `ProblemCatcherScript` in bunk's isolated world before the page's own
  scripts; DOM events reach every world, so it records the page's script
  errors, unhandled rejections and policy violations. The page runtime
  records diagrams and formulas that do not parse in `<html
  data-bunk-problems>` and sets `data-bunk-rendered` once it has drawn.
  Files of the document's origin that were not served come from
  `serveScheme` (`webView.notServed`). `panel.collect` reads them all once
  the page has drawn (`ProblemsScript`, at most `collectWait`), shows the
  count in the status line and answers the checks of `bunk present`
  (`notify.Presenter`'s check, `addCheck`); `panelDoc.pending` keeps a
  check from reading a page while its new version loads. A change of the
  file while its page is still loading whole (`inFlight`) loads it again,
  rather than swap the new text into the page on its way.
- Before an HTML page loads again, or another document takes the view,
  `present.SaveStateScript` reads in bunk's isolated world which
  `<details>` are open, what form controls hold and which tabs
  (`role="tab"`) are selected; `RestoreStateScript` gives it back once the
  page has loaded, before the scroll: it sets the controls with input and
  change events and clicks the tabs, so the page's scripts follow. Elements
  match by id, a control by name and value, else by place.
- Closing a tab ends its view's web process (`closeWebView`); `--report`
  counts the live views (`run.web_views`).
- "Ask the agent" (the header button, or the context menu on selected
  text) reads the selection and its heading in an isolated script world
  (`present.SelectionScript`), which page scripts cannot change. The
  question is pasted into the pane that presented the document, else the
  tab's agent, as one bracketed paste without Enter; the pane takes the
  focus, and the user sends it. Without bracketed paste it is one line.
- The question box is a GtkTextView: Enter pastes, Shift+Enter starts a
  line. "Add to review" keeps the question as a `reviewItem`; the Review
  button lists them and pastes `reviewMessage` to the first comment's
  pane. Passages are highlighted with the CSS Custom Highlight API
  (`present.MarkQuotesScript`, isolated world), which leaves the page's
  DOM alone, and marked again after each load and swap. A review lives as
  long as the window: session restore does not keep it.
- Page replies: each view registers the script message handler
  `bunkReply` in the page's world; `bunkDoc.reply` and `[data-bunk-reply]`
  post `{text, section}`, and `panel.pageReply` opens the question
  popover with it, without a quote. A legacy event controller written in C
  (`bp_input`, as gotk4 cannot pass a GdkEvent to Go) stamps the user's
  clicks and keys in the page, and a reply more than `replyGrace` (2 s)
  after the last is dropped, so a page cannot open the popover by itself;
  the user still pastes it, and sends it in the pane.
- Hook snippets run `present --hook AGENT -` after the agents'
  file-writing tools. A reported Markdown or HTML file is listed in the
  panel; `[present] auto` opens the panel for it. Without it, a document
  new to a hidden panel counts in `panel.unseen` (the menus' "Show
  documents (N new)") and gets a toast with Show (`noticeListed`), which
  later listings retitle while it is up rather than queue toasts. Before a plan approval
  (Claude Code's ExitPlanMode, with `planFilePath`; Copilot CLI's
  exit_plan_mode, with the session's `plan.md`) the same command shows
  the plan with the panel open.
- Agents cannot see their environment, so the snippets' SessionStart hook
  (`present --session-start AGENT -`) tells them, in a bunk-gui pane
  only (`TERM_PROGRAM=bunk-gui`), that the panel is there and how to
  present (`sessionHint`), in each agent's additionalContext form.
- `bunk present install AGENT` writes the bunk-present skill
  (`skills/bunk-present/SKILL.md` for claude, codex and copilot), which
  tells agents when and how to present; Preferences offers it too. Its
  description names the task, not the environment variable, because
  agents choose skills by it.
- Session restore keeps each tab's documents, the one shown, the panel's
  width and whether it is open. Restored questions go to the tab's agent.
  A tab not shown yet has no width, so the panel takes its width when
  GTK first lays the tab out (`max-position`), as restored splits do; a
  hidden panel loads its document when it opens.
- Window tests drive the panel with `present`, `present-eval`,
  `present-wait`, `present-select`, `present-ask`, `present-toggle` and
  `present-back`, and point at an element of the page with
  `ptr-at doc SELECTOR`
  (`TestGUIPresentMarkdown`, `TestGUIPresentHTML`, `TestGUIPresentListed`,
  `TestGUIPresentLinks`, `TestGUIPresentFindZoom`). `keyboard_focus` is
  `panel` or `panel find` there.

## Session restore

- bunk-gui restores sessions (`restore.go`, `internal/restore`).
  - Each window holds a slot in `$XDG_STATE_HOME/bunk-gui/windows`:
    `<id>.json` (tabs, split ratios, focus, zoom, and each pane's
    directory, agent, agent session ID and ssh or mosh argv), `<id>/`
    with each pane's scrollback (`Session.History`), and `<id>.lock`,
    which the running window holds with flock. Files are 0600, in a 0700
    directory.
  - The layout is written every 2 s when it changed, and scrollback of
    panes with new output every 30 s, off the main thread, and both once
    more as bunk-gui quits. Agent panes skip scrollback: the agent prints
    its conversation again.
  - A start whose slot is new lists the slots no lock holds and shows a
    banner over the panes. Restore builds the newest here, in place of a
    first tab nobody typed in, and starts `bunk-gui --restore ID` for each
    other; Dismiss deletes them. New window (Ctrl+Shift+N) offers them
    too.
  - A restored pane starts the shell in its directory, replays its
    scrollback with a "restored from" line, and types its command once the
    shell has been quiet for 300 ms (at most 5 s): the agent's resume
    command with Enter, or the ssh or mosh command line without it.
    `restore.ResumeCommands` uses the session ID that hooks report
    (`Status.AgentSession`), else the directory's latest conversation
    when no other pane ran that agent there. Remote agents are not
    resumed.
  - A split's ratio is applied when GTK first lays it out (`node.ratio`,
    `newPaned`), so tabs not shown yet get theirs when selected. The
    containers' focus children point at the focused pane, or GTK would
    focus the first pane when the tab is shown.
  - Every orderly end (a quit, the last tab closed, SIGTERM or SIGHUP at
    logout) keeps the slot and stamps `Window.Closed`; a window without a
    live pane leaves none. `restore.Offer` offers the windows closed
    together: the last one closed and each closed within `CloseGap` (1
    min) of the next, so the order the user closes them in does not
    matter. A window closed earlier is deleted. Closed windows are offered
    while other windows run too, so a window closed beside others comes
    back in the next one opened; a crashed or killed one is always offered.
  - Window tests get their own state directory: the harness sets
    `XDG_STATE_HOME_TEST`, which `scripts/headless-gui.sh` turns into
    `XDG_STATE_HOME`. `TestGUIRestoreSession` saves, quits and restores
    with a fake `claude` on PATH that reports a session ID.

## Images and links

- bunk-gui draws image cells as textured quads (`cmd/bunk-gui/images.go`).
  Each placement gets a texture, uploaded on first draw. Drawing uses
  texelFetch when the cell size matches the placement's and linear
  filtering otherwise. Textures of dropped placements are freed; textures
  not on screen are freed oldest first past `--image-gpu-bytes` (256 MiB per
  pane). Links are handled in `links.go`. Ctrl+hover underlines OSC 8 links
  and plain URLs, and Ctrl+click opens them in their default application
  through `gio.AppInfoLaunchDefaultForURIAsync` behind `openURL`;
  Ctrl+Shift+click does the same while the application reports the mouse.
  `gtk.UriLauncher` would go through the desktop portal, which asks which
  application to use until the user has picked one a few times, and fails
  when that dialog is dismissed. `launchRefused` keeps the launcher off the test compositor;
  window tests read the links from `--report` (`opened`).

## Box drawing

- bunk-gui draws box drawing and similar characters itself
  (`internal/boxdraw`), as kitty and VTE do. A font's glyphs for them
  seldom reach the cell's edges, so neighbouring cells showed seams.
  - The set is kitty's 1,098 characters: box drawing, blocks and shades,
    braille, the symbols for legacy computing and their supplement,
    powerline separators, Fira Code's progress bar and spinner, and the
    branch drawing symbols of git graphs.
  - `Atlas.add` puts the mask into the glyph's slot on first use, so a
    frame costs what it did. Bold draws the same mask, and a character
    with a combining mark still goes to the font.
  - A light line is `LineWidth(cellW)` pixels wide: the cell width / 6.5,
    at least 1. A heavy line keeps the light line's parity, so both share
    a centre. A double line is two light lines a light line apart.
  - Fractions of the cell round through one function (`part`), so a half
    block ends where the second row of quarter blocks does. Shades are
    flat coverage (25, 50 and 75%), not dither patterns.
  - Curves, diagonals and triangles are antialiased from signed distance
    functions. Where a line leaves the cell it has the straight line's
    edge, so it meets the next cell's line. `TestLinesMeet` checks every
    edge, and `TestGUIBoxDrawing` compares the window's pixels with the
    masks.
  - bunk's TUI is unchanged: there the host terminal draws every glyph.

## Glyphs wider than a cell

- bunk-gui fits glyphs that are wider than their cell. A fallback font
  draws the symbols that the terminal font lacks, often wider than a cell:
  Noto Sans Math's ⟳ is 21 px wide in a 12 px cell, and the atlas kept
  only its first 12 px.
  - The rasterizer measures each glyph's ink (`fitInk`, atlas.go). Ink
    within a pixel of the cell (cellW/8) keeps its place, so the terminal
    font draws as before. Wider ink moves into the cell, and shrinks when
    it is wider than the cell.
  - Such a glyph (`flagOverflow`) draws across the next cell at full size
    when that cell is blank (`blankAt`, term.go), as in WezTerm; the
    atlas keeps both forms. In the last column or before a character, it
    draws fitted into its own cell.
  - A pictograph (Extended_Pictographic, such as ⚠ ★ ☑ ⚙ ⏺) that the
    terminal font lacks comes from a symbol font (`symbolFamilies`: Noto
    Sans Symbols 2, Noto Sans Symbols, Symbola, DejaVu Sans) before
    fontconfig's fallback. For Noto Sans Mono that fallback is Adwaita
    Mono, which squeezes pictographs into its cell: its ⚠ was a triangle
    10 px wide and 16 px tall. The fonts are named because after a
    monospace font, fontconfig ranks monospace fonts first even for
    "sans-serif". The RPM recommends the two Noto fonts; with none of the
    four installed, the fallback is fontconfig's, as before. Other
    symbols, such as Claude Code's ⎿ ✓ ❯, keep the monospace fallback,
    whose glyphs fit a cell; Noto Sans Symbols' ⎿ does not reach the
    cell's edges. `TestAtlasPictographs` checks the fonts and ⚠'s shape.
  - `TestAtlasFitsWideInk` draws every symbol of the arrow, mathematical,
    technical and dingbat blocks with the real fonts and checks that no
    ink leaves its slot. About 420 of their 1,840 symbols overflow a cell.

## Colour emoji

- bunk-gui draws colour emoji in their own colours. The atlas has two
  pages (`atlas.go`): coverage (R8), which the text shader tints with the
  cell's foreground, and premultiplied RGBA (`Atlas.Color`).
  - The rasterizer draws every glyph white on an ARGB32 surface. A glyph
    whose pixels are not grey came from a colour font, so it goes to the
    colour page and its slot gets `flagColor`; any other keeps its
    coverage, the alpha channel. The font decides, not a code point
    range: ❤ alone stays text, ❤️ with VS16 is colour.
  - The colour page is a second texture on unit 2. Its slots number from
    1 like the coverage page's, and the text shader picks the texture by
    the flag. Colour glyphs fade with an inactive pane and spread over a
    blank cell like other overflowing glyphs.
  - `TestAtlasColorEmoji` checks the real fonts, and `TestGUIColorEmoji`
    the window's pixels. Before, every emoji showed as U+FFFD.

## TERM_PROGRAM in panes

- bunk-gui's panes get `TERM_PROGRAM=bunk-gui` and `TERM_PROGRAM_VERSION`
  in place of inherited ones, so shell start-up files can skip starting
  bunk's TUI there (README). bunk's TUI passes an inherited `TERM_PROGRAM`
  on: Claude Code sends OSC notifications only to terminals it recognises
  by it, such as iTerm on macOS.
