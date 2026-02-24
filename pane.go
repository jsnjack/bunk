// pane.go – PTY spawning and VT100/ANSI emulation bridge.
//
// Each Pane owns:
//   - a PTY master (os.File) connected to a shell subprocess
//   - a vt10x.Terminal: the ANSI/VT100 state machine that parses raw PTY bytes
//     and maintains a virtual 2-D cell grid
//
// The "bridge" is the readPTY goroutine: it reads raw bytes from the PTY master
// and feeds them into term.Write().  The render goroutine then reads the cell
// grid via term.Cell(col, row) and paints it onto the tcell screen.
// Pane.mu serialises those two concurrent accesses to the terminal state.
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"github.com/hinshun/vt10x"
)

// scrollbackLines is the maximum number of scrollback lines retained per pane.
// It controls both the raw PTY replay buffer (rawBufMax) and the glyph ring
// (sbMaxLines).  Increasing this number uses more memory per pane:
//   - rawBuf:  scrollbackLines × ~200 bytes/line (raw ANSI, varies by content)
//   - sbRing:  scrollbackLines × cols × ~24 bytes (rendered glyphs, on demand)
const scrollbackLines = 10_000

// selPos identifies a cell in the pane's unified virtual coordinate space.
//
//	row 0 … sb.count-1  → scrollback ring entries (oldest … newest)
//	row sb.count + r    → live terminal row r
//
// This coordinate is stable across scrolls: as a line scrolls off the live
// view into the ring, its virtual row index does not change.
type selPos struct {
	row, col int
}

// Pane represents one terminal pane: a shell process attached to a PTY,
// plus a virtual terminal state machine that tracks what should be displayed.
type Pane struct {
	id   int
	x, y int // top-left corner on the host screen (content area, 0-indexed)
	w, h int // width and height of the content area in cells

	ptmx     *os.File  // PTY master – write keystrokes here, read shell output here
	ptmxOnce sync.Once // ensures PTY master fd is closed exactly once
	cmd      *exec.Cmd // the shell process

	// mu serialises all access to term (both writes from readPTY and reads from
	// the render goroutine), plus the dead and wantsBracketedPaste flags.
	mu                  sync.Mutex
	term                vt10x.Terminal // VT100/ANSI state machine
	dead                bool           // true once the shell process has exited
	wantsBracketedPaste bool           // DECSET 2004 enabled by the running app

	// Scrollback buffer – lines that have scrolled off the vt10x grid top.
	// Protected by mu.
	sb    sbRing // ring buffer of captured rows
	sbOff int    // 0 = live view; N = N lines above live view

	// Text selection state.  Protected by mu.
	// selAnchor is where Button1 was pressed; selCursor tracks the drag endpoint.
	// Both are in virtual (scrollback+live) row/col coordinates.
	selAnchor, selCursor selPos
	selActive            bool

	// searchHL maps (vRow<<16|col) → match type: 1=regular, 2=current (orange).
	// nil when no search is active for this pane.  Protected by mu.
	searchHL map[int64]int8

	// oscScan is the per-pane OSC pre-scanner (value, no alloc).
	// Forwards OSC 7 (CWD), OSC 8 (hyperlinks), OSC 52 (clipboard) to the
	// host terminal so those features work through the multiplexer.
	oscScan oscScanner

	// Process and container tracking.  All protected by mu.
	fgProcess     string // name of the current foreground process (e.g. "ssh", "sudo")
	containerID   string // active container name (updated live by trackFgProcess)
	containerType string // "toolbox", "distrobox", "podman", "lxc", or ""

	// baseContainerType/ID are set once at startup and represent the static
	// container context of the pane itself (e.g. bunk running inside a
	// Toolbox container).  They are used as fallback when the foreground
	// process is not inside any container.  Protected by mu.
	baseContainerType string
	baseContainerID   string

	// rawBuf stores the raw PTY byte stream (capped at rawBufMax) so that the
	// terminal content can be replayed into a fresh vt10x on resize, giving
	// correct line-wrap reflow at the new column width.  Protected by mu.
	rawBuf []byte
}

// NewPane spawns a shell inside a new PTY with the given geometry, starts the
// VT10x emulator, and launches the background I/O goroutines.
//
//	spawnArgs – argv for the child process; nil or empty means use $SHELL.
//	            Pass containerSpawnArgs(...) here to open the new pane inside
//	            the same container as the pane that was split.
//	redraw    – signalled after each chunk of PTY output
//	paneDead  – receives p when the shell exits
//	done      – closed by the app on shutdown
//	oscCh     – receives OSC 7/8/52 sequences to forward to the host terminal
func NewPane(id, x, y, w, h int, spawnArgs []string, redraw chan struct{}, paneDead chan *Pane, done chan struct{}, oscCh chan<- []byte) (*Pane, error) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	if len(spawnArgs) == 0 {
		spawnArgs = []string{shell}
	}

	cmd := exec.Command(spawnArgs[0], spawnArgs[1:]...)

	// Build the child environment.
	// Filter out TERM and COLORTERM from the host before setting our own.
	// (Simply appending would not override them on most shells/kernels.)
	// • TERM=xterm-256color – the emulation profile we advertise.
	// • COLORTERM=truecolor – tells colour-aware apps 24-bit RGB works.
	filtered := make([]string, 0, len(os.Environ()))
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "TERM=") && !strings.HasPrefix(e, "COLORTERM=") {
			filtered = append(filtered, e)
		}
	}
	// BUNK=1 lets shell rc files detect they're running inside bunk and skip
	// auto-launching it again (prevents recursive invocation).
	cmd.Env = append(filtered, "TERM=xterm-256color", "COLORTERM=truecolor", "BUNK=1")

	// Create the PTY pair first so we can pass the master as vt10x's response
	// writer.  vt10x uses the writer to send replies to OSC 10/11/4 colour
	// queries back to the shell (e.g. "what is the terminal background colour?").
	// Without this, apps like neovim and bat that adapt their colour scheme to
	// the terminal background see no response and fall back to defaults.
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Rows: uint16(h),
		Cols: uint16(w - 1), // reserve last column for the scrollbar
	})
	if err != nil {
		return nil, fmt.Errorf("pty.StartWithSize: %w", err)
	}

	// Initialise the VT10x state machine.
	// NOTE: we intentionally do NOT use vt10x.WithWriter(ptmx) here.
	// WithWriter makes vt10x send OSC 10/11 colour-query responses directly to
	// the PTY master (ptmx).  Writing to the master sends bytes to the slave
	// process's stdin.  When the pane is running SSH, those response bytes are
	// forwarded to the remote server as user input, corrupting the remote shell
	// session.  The colour-query feature is a cosmetic nicety; SSH reliability
	// is not optional.
	term := vt10x.New(vt10x.WithSize(w-1, h))

	p := &Pane{
		id: id, x: x, y: y, w: w, h: h,
		ptmx: ptmx, cmd: cmd, term: term,
	}

	// One-time container detection: read the shell process's own environ.
	if cmd.Process != nil {
		if ct := detectContainerFromPID(cmd.Process.Pid); ct != "" {
			p.containerType = ct
			p.baseContainerType = ct
			if ct == "lxc" {
				name := lxdContainerName()
				p.containerID = name
				p.baseContainerID = name
			}
			L.Debug("pane: container detected", "id", p.id, "type", p.containerType, "name", p.containerID)
		}
	}

	L.Debug("pane spawned", "id", p.id, "x", x, "y", y, "w", w, "h", h)

	go p.readPTY(redraw, oscCh)       // VT100 parsing bridge (write side)
	go p.waitForExit(paneDead, done)  // monitors shell lifecycle
	go p.trackFgProcess(redraw, done) // polls foreground process name

	return p, nil
}

// readPTY is the VT100 parsing bridge (write side).
//
// For each chunk of raw PTY bytes it:
//  1. Runs the oscScanner to extract and forward OSC 7/8/52 sequences.
//  2. Pre-scans for DECSET 2004 (bracketed paste enable/disable).
//  3. Captures rows that are about to scroll off the top (scrollback).
//  4. Feeds the bytes into vt10x.
//  5. Signals the render loop to repaint.
func (p *Pane) readPTY(redraw chan struct{}, oscCh chan<- []byte) {
	buf := make([]byte, 4096)
	for {
		n, err := p.ptmx.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			L.Debug("readPTY: received bytes", "pane", p.id, "bytes", n)

			// Step 1 – OSC passthrough (CWD, hyperlinks, clipboard).
			p.oscScan.Scan(chunk, oscCh)

			// Step 2 – track DECSET 2004 (bracketed paste).
			if bytes.Contains(chunk, []byte("\x1b[?2004h")) {
				p.mu.Lock()
				p.wantsBracketedPaste = true
				p.mu.Unlock()
			} else if bytes.Contains(chunk, []byte("\x1b[?2004l")) {
				p.mu.Lock()
				p.wantsBracketedPaste = false
				p.mu.Unlock()
			}

			// Steps 3+4 – scrollback capture + vt10x write (all under Pane.mu).
			p.mu.Lock()
			p.captureAndWrite(chunk)
			scrolling := p.sbOff > 0
			p.mu.Unlock()

			// Step 5 – wake the render loop (coalesced).
			// Skip when the user is reading scrollback: new output is buffered
			// silently so the visible view doesn't jump while they are reading.
			if !scrolling {
				select {
				case redraw <- struct{}{}:
				default:
				}
			}
		}
		if err != nil {
			L.Debug("readPTY: PTY read error (shell exited)", "pane", p.id, "err", err)
			break
		}
	}
	p.closePTX()
}

// captureAndWrite snapshots rows that are about to scroll off, then writes
// chunk to vt10x.  Must be called with Pane.mu held.
//
// The detection algorithm is described in scrollback.go.  We skip capture
// when the alternate screen is active (vim, htop, less) because those apps
// manage their own screen state and don't produce classic TTY scrolling.
//
// We do NOT skip capture based on cursor position.  The previous optimisation
// (only snapshot when cursorY >= rows/2) was incorrect: a large burst of output
// can cause scrolling even when the cursor started in the upper half of the
// screen.  Fresh panes start with cursor at row 0, so the guard prevented any
// scrollback from being captured until the cursor happened to move past the
// midpoint.  Removing it costs one full-grid snapshot per PTY chunk (cheap;
// see scrollback.go for the O(cols×rows) analysis).
func (p *Pane) captureAndWrite(chunk []byte) {
	cols, rows := p.term.Size()
	altScreen := p.term.Mode()&vt10x.ModeAltScreen != 0

	// Accumulate raw bytes for replay-based reflow on resize.
	// Skip while alternate screen is active (vim, htop, etc.) — those apps
	// paint absolute-position content that doesn't replay meaningfully.
	if !altScreen {
		p.rawBuf = append(p.rawBuf, chunk...)
		if len(p.rawBuf) > scrollbackLines*200 {
			// Trim from the front at a clean newline boundary so we don't
			// start playback in the middle of an ANSI escape sequence.
			excess := len(p.rawBuf) - scrollbackLines*200
			if nl := bytes.IndexByte(p.rawBuf[excess:], '\n'); nl >= 0 {
				p.rawBuf = p.rawBuf[excess+nl+1:]
			} else {
				p.rawBuf = p.rawBuf[excess:]
			}
		}
	}

	var prevGrid [][]vt10x.Glyph
	if !altScreen {
		prevGrid = captureGrid(p.term, cols, rows)
	}

	p.term.Write(chunk) //nolint:errcheck

	// When alt screen exits, rawBuf has the entry sequence (\x1b[?1049h) but
	// NOT the exit — the exit chunk arrived while altScreen=true and was
	// skipped.  If rawBuf is later replayed in resizeAndReflow the scratch
	// terminal would enter alt screen and never leave it, putting cursor at
	// row 0 of the alt screen and producing a blank reconstructed pane.
	// Appending the exit sequence here keeps rawBuf self-consistent.
	if altScreen && p.term.Mode()&vt10x.ModeAltScreen == 0 {
		p.rawBuf = append(p.rawBuf, "\x1b[?1049l"...)
	}

	if prevGrid != nil {
		newRow0 := captureRow(p.term, 0, cols)
		var newRow1 []vt10x.Glyph
		if rows >= 2 {
			newRow1 = captureRow(p.term, 1, cols)
		}
		shift := detectShift(prevGrid, newRow0, newRow1)
		if shift > 0 && shift < len(prevGrid) {
			// Normal scroll: exactly `shift` rows have scrolled off the top.
			for i := 0; i < shift; i++ {
				p.sb.push(prevGrid[i])
			}
			// If the user is reading scrollback, keep the view anchored by
			// advancing sbOff by the same amount.  Without this the view drifts
			// forward as new content pushes old lines into the ring.
			if p.sbOff > 0 {
				p.sbOff += shift
				if p.sbOff > p.sb.count {
					p.sbOff = p.sb.count
				}
			}
			L.Debug("captureAndWrite: scrollback push", "pane", p.id, "rows", shift, "total", p.sb.count, "sbOff", p.sbOff)
		} else if shift == len(prevGrid) {
			// Large-burst sentinel: the output scrolled more than one full
			// terminal height, so all of prevGrid has rolled off.
			// Only push up to the LAST NON-BLANK row to avoid storing the
			// unused blank space below the cursor (those rows were never
			// written to; a terminal is never "full" at the start).
			lastNonBlank := -1
			for i := 0; i < len(prevGrid); i++ {
				if !isBlankRow(prevGrid[i]) {
					lastNonBlank = i
				}
			}
			pushed := lastNonBlank + 1
			for i := 0; i < pushed; i++ {
				p.sb.push(prevGrid[i])
			}
			if p.sbOff > 0 {
				p.sbOff += pushed
				if p.sbOff > p.sb.count {
					p.sbOff = p.sb.count
				}
			}
			L.Debug("captureAndWrite: large-burst scrollback push", "pane", p.id, "rows", pushed, "total", p.sb.count, "sbOff", p.sbOff)
		}
	}
}

// waitForExit blocks until the shell process exits (or the app shuts down),
// then marks the pane dead and notifies the app so it can remove the pane.
func (p *Pane) waitForExit(paneDead chan *Pane, done chan struct{}) {
	p.cmd.Wait() //nolint:errcheck
	L.Debug("pane process exited", "id", p.id)
	p.mu.Lock()
	p.dead = true
	p.mu.Unlock()
	select {
	case paneDead <- p:
	case <-done:
	}
}

// writeInput sends raw bytes (encoded keystrokes or mouse sequences) to the
// shell via the PTY master.
func (p *Pane) writeInput(data []byte) {
	p.ptmx.Write(data) //nolint:errcheck
}

// scrollUp scrolls the view n lines toward the past (increases sbOff).
// Clamped so sbOff never exceeds the number of captured lines.
func (p *Pane) scrollUp(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sbOff == 0 {
		// Entering scrollback for the first time: rebuild the ring from the
		// raw PTY byte buffer.  detectShift can only capture rows that were
		// visible *before* a chunk arrived; a single large TCP burst (common
		// over SSH) can scroll through many screenfuls in one read, dropping
		// every intermediate line.  The rawBuf replay into a tall scratch
		// terminal captures all of them at once.
		p.rebuildScrollbackFromRawBuf()
	}
	before := p.sbOff
	p.sbOff += n
	if p.sbOff > p.sb.count {
		p.sbOff = p.sb.count
	}
	L.Debug("scrollUp", "pane", p.id, "from", before, "to", p.sbOff, "max", p.sb.count)
}

// scrollDown scrolls the view n lines toward the present (decreases sbOff).
func (p *Pane) scrollDown(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	before := p.sbOff
	p.sbOff -= n
	if p.sbOff < 0 {
		p.sbOff = 0
	}
	L.Debug("scrollDown", "pane", p.id, "from", before, "to", p.sbOff)
}

// scrollReset snaps back to the live view (sbOff = 0).
func (p *Pane) scrollReset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sbOff != 0 {
		L.Debug("scrollReset", "pane", p.id, "was", p.sbOff)
	}
	p.sbOff = 0
}

// inScrollback reports whether the pane is currently showing scrollback.
// Safe to call without Pane.mu (reads an int, which is atomically readable
// on all Go-supported platforms, but we use a lock for correctness).
func (p *Pane) inScrollback() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sbOff > 0
}

// isDead reports whether the shell has exited.
func (p *Pane) isDead() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dead
}

// resize updates the pane's screen position and dimensions, sends TIOCSWINSZ
// to the PTY (causing the shell to receive SIGWINCH), and resizes the vt10x
// grid.  If the width changes, the existing terminal content is captured and
// re-injected so it reflows naturally to the new column width instead of being
// silently truncated.
func (p *Pane) resize(x, y, w, h int) {
	L.Debug("pane resize", "pane", p.id, "x", x, "y", y, "w", w, "h", h)
	p.x, p.y, p.w, p.h = x, y, w, h
	p.mu.Lock()
	p.resizeAndReflow(w-1, h)
	p.mu.Unlock()
	if p.ptmx != nil {
		pty.Setsize(p.ptmx, &pty.Winsize{ //nolint:errcheck
			Rows: uint16(h),
			Cols: uint16(w - 1), // last column reserved for scrollbar
		})
	}
}

// resizeAndReflow resizes the pane terminal to (newCols × newRows).
//
// When raw PTY bytes are available, the entire output history is replayed
// into a temporary vt10x at the new width so lines wrap correctly at the
// new column count.  The resulting state is split into a new glyph scrollback
// (rows that don't fit in newRows) and a fresh live terminal (the rest).
//
// For alt-screen apps (vim, htop, …) reflow is skipped — they redraw
// themselves after SIGWINCH.
//
// Must be called with p.mu held.
func (p *Pane) resizeAndReflow(newCols, newRows int) {
	oldCols, oldRows := p.term.Size()
	if oldCols == newCols && oldRows == newRows {
		return
	}

	if p.term.Mode()&vt10x.ModeAltScreen != 0 {
		p.term.Resize(newCols, newRows)
		return
	}

	if len(p.rawBuf) == 0 {
		// No history yet (brand-new pane); plain resize is sufficient.
		p.term.Resize(newCols, newRows)
		return
	}

	// Replay raw bytes into a tall scratch terminal so nothing scrolls off
	// during replay and we can read back all rows.
	replayH := sbMaxLines + newRows
	scratch := vt10x.New(vt10x.WithSize(newCols, replayH))
	// Prepend a full SGR reset so trimmed attribute state doesn't bleed.
	scratch.Write(append([]byte("\x1b[0m"), p.rawBuf...)) //nolint:errcheck

	// Content extent: the cursor row is the last line the shell wrote to.
	cur := scratch.Cursor()
	contentRows := cur.Y + 1

	// Split: rows [0, firstVisible) → scrollback; [firstVisible, contentRows) → live terminal.
	firstVisible := contentRows - newRows
	if firstVisible < 0 {
		firstVisible = 0
	}

	// Rebuild scrollback from the replay.
	p.sb = sbRing{}
	for r := 0; r < firstVisible; r++ {
		p.sb.push(captureRow(scratch, r, newCols))
	}
	p.sbOff = 0

	// Rebuild the live terminal by injecting only the visible rows.
	visibleRows := make([][]vt10x.Glyph, newRows)
	for r := 0; r < newRows; r++ {
		srcRow := firstVisible + r
		if srcRow < contentRows {
			visibleRows[r] = captureRow(scratch, srcRow, newCols)
		} else {
			visibleRows[r] = make([]vt10x.Glyph, newCols) // blank padding
		}
	}
	p.term = vt10x.New(vt10x.WithSize(newCols, newRows))
	reflowInject(p.term, visibleRows)

	L.Debug("resizeAndReflow: raw replay done", "pane", p.id,
		"old", fmt.Sprintf("%dx%d", oldCols, oldRows),
		"new", fmt.Sprintf("%dx%d", newCols, newRows),
		"content_rows", contentRows, "sb_rows", firstVisible)
}

// rebuildScrollbackFromRawBuf replays the raw PTY byte buffer into a tall
// scratch terminal at the current column width and rebuilds the scrollback
// ring from rows that don't fit in the live view.
//
// Unlike the real-time detectShift path, this captures every line that ever
// passed through the terminal — including lines that scrolled off mid-chunk
// in a single large TCP burst (the common SSH case where a remote "cat" sends
// many screenfuls of data in one read).
//
// Only the scrollback ring is updated; p.term is left untouched so the live
// view is not disturbed.
//
// Must be called with p.mu held.
func (p *Pane) rebuildScrollbackFromRawBuf() {
	cols, rows := p.term.Size()
	if len(p.rawBuf) == 0 || p.term.Mode()&vt10x.ModeAltScreen != 0 {
		return
	}
	replayH := sbMaxLines + rows
	scratch := vt10x.New(vt10x.WithSize(cols, replayH))
	scratch.Write(append([]byte("\x1b[0m"), p.rawBuf...)) //nolint:errcheck

	cur := scratch.Cursor()
	contentRows := cur.Y + 1
	firstVisible := contentRows - rows
	if firstVisible < 0 {
		firstVisible = 0
	}
	p.sb = sbRing{}
	for r := 0; r < firstVisible; r++ {
		p.sb.push(captureRow(scratch, r, cols))
	}
	L.Debug("rebuildScrollbackFromRawBuf", "pane", p.id,
		"content_rows", contentRows, "sb_rows", firstVisible)
}

// close shuts down the PTY and sends SIGHUP to the shell so it exits cleanly.
// Safe to call multiple times.
func (p *Pane) close() {
	L.Debug("pane close", "pane", p.id)
	p.closePTX()
	if p.cmd.Process != nil {
		p.cmd.Process.Signal(syscall.SIGHUP) //nolint:errcheck
	}
}

// closePTX closes the PTY master exactly once.  Closing the master causes the
// kernel to send HUP to the shell's controlling terminal.
func (p *Pane) closePTX() {
	p.ptmxOnce.Do(func() { p.ptmx.Close() })
}

// ---------------------------------------------------------------------------
// Text selection helpers (all require p.mu to be held by the caller)
// ---------------------------------------------------------------------------

// selNorm returns the selection endpoints in top-left → bottom-right order.
func (p *Pane) selNorm() (start, end selPos) {
	a, c := p.selAnchor, p.selCursor
	if a.row < c.row || (a.row == c.row && a.col <= c.col) {
		return a, c
	}
	return c, a
}

// selContainsUnlocked reports whether virtual cell (vRow, col) falls within
// the current selection.  Requires p.mu held.
func (p *Pane) selContainsUnlocked(vRow, col int) bool {
	if !p.selActive {
		return false
	}
	start, end := p.selNorm()
	if vRow < start.row || vRow > end.row {
		return false
	}
	if start.row == end.row {
		return col >= start.col && col <= end.col
	}
	if vRow == start.row {
		return col >= start.col
	}
	if vRow == end.row {
		return col <= end.col
	}
	return true
}

// selText extracts the selected text from the virtual grid (scrollback + live).
// Lines are newline-separated; trailing spaces on each line are trimmed.
// Requires p.mu held.
func (p *Pane) selText() string {
	if !p.selActive {
		return ""
	}
	start, end := p.selNorm()
	if start == end {
		return ""
	}
	cols, rows := p.term.Size()
	sbCount := p.sb.count
	var buf strings.Builder
	for vRow := start.row; vRow <= end.row; vRow++ {
		if vRow > start.row {
			buf.WriteByte('\n')
		}
		var cells []vt10x.Glyph
		if vRow < sbCount {
			cells = p.sb.get(vRow)
		} else if tr := vRow - sbCount; tr >= 0 && tr < rows {
			cells = captureRow(p.term, tr, cols)
		}
		fromCol, toCol := 0, cols-1
		if vRow == start.row {
			fromCol = start.col
		}
		if vRow == end.row {
			toCol = end.col
		}
		var line strings.Builder
		for c := fromCol; c <= toCol && c < cols; c++ {
			ch := rune(' ')
			if cells != nil && c < len(cells) {
				if g := cells[c].Char; g != 0 {
					ch = g
				}
			}
			line.WriteRune(ch)
		}
		buf.WriteString(strings.TrimRight(line.String(), " "))
	}
	return buf.String()
}
