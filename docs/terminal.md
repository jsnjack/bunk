# The terminal core: emulator, sessions and bunk's TUI

The design decisions behind the parts both front ends share, `internal/vt10x`,
`internal/session` and `internal/graphics`, and behind bunk's TUI (package
main). Read it before changing how output is parsed, stored, reflowed or
drawn, how bytes reach the PTY, or how a session starts and ends. The four
core flows are in [AGENTS.md](../AGENTS.md#key-flows).

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

## Keyboard passthrough

- Keyboard passthrough is per pane. Ctrl+F12 (`[keys].passthrough`) toggles
  forwarding of all other keys through the normal terminal encoder. The toggle
  remains reserved and exits bunk search. Mouse handling is unchanged. The PASS
  badge shares the status layout, takes highest priority, and invalidates the
  render overlay when toggled.

## The scrollbar column

- The PTY is one column narrower than the pane. The rightmost column
  (`p.x + p.w - 1`) is reserved for the scrollbar; the PTY never writes there.

## Cursor and cells under one lock

- Cursor shape and cell content must be read under the same lock. Both live
  inside `p.term`; `render()` must read them within the same `p.mu`
  acquisition so that `readPTY` cannot write between the two reads.
  `TestReadPTYSingleLock_ClaudeCursorRace` guards this.

## Display cells and wide characters

- vt10x coordinates are display cells. Wide characters occupy a lead cell
  (`Glyph.Width == 2`) and a continuation (`-1`); `-2` marks padding before a
  wide-character wrap. Padding is only ever the last cell of a wrapped row;
  `repairWideRow` turns padding that a shift or resize moves into a blank.
  Rendering, search, and selection skip continuations.
  Combining marks, variation selectors, and multi-codepoint emoji stay together
  in the lead cell through render, copy, search, and reflow. Mode 2027 can switch
  back to per-codepoint widths for legacy applications.

## Query replies

- Query replies belong to the pane. They are generated in stream order in
  both primary and alternate screens, including SSH/mosh panes; they are never
  delegated to the outer terminal. Unknown host colour defaults remain unknown.
  XTGETTCAP key replies use the normal input encoder with the current pane modes
  and Kitty flags; its terminal-name reply matches the TERM exported to panes.

## The PTY writer

- No PTY write blocks a front end or holds the session lock. Every write
  to a session's PTY goes through its writer (`internal/session/writer.go`).
  That covers query replies, graphics replies, keys, pastes, mouse and focus.
  - Each session has one FIFO queue. A goroutine starts when bytes are
    queued and exits when the queue is empty. `go` on a bound func
    allocates nothing.
  - Bytes leave in the order they were queued. Each reply and each input
    event is queued whole, so no reply lands inside a key or a paste.
  - `CaptureAndWrite` queues replies under the session lock, and the writer
    goroutine writes them without it. `WriteInput` returns at once.
  - Unwritten replies are capped at `ReplyBytes` (64 KiB). A reply that
    would pass the cap is dropped whole and counted in `WriteStats`.
  - Queued input is capped at `Options.InputBytes`, which defaults to
    `DefaultInputBytes` (16 MiB). An event that would pass the cap is
    refused whole with `ErrInputFull`.
  - When input is refused, bunk flashes "input not sent" on the pane.
    bunk-gui shows a toast, at most one per pane every 3 s; `--input-bytes`
    sets its cap.
  - A drained queue keeps at most 4 KiB, and a paste's buffer is freed.
  - Closing the PTY drops the queue and ends a blocked write. Close does
    this, and ReadLoop does it when the child exits.
  - In the TUI, tcell delivers host pastes as key events. bunk collects
    them between the paste markers and queues one event. A paste without an
    end marker is sent after `pasteTimeout` (1 s).
  - For tests, `WriteStats` reports queued, dropped and refused bytes, and
    `WaitWritten` waits for an empty queue before a test reads the PTY.

## Graphics

- Graphics are ordinary cells. Static SIXEL, Kitty, and iTerm2 images become
  half-block glyphs with immutable `Glyph.Image` samples. They clip, erase, scroll,
  and reflow with the grid; source bitmaps are not retained by scrollback. No image
  escape is forwarded to the host. Kitty file/shared-memory access is rejected.
  PTYs and CSI 14/16/18 replies use the session's cell pixels: virtual in bunk's
  TUI (8 wide, height from cell aspect); real device pixels in bunk-gui, which
  sets `Options.CellPixels` and calls `Session.SetCellPixels` on zoom or scale.
  Transfers are capped at 8 MiB, decoded images at 4 million pixels / 4096 per
  axis, and Kitty caches at 32 MiB / 32 images. Reflow retains cell samples
  without replaying image transfers.

## The image store

- Image bitmaps live in a per-session store, and cells name them by ID.
  `Options.ImageBytes` (bunk-gui: 64 MiB per pane, `--image-bytes`) creates a
  `graphics.Store`; bunk's TUI keeps none. A cell's `ImageCell` holds
  `Placement.Bitmap` (store ID) and its own `Col`/`Row` in the placement, in
  the side table, so references survive scrolling, scrollback and reflow.
  The store counts each bitmap once, sweeps placements no cell refers to
  (`MarkImages` over both screens and the scrollback) after Kitty deletion,
  ED 3, RIS, reflow, every 64 additions and before evicting, then evicts the
  least recently drawn. Cells without a bitmap draw their samples.
  `reflow_test.go` checks every image cell's reference after each reflow.

## Hyperlink IDs

- Hyperlink IDs are swept and reused. A cell's `Glyph.Link` is a uint16
  ID into its terminal's URL table. A sweep frees the IDs no cell of either
  screen, cursor attribute or kept row refers to; new URLs reuse them, lowest
  first. Sweeps run when the table reaches 4,096 URLs or twice what the last
  sweep kept, and after ED 3 and RIS. A cell never changes its ID, so it keeps
  its URL. Rows kept outside the terminal must be marked through
  `WithLinkReferences` (the session marks its scrollback ring); a terminal
  with a scroll callback but no marker never frees IDs. When live links alone
  exceed 65,535 IDs or 16 MiB, new links lose their URL until rows holding
  links leave; the sweep then retries once per 1,024 new URLs. A front end
  that resolves IDs from rows it copied earlier compares `LinkGen` first, as
  bunk-gui's Ctrl+click does; the TUI resolves them under the session lock.

## Reflow

- Primary-screen reflow uses stored cells. Soft-wrap markers join physical
  rows into logical lines; explicit line breaks remain separate. Cursor and
  scroll anchors follow their cells. Styles, links, and image samples survive
  reflow, and output storage is bounded by scrollback capacity plus pane height.
  Width changes clear selections whose coordinates are no longer valid.
  `vt10x.Rewrap` copies compact cells. A scrollback row that is a whole line
  and fits the new width moves without a copy; `Ring` caches that fit per
  row. Any reflow change must keep `FuzzReflowEquivalence` and the tests in
  `internal/session/reflow_test.go` green: they compare against the
  pre-rewrite implementation in `reflow_oracle_test.go`. Track speed with
  `go test ./internal/session -run '^$' -bench BenchmarkReflow`.

## Cursor colour

- Cursor colour follows the active pane. OSC 12 overrides and OSC 112 resets
  are emitted through tcell; shutdown restores the host cursor colour.

## Synchronized updates

- Synchronized updates are per pane. Other panes continue repainting;
  an abandoned update is released after one second.

## Kitty keyboard state

- Kitty keyboard state belongs to its negotiating foreground process group.
  Polling clears it only after that owner leaves the foreground.

## Terminal cleanup

- Terminal cleanup runs synchronously in `main` after the event loop,
  because background goroutines can't be relied on to finish their cleanup
  before the process exits.

## Session timers

- Session timers are set per session. `Options.Timing` holds the
  foreground poll (`ProcessPoll`, 1 s), idle (`IdleAfter`, 2 s), done
  (`DoneAfter`, 10 s), hang-up grace (`HangupGrace`, 1 s), recently-left
  (2 s) and sync-release (`SyncUpdateTimeout`, 1 s) intervals. A zero
  field takes the default; both front ends leave it zero. Tests shorten it
  per session, never through package variables, so parallel tests cannot
  race on it.

## Nested sessions

- Nested sessions are refused. `BUNK=1` is exported into every pane's
  environment; the binary exits at startup if it sees it set.

## Closing a session

- Closing a session ends it, whatever its child does. `Session.Close`
  closes the PTY master and sends SIGHUP; a child still running after
  `HangupGrace` (1 s) is killed with its process group. On Linux the master
  is re-wrapped as a pollable file (`ptyfd_linux.go`), so Close ends
  ReadLoop even while a process the child left behind (`server &`) holds
  the PTY. Both front ends close a pane's session when its child exits
  (the TUI in `removePane`). Resize the PTY with `session.Setsize`, not
  `pty.Setsize`: the latter reads the descriptor with `Fd`, which races
  with ReadLoop's Close and can hit a reused descriptor number
  (`TestResizeWhileOutputCloses`).
