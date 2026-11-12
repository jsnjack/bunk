# Features and keys

This page lists every feature of bunk-gui and bunk with its default keys.
[configuration.md](configuration.md) shows how to change the keys, and
[agent-status.md](agent-status.md) explains the agent states.

## bunk-gui

bunk-gui's default keys are bunk's keys plus those of Ptyxis, GNOME's
terminal, with F2 for a new tab and Ctrl+Left/Right or Ctrl+Up/Down to
switch tabs.

| Feature | Default keys | Details |
| :- | :- | :- |
| New / close tab | F2 / Ctrl+Shift+W | A new tab starts in `new_dir`, the home directory by default |
| New window | Ctrl+Shift+N | Starts another bunk-gui in `new_dir`; GNOME's app menu has New Window too |
| Switch tab | Ctrl+Right / Ctrl+Left, Ctrl+Down / Ctrl+Up, Alt+1…9, Alt+0 | Alt+0 selects tab 10 |
| Move tab | Ctrl+Shift+PgUp / Ctrl+Shift+PgDn, or drag | Moves the selected tab in the rail or the bar; any tab can be dragged to a new place |
| Rename tab | | Double-click a tab, or choose "Rename tab…" in the menu; `rename_tab` in `[keys]` binds a key. The name replaces the agent or program name, and an empty name brings it back |
| Rail | Ctrl+Shift+B | Expands or collapses the side rail; the menu or `tabs` switches to the bar |
| Tab labels | Hold Alt | In the collapsed rail, holding Alt alone for 0.2 s (`rail_peek_delay`) labels each tab's ring with its Alt+N key, agent or name, repo ⎇ branch and hosts, and dims the panes behind the labels; Alt+N switches while they show, and releasing Alt hides them |
| Agent status | | Dot, state, repo ⎇ branch, the containers and SSH hosts of all the tab's panes as their badges name them, and the agent's notice; see [agent-status.md](agent-status.md) |
| Switcher | Ctrl+Shift+O | Lists the tabs in their order and marks the current one; the cursor starts on the first other tab. Type to filter, Up and Down pick a row, Enter opens it. A row's × closes its tab, and the switcher stays open |
| Tab menu | Right-click a tab | Rename, Copy directory, Mark as seen (clears done marks and notifications), Mute notifications, Show documents, Open in panel…, Close tab, Close other tabs; closing asks first while something runs |
| Pane menu | Right-click a pane | The link under the pointer, Copy, Paste, Select all, Find, Split, Zoom, Passthrough, Copy or open the directory, Clear scrollback, Close pane. Where the program uses the mouse, Shift+right-click opens it |
| Next needing you | Alt+A | Visits needs-you tabs, then done tabs; a done tab counts as seen, and a needs-you tab stays until you answer |
| Notifications | | A desktop notification when an agent in a tab you are not looking at needs you, is done or failed, or when a command there that ran 10 s or longer ends; clicking it opens the tab |
| Split | F1 / Alt+F1 | Side by side or stacked by pane shape; Alt+F1 opens a shell in the same container. While you drag a divider, a label shows each side's share |
| Zoom pane | F12 | |
| Move between panes | Alt+arrows | |
| Find | Ctrl+F or Ctrl+Shift+F | Enter or Ctrl+N: next; Ctrl+P: previous; Esc keeps the scroll position; Ctrl+C and Ctrl+V edit the bar's text |
| Scrollback | Shift+PgUp / Shift+PgDn | |
| Copy / paste | Ctrl+C / Ctrl+V, Ctrl+Shift+C / Ctrl+Shift+V | Ctrl+C copies a selection and ends it; with nothing selected it reaches the program. An image pastes as the path of a PNG file holding it; with nothing to paste, Ctrl+V reaches the program. Selecting copies to the primary selection; middle click pastes it |
| Select all | Ctrl+Shift+A | Selects the scrollback and the screen, and copies them to the primary selection |
| Text size | Ctrl+= / Ctrl+- / Ctrl+0 | |
| Images | | SIXEL, Kitty and iTerm2 images drawn at full resolution, 1:1 in device pixels |
| Links | Ctrl+click | Opens OSC 8 links and plain http, https and file URLs; Ctrl+hover underlines them |
| Passthrough | Ctrl+F12 | Per pane: every other key goes to the program; a PASS badge shows it |
| Shortcuts window | Ctrl+Shift+? / Ctrl+? | Rebind or turn off any action, and give it more than one key; ⚠ marks keys terminal programs may use |
| Preferences | Ctrl+, | Layout, rail, the Alt labels' delay, where new tabs start, font (from the installed monospace fonts), text zoom, theme, session restore, notifications, agent hooks, documents and extra agent CLIs, on four searchable pages (General, Appearance, Notifications, Agents); General's last group opens the shortcuts window and config.toml in your text editor |
| Agent hooks | | Preferences' Agents page shows which agents report their own state and have the bunk-present skill, and sets up both; a banner offers to set them up when an agent runs without them or they are outdated |
| Options menu | F10 | The ≡ button at the bottom of the rail, or at the right of the bar |
| Fullscreen | F11 | |
| Themes | | bunk's themes; picking one in Preferences repaints the panes at once |
| Quit | Ctrl+Q / Ctrl+Shift+Q | Asks first while a pane runs more than an idle shell |
| Restore session | | The next start offers the last session in a banner; see [Session restore](#session-restore) |

### Images and input

Programs in a pane see real cell pixels through TIOCGWINSZ and CSI 14/16/18 t,
and bunk-gui draws their images 1:1.

- Each pane keeps up to 64 MiB of image bitmaps (`--image-bytes`), and the
  images drawn least recently fall back to half-block cells.
- When a program stops reading its input, the window and other panes keep
  working. Its pane queues up to 16 MiB of input (`--input-bytes`) and refuses
  more with a toast.
- When a program reads the mouse, Ctrl+Shift+click opens a link instead of
  Ctrl+click.

### Documents

Agents can show you what they wrote in a panel beside the tab's panes
(`docs/present.md`). An agent, or you, runs `bunk present FILE` in a pane.

- HTML pages run as they are, with no network access, and can use bunk's
  stylesheet, theme colours, Mermaid, KaTeX, ECharts charts, Graphviz
  graphs, D3 and highlight.js. Agents are told to prefer
  them, because they can be interactive. Markdown shows as a formatted page
  with tables, highlighted code, Mermaid diagrams and math.
- Any other file previews there too: images, PDFs, video, audio, and code
  or text highlighted by its name. "Open in panel…" (the panel's folder
  button, the tab's menu, the main menu, the `open_in_panel` action) opens a file
  dialog in the focused pane's directory, and files dropped on a tab
  preview as well.
- Pages get a small kit by markup: sortable and filterable tables, tabs,
  steps, callouts, badges, an outline, coloured diffs and copy buttons on
  code. Markdown tables sort on their own, and `[TOC]` adds an outline.
- Markdown shows GitHub's alerts (`> [!WARNING]`) as callouts, and
  `<details>` sections, `<kbd>` keys and a few other tags.
- The page reloads when the file changes and keeps your place, the
  sections you opened, the tab you chose and what you typed. Sections that
  changed are marked, and "N changed" in the header shows what changed,
  word by word.
- Select text and choose "Ask the agent" (the header button or the context
  menu). Your question is pasted into the agent's input with the file, the
  section and the quoted text. Press Enter in the pane to send it.
- Or add it to a review: comments collect, their passages highlighted, and
  the Review button pastes them all as one message.
- A page can offer replies: a button such as "Choose B" opens the question
  popover with its text, and Enter pastes it into the agent's input.
- "Show documents" in the tab's menu or the main menu shows and hides the
  panel, or asks for a file in a tab without documents; the `documents`
  and `open_in_panel` actions have no default keys. The header lists the
  tab's documents and opens the shown one in the browser.
- "Open in the browser" shows the document light or dark as your system is
  set, and the open page follows the agent's edits.
- Click into the document, and Ctrl+F finds in it while the text size keys
  zoom it, without changing the terminal's text. F12 there gives the
  document the whole tab, and F12 again brings the panes back.
- A link to another file beside the document opens that file as a document
  of its own, at the heading the link names. Back returns to the previous
  document, and each document keeps your place while you read another.
- Agent hooks list the Markdown and HTML files an agent writes; with
  `[present] auto = true` they open the panel too. Otherwise a toast with a
  Show button names each new one, and "Show documents" counts them. When Claude Code or
  Copilot CLI asks you to approve a plan, the plan opens in the panel.
- In a bunk-gui pane, the hooks tell each agent at session start that the
  panel is there and how to present to it.
- `bunk present FILE#heading` opens the document at that heading, so the
  agent can point you to what it changed.
- `bunk present FILE` waits for the page to draw and tells the agent what
  is wrong with it: a diagram or formula that does not parse, a script
  error, a missing image, something blocked from the network. The status
  line counts the problems too.
- `bunk present install claude` (or `codex`, `copilot`) installs a skill that
  tells the agent when to present a document.

### Session restore

bunk-gui saves each window as it changes, in `~/.local/state/bunk-gui/windows`,
so a crash or a logout loses at most the last seconds. When bunk-gui starts and
finds windows that are no longer open, a banner asks whether to restore them.

- Restore brings the newest window back in place of the fresh tab and opens
  each other window again. Dismiss deletes them.
- Each pane gets its tab name, split, folder and scrollback back, with a line
  marking where the old output ends. Each tab gets its documents back.
- An agent pane runs the agent's resume command, such as `claude --resume ID`,
  with the session ID its hooks reported. Without hooks it resumes the folder's
  latest conversation (`claude --continue`), unless two panes ran that agent in
  the same folder. Agent panes skip the old scrollback, because the agent
  prints its conversation again.
- A pane that ran `ssh` or `mosh` gets that command typed at the prompt, but
  not run. Other commands are not restarted.
- Windows closed together are offered together, also while other bunk-gui
  windows run, and New window offers them too. A window whose last shell
  exited leaves nothing behind.
- The saved scrollback can hold secrets that were printed in a pane. The files
  are readable by you alone; `restore_scrollback = false` stops saving
  scrollback, and `restore = false` turns the feature off.

## bunk

| Feature | Default keys | Details |
| :- | :- | :- |
| Split | F1 | Direction from the pixel cell aspect ratio, not the column count |
| Split in the same context | Alt+F1 | New shell in the same container, SSH host or sudo |
| Zoom pane | F12 | Other panes stay open |
| Move between panes | Alt+arrows | |
| Scrollback | Shift+PgUp / Shift+PgDn | 10 000 lines per pane, kept through vim and other alt-screen programs |
| Search | Ctrl+F | Enter or Ctrl+N: next; Ctrl+P: previous; Esc: exit |
| Copy / paste | Ctrl+C / Ctrl+V | Ctrl+C reaches the program when nothing is selected |
| Passthrough | Ctrl+F12 | Per pane: every other key goes to the program |
| Agent status | | A badge per pane: working, idle, needs you (amber), done (green) or failed, with the agent's notice |
| Next needing you | Alt+A | Focuses the next pane whose agent needs you, else one that is done |
| Notifications | | A desktop notification when an agent in a pane you are not looking at needs you, is done or failed, or when a command there that ran 10 s or longer ends; over SSH, OSC 777 to the host terminal |
| Re-initialise host terminal | Alt+R | Recovers after binary output corrupts the host |
| Quit | Ctrl+Q | Ctrl+D or `exit` closes one pane |
| Mouse | | Click focuses, drag selects, double click selects a word; Shift overrides program mouse mode |
| Reflow | | Text rewraps at the new width on every split and resize |
| Themes | | `terminal` (host colours), `default`, `solarized-dark`, `dracula`, `nord`, or your own `[themes.NAME]` |

Alt+F1 supports Podman, Docker, LXD/LXC, Incus, Toolbox, Distrobox, SSH and
sudo/su.

## Badges

Each pane shows these badges in its top-right corner, in bunk and in bunk-gui:

| Badge | Meaning |
| :- | :- |
| `◐ claude needs you · Allow npm install?` | The agent's state and notice; needs you and done fill the badge |
| `● make running` | A command that has run for `command_after` seconds; `✓ done` or `✕ failed` after it ends while you look elsewhere |
| `⬡ mycontainer` | Podman, Docker, LXD, LXC or Incus container |
| `▣ my-toolbox` | Toolbox or Distrobox container |
| `⇄ myserver.com` | SSH session |
| `sudo` / `su` | Elevated shell |
| `bunk ⎇ main` | Where an agent or a full-screen program (vim, btop) works: repo ⎇ branch, else the directory. It takes the room the other badges leave, before the notice, and is cut to fit or left out in a narrow pane. Over ssh it shows only what a remote agent reports, and a multiplexer (bunk, tmux) gets none |
| `PASS` | Keyboard passthrough on; shown first when badges do not fit |
| `ZOOM` | Pane zoomed with F12 |
| `-42` | Scrolled back 42 lines |
| `COPIED` | Selection copied to the clipboard |

In bunk, the separators around a pane that needs you or is done take the
badge's colour.
