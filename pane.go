// pane.go - PTY spawning and VT100/ANSI emulation bridge.
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
	"time"
	"unicode/utf8"

	"github.com/creack/pty"
	"github.com/hinshun/vt10x"
)

// defaultScrollbackLines is the default maximum number of scrollback lines
// retained per pane.  Overridden by the "scrollback" config field.
// Memory cost per pane:
//   - rawBuf:  scrollbackLines × ~200 bytes/line (raw ANSI, varies by content)
//   - sbRing:  scrollbackLines × cols × ~24 bytes (rendered glyphs, on demand)
const defaultScrollbackLines = 10_000

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

	ptmx     *os.File  // PTY master - write keystrokes here, read shell output here
	ptmxOnce sync.Once // ensures PTY master fd is closed exactly once
	cmd      *exec.Cmd // the shell process

	// mu serialises all access to term (both writes from readPTY and reads from
	// the render goroutine), plus the dead, wantsBracketedPaste, and
	// inSyncUpdate flags.
	mu                  sync.Mutex
	term                vt10x.Terminal // VT100/ANSI state machine
	dead                bool           // true once the shell process has exited
	wantsBracketedPaste bool           // DECSET 2004 enabled by the running app
	inSyncUpdate        bool           // inside a DEC 2026 synchronized update
	cursorStyle         int            // DECSCUSR cursor shape (0-6); 0 = default

	// Scrollback buffer - lines that have scrolled off the vt10x grid top.
	// Protected by mu.
	sb             sbRing // ring buffer of captured rows
	sbOff          int    // 0 = live view; N = N lines above live view
	scrollbackLines int   // max scrollback rows (from config)

	// Text selection state.  Protected by mu.
	// selAnchor is where Button1 was pressed; selCursor tracks the drag endpoint.
	// Both are in virtual (scrollback+live) row/col coordinates.
	selAnchor, selCursor selPos
	selActive            bool

	// searchHL maps (vRow<<32|col) → match type: 1=regular, 2=current (orange).
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
	sshHost       string // remote hostname when fgProcess is "ssh" or "mosh"

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

	// altEntryCursor remembers the primary-screen cursor position at the moment
	// the pane entered the alternate screen.  vt10x shares a single saved-cursor
	// slot for both ESC-7/8 (DECSC/DECRC) and \x1b[?1049h/l, so the primary
	// cursor is not reliably preserved across an alt-screen round-trip.
	// We restore it manually after \x1b[?1049l so programs like gh-copilot
	// (which render inline at the current cursor position) start in the right
	// place.  Protected by mu.
	altEntryCursorX, altEntryCursorY int

	// needsSync is set when the pane exits the alternate screen so that the
	// next render call uses screen.Sync() (full repaint) instead of
	// screen.Show() (differential).  This clears any residual background
	// colours that alt-screen apps like btop may have left in the terminal.
	// Protected by mu.
	needsSync bool

	// kittyStack is the per-pane kitty keyboard protocol flag stack.
	// When pane apps send \x1b[=<flags>u (push), we push onto this stack
	// and respond with the saved value on \x1b[?u (query).
	// On \x1b[<N>u (pop) we pop N levels.
	// All kitty keyboard sequences are stripped before vt10x sees them —
	// vt10x misinterprets the 'u' final byte as DECRC (restore cursor).
	// Protected by mu.
	kittyStack []int

	// Temporary status message (e.g. "COPIED") shown in the status bar.
	// Clears automatically after statusMsgEnd.  Protected by mu.
	statusMsg    string
	statusMsgEnd time.Time
}

// NewPane spawns a shell inside a new PTY with the given geometry, starts the
// VT10x emulator, and launches the background I/O goroutines.
//
//	spawnArgs - argv for the child process; nil or empty means use $SHELL.
//	            Pass containerSpawnArgs(...) here to open the new pane inside
//	            the same container as the pane that was split.
//	redraw    - signalled after each chunk of PTY output
//	paneDead  - receives p when the shell exits
//	done      - closed by the app on shutdown
//	oscCh     - receives OSC 7/8/52 sequences to forward to the host terminal
func NewPane(id, x, y, w, h, scrollback int, dir string, spawnArgs []string, redraw chan struct{}, paneDead chan *Pane, done chan struct{}, oscCh chan<- []byte) (*Pane, error) {
	if w < 2 || h < 1 {
		return nil, fmt.Errorf("pane too small: %dx%d", w, h)
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	if len(spawnArgs) == 0 {
		spawnArgs = []string{shell}
	}

	cmd := exec.Command(spawnArgs[0], spawnArgs[1:]...)
	if dir != "" {
		cmd.Dir = dir
	}

	// Build the child environment.
	// Filter out TERM and COLORTERM from the host before setting our own.
	// (Simply appending would not override them on most shells/kernels.)
	// • TERM=xterm-256color - the emulation profile we advertise.
	// • COLORTERM=truecolor - tells colour-aware apps 24-bit RGB works.
	filtered := make([]string, 0, len(os.Environ()))
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "TERM=") && !strings.HasPrefix(e, "COLORTERM=") {
			filtered = append(filtered, e)
		}
	}
	// BUNK=1 lets shell rc files detect they're running inside bunk and skip
	// auto-launching it again (prevents recursive invocation).
	cmd.Env = append(filtered, "TERM=xterm-256color", "COLORTERM=truecolor", "BUNK=1")

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Rows: uint16(h),
		Cols: uint16(w - 1), // reserve last column for the scrollbar
	})
	if err != nil {
		return nil, fmt.Errorf("pty.StartWithSize: %w", err)
	}

	// Initialise the VT10x state machine.
	// NOTE: we intentionally do NOT use vt10x.WithWriter(ptmx) here.
	// WithWriter makes vt10x write OSC 10/11 colour-query responses (and
	// other device-report replies) directly to the PTY master — i.e. into
	// the shell's stdin.  This causes two problems:
	//   1. SSH panes: response bytes are forwarded to the remote server as
	//      user input, corrupting the remote session.
	//   2. Fast-exiting programs (gh, bat --paging=never, …) may exit
	//      before reading the response; bash readline then echoes the
	//      stale bytes as visible garbage on the prompt line.
	// DA/DA2/CPR responses are handled manually in captureAndWrite where
	// we can gate them on specific conditions (e.g. non-alt-screen).
	term := vt10x.New(vt10x.WithSize(w-1, h))

	p := &Pane{
		id: id, x: x, y: y, w: w, h: h,
		ptmx: ptmx, cmd: cmd, term: term,
		scrollbackLines: scrollback,
		sb:               sbRing{maxLines: scrollback},
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
	buf := make([]byte, 32768)
	var carry []byte // incomplete UTF-8 tail from previous read
	for {
		n, err := p.ptmx.Read(buf)
		if n > 0 {
			chunk := buf[:n]

			// Prepend any incomplete UTF-8 bytes carried from the previous read.
			if len(carry) > 0 {
				chunk = append(carry, chunk...)
				carry = nil
			}

			// If the chunk ends mid-UTF-8 sequence, split off the trailing
			// incomplete bytes so vt10x (and the OSC scanner) only see
			// complete runes.  Without this, a multi-byte character like ▒
			// (3 bytes) straddling the read boundary is lost: vt10x treats
			// each fragment as invalid and drops them.
			if split := utf8Boundary(chunk); split < len(chunk) {
				carry = make([]byte, len(chunk)-split)
				copy(carry, chunk[split:])
				chunk = chunk[:split]
			}

			L.Log(nil, LevelTrace, "readPTY: chunk", "pane", p.id, "data", fmt.Sprintf("%q", chunk))

			// Step 1 - OSC passthrough (CWD, hyperlinks, clipboard).
			p.oscScan.Scan(chunk, oscCh)

			// Step 2 - track DECSET 2004 (bracketed paste) and
			// DEC 2026 (synchronized update).
			// Check for both enable and disable; the last occurrence wins
			// when a chunk contains both (e.g. an app toggles the mode).
			enableBP := bytes.LastIndex(chunk, []byte("\x1b[?2004h"))
			disableBP := bytes.LastIndex(chunk, []byte("\x1b[?2004l"))
			enableSU := bytes.LastIndex(chunk, []byte("\x1b[?2026h"))
			disableSU := bytes.LastIndex(chunk, []byte("\x1b[?2026l"))

			// Step 2.5 - track DECSCUSR (cursor shape).
			// Sequence: ESC [ Ps SP q  (e.g. \x1b[5 q = blinking bar).
			curStyle := scanCursorStyle(chunk)

			// Steps 2.5–4 under a single lock to prevent render from seeing
			// updated flags (cursorStyle, inSyncUpdate) with stale vt10x cells.
			p.mu.Lock()
			if enableBP >= 0 || disableBP >= 0 {
				p.wantsBracketedPaste = enableBP > disableBP
			}
			if enableSU >= 0 || disableSU >= 0 {
				p.inSyncUpdate = enableSU > disableSU
			}
			if curStyle >= 0 {
				p.cursorStyle = curStyle
			}
			p.captureAndWrite(chunk)
			scrolling := p.sbOff > 0
			syncing := p.inSyncUpdate
			p.mu.Unlock()

			// Step 5 - wake the render loop (coalesced).
			// Skip when the user is reading scrollback: new output is buffered
			// silently so the visible view doesn't jump while they are reading.
			// Skip during synchronized updates (DEC 2026): the app is
			// building a frame — repaint only when the end marker arrives.
			if !scrolling && !syncing {
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
	if L.Enabled(nil, LevelTrace) {
		cur := p.term.Cursor()
		L.Log(nil, LevelTrace, "captureAndWrite: start", "pane", p.id, "cursor_y", cur.Y, "cursor_x", cur.X, "alt", altScreen, "chunk_len", len(chunk))
	}

	// Respond to terminal capability queries emitted by local programs
	// (e.g. BubbleTea inline apps like gh-copilot).  Answering immediately
	// eliminates multi-second startup delays caused by unanswered-query
	// timeouts.  Skip for SSH/mosh panes: writing to ptmx there forwards
	// bytes to the remote server as user input, corrupting the session.
	if p.fgProcess != "ssh" && p.fgProcess != "mosh" {
		// DA - Primary Device Attributes: ESC [ c or ESC [ 0 c
		// Many TUI apps (neovim, BubbleTea, zellij) send this at startup to
		// detect terminal capabilities.  Without a response they time out or
		// fall back to degraded mode.  We identify as VT220 with ANSI colour,
		// which is the minimum needed for modern apps to enable 256-colour /
		// truecolor support.
		if bytes.Contains(chunk, []byte("\x1b[c")) || bytes.Contains(chunk, []byte("\x1b[0c")) {
			// Response: VT220 with ANSI colour (62), columns (1), ANSI text
			// locator (9).  Matches what xterm-256color reports.
			p.ptmx.Write([]byte("\x1b[?62;1;2;4;6;9;15;22c")) //nolint:errcheck
			L.Log(nil, LevelTrace, "captureAndWrite: DA response", "pane", p.id)
		}
		// DA2 - Secondary Device Attributes: ESC [ > c or ESC [ > 0 c
		// Reports terminal type and version.  We identify as xterm (type 0).
		if bytes.Contains(chunk, []byte("\x1b[>c")) || bytes.Contains(chunk, []byte("\x1b[>0c")) {
			p.ptmx.Write([]byte("\x1b[>0;279;0c")) //nolint:errcheck
			L.Log(nil, LevelTrace, "captureAndWrite: DA2 response", "pane", p.id)
		}
		// CPR - cursor position report: ESC [ 6 n → ESC [ row ; col R
		// BubbleTea sends this at startup to know where to render inline UI.
		// Reply with the actual cursor position so the app renders right after
		// the command line, matching normal terminal behaviour.
		if bytes.Contains(chunk, []byte("\x1b[6n")) && !altScreen {
			cur := p.term.Cursor()
			resp := fmt.Sprintf("\x1b[%d;%dR", cur.Y+1, cur.X+1)
			p.ptmx.Write([]byte(resp)) //nolint:errcheck
			L.Log(nil, LevelTrace, "captureAndWrite: CPR response", "pane", p.id, "row", cur.Y+1, "col", cur.X+1)
		}
		// OSC 10/11 (fg/bg colour queries) — NOT answered.
		// Writing the response to ptmx injects bytes into the shell's stdin.
		// Fast-exiting programs (e.g. `gh` printing help) send the query via
		// BubbleTea init and exit before the response arrives; bash's readline
		// then picks up the stale bytes and echoes them as visible garbage.
		// BubbleTea has a ~100 ms timeout and falls back to default colours,
		// so not responding is safe and avoids the race entirely.
	}

	// Kitty keyboard protocol — bunk acts as the "terminal" for pane apps.
	// Apps negotiate with us using three sequence types:
	//   \x1b[?u        - query current flags  → respond with \x1b[?<flags>u
	//   \x1b[>|=<n>u   - push flags onto stack (> per spec, = legacy)
	//   \x1b[<N>u      - pop N levels (N defaults to 1)
	// All are stripped before vt10x sees them; vt10x interprets the bare 'u'
	// final byte as DECRC (restore cursor), jumping the cursor to 0,0.
	if bytes.ContainsAny(chunk, "u") && bytes.Contains(chunk, []byte("\x1b[")) {
		chunk = p.handleKittyKeyboard(chunk)
	}

	// Accumulate raw bytes for replay-based reflow on resize.
	// Skip while alternate screen is active (vim, htop, etc.) — those apps
	// paint absolute-position content that doesn't replay meaningfully.
	if !altScreen {
		p.rawBuf = append(p.rawBuf, chunk...)
		rawMax := p.scrollbackLines * 200
		if len(p.rawBuf) > rawMax {
			// Trim from the front at a clean newline boundary so we don't
			// start playback in the middle of an ANSI escape sequence.
			excess := len(p.rawBuf) - rawMax
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

	// If this chunk crosses an alt-screen entry point:
	//   1. Save the primary cursor so we can restore it on exit (vt10x shares
	//      a single saved-cursor slot for ESC-7/8 and \x1b[?1049h/l, so the
	//      real primary position is lost once the alt-screen swap happens).
	//   2. Split processing and inject \x1b[2J\x1b[H right after the entry so
	//      the alt-screen starts with a clean slate (it may contain stale
	//      content if a previous TUI program crashed without sending \x1b[?1049l).
	wrote := false
	exitSplitAt := 0 // byte offset into chunk just after the ?1049l/47l sequence, 0 if none
	if !altScreen {
		for _, seq := range []string{"\x1b[?1049h", "\x1b[?1047h", "\x1b[?47h"} {
			b := []byte(seq)
			if i := bytes.Index(chunk, b); i >= 0 {
				// Write everything BEFORE the alt-screen entry sequence first,
				// so any cursor-movement sequences in this chunk (e.g. vim's
				// \r\n or \x1b[row;colH) update the vt10x cursor position.
				// Only then read the cursor — otherwise over SSH where many
				// sequences arrive in one chunk, the pre-entry cursor moves
				// are missed and we save y=0 instead of the actual prompt row.
				if i > 0 {
					p.term.Write(chunk[:i]) //nolint:errcheck
				}
				cur := p.term.Cursor()
				p.altEntryCursorX, p.altEntryCursorY = cur.X, cur.Y
				L.Log(nil, LevelTrace, "captureAndWrite: alt-screen entry", "pane", p.id, "seq", seq, "cursor_x", cur.X, "cursor_y", cur.Y)

				end := i + len(b)
				p.term.Write(chunk[i:end])             //nolint:errcheck
				p.term.Write([]byte("\x1b[2J\x1b[H")) //nolint:errcheck
				if end < len(chunk) {
					p.term.Write(chunk[end:]) //nolint:errcheck
				}
				prevGrid = nil // alt-screen now active; skip primary-row scrollback push
				wrote = true
				break
			}
		}
	}
	// If this chunk crosses an alt-screen EXIT point:
	//   1. Inject \x1b[0m right after the exit sequence to prevent TUI background
	//      colour bleeding into the restored primary screen.
	//   2. Inject curRestore BEFORE writing chunk[end:] — over SSH, bash often
	//      sends the shell prompt in the same chunk as \x1b[?1049l.  If curRestore
	//      were injected after chunk[end:], it would rewind the cursor to x=0 on
	//      the prompt line (undoing the natural cursor advance from the prompt text)
	//      causing subsequent input to overwrite PS1.
	if !wrote && altScreen {
		for _, seq := range []string{"\x1b[?1049l", "\x1b[?1047l", "\x1b[?47l"} {
			b := []byte(seq)
			if i := bytes.Index(chunk, b); i >= 0 {
				end := i + len(b)
				exitSplitAt = end
				p.term.Write(chunk[:end])       //nolint:errcheck
				p.term.Write([]byte("\x1b[0m")) //nolint:errcheck
				// Restore primary cursor here — before any trailing output
				// in this chunk (e.g. prompt text) — so the prompt text
				// advances the cursor naturally to the correct position.
				L.Log(nil, LevelTrace, "captureAndWrite: alt-screen exit", "pane", p.id, "cursor_x", p.altEntryCursorX, "cursor_y", p.altEntryCursorY)
				curRestore := fmt.Sprintf("\x1b[%d;%dH", p.altEntryCursorY+1, p.altEntryCursorX+1)
				L.Log(nil, LevelTrace, "captureAndWrite: injecting curRestore", "pane", p.id, "seq", curRestore)
				p.term.Write([]byte(curRestore)) //nolint:errcheck
				if end < len(chunk) {
					p.term.Write(chunk[end:]) //nolint:errcheck
				}
				wrote = true
				break
			}
		}
	}
	if !wrote {
		p.term.Write(chunk) //nolint:errcheck
	}

	// When alt screen exits, append the chunk to rawBuf (skipped above because
	// altScreen=true) and insert the SGR reset at the split point so rawBuf
	// replay uses default colours for subsequent clears.
	if altScreen && p.term.Mode()&vt10x.ModeAltScreen == 0 {
		// Reset kitty keyboard protocol stack.  TUI apps push onto the stack
		// when entering alt-screen but may not pop if they crash, are killed,
		// or exit abnormally.  Clearing here prevents stale flags from
		// affecting the shell after the alt-screen app is gone.
		p.kittyStack = p.kittyStack[:0]

		const sgrReset = "\x1b[0m"
		if exitSplitAt > 0 {
			// Insert sgrReset into rawBuf right after the exit sequence so
			// replay sees the same colour-reset behaviour as the live path.
			p.rawBuf = append(p.rawBuf, chunk[:exitSplitAt]...)
			p.rawBuf = append(p.rawBuf, sgrReset...)
			p.rawBuf = append(p.rawBuf, chunk[exitSplitAt:]...)
		} else {
			// Fallback: exit sequence not found in this chunk (e.g. split
			// across two reads).  curRestore was not injected above, so do it
			// now before any further output from this chunk.
			p.rawBuf = append(p.rawBuf, chunk...)
			p.term.Write([]byte(sgrReset)) //nolint:errcheck
			p.rawBuf = append(p.rawBuf, sgrReset...)
			L.Log(nil, LevelTrace, "captureAndWrite: alt-screen exit (fallback)", "pane", p.id, "cursor_x", p.altEntryCursorX, "cursor_y", p.altEntryCursorY)
			curRestore := fmt.Sprintf("\x1b[%d;%dH", p.altEntryCursorY+1, p.altEntryCursorX+1)
			L.Log(nil, LevelTrace, "captureAndWrite: injecting curRestore (fallback)", "pane", p.id, "seq", curRestore)
			p.term.Write([]byte(curRestore)) //nolint:errcheck
		}
		rawMax := p.scrollbackLines * 200
		if len(p.rawBuf) > rawMax {
			excess := len(p.rawBuf) - rawMax
			if nl := bytes.IndexByte(p.rawBuf[excess:], '\n'); nl >= 0 {
				p.rawBuf = p.rawBuf[excess+nl+1:]
			} else {
				p.rawBuf = p.rawBuf[excess:]
			}
		}

		// Signal the render loop to do a full Sync() repaint so any residual
		// background colour from the TUI app is cleared from the terminal.
		p.needsSync = true
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
			oldCount := p.sb.count
			oldSbOff := p.sbOff
			for i := 0; i < shift; i++ {
				p.sb.push(prevGrid[i])
			}
			p.adjustAfterScrollbackPush(shift, oldCount, oldSbOff)
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
			oldCount := p.sb.count
			oldSbOff := p.sbOff
			for i := 0; i < pushed; i++ {
				p.sb.push(prevGrid[i])
			}
			p.adjustAfterScrollbackPush(pushed, oldCount, oldSbOff)
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

// SetStatus shows msg in the pane's status bar for dur, then clears it.
func (p *Pane) SetStatus(msg string, dur time.Duration) {
	p.mu.Lock()
	p.statusMsg = msg
	p.statusMsgEnd = time.Now().Add(dur)
	p.mu.Unlock()
}

// resize updates the pane's screen position and dimensions, sends TIOCSWINSZ
// to the PTY (causing the shell to receive SIGWINCH), and resizes the vt10x
// grid.  If the width changes, the existing terminal content is captured and
// re-injected so it reflows naturally to the new column width instead of being
// silently truncated.
func (p *Pane) resize(x, y, w, h int) {
	L.Debug("pane resize", "pane", p.id, "x", x, "y", y, "w", w, "h", h)
	p.mu.Lock()
	p.x, p.y, p.w, p.h = x, y, w, h
	p.resizeAndReflow(w-1, h)
	p.mu.Unlock()
	if p.ptmx != nil {
		pty.Setsize(p.ptmx, &pty.Winsize{ //nolint:errcheck
			Rows: uint16(h),
			Cols: uint16(w - 1), // last column reserved for scrollbar
		})
	}
}

// resizePTYOnly updates the pane's screen coordinates and PTY size without
// performing the expensive rawBuf replay.  This gives the shell an immediate
// SIGWINCH so it can start redrawing while the full reflow is deferred.
func (p *Pane) resizePTYOnly(x, y, w, h int) {
	p.mu.Lock()
	p.x, p.y, p.w, p.h = x, y, w, h
	// Alt-screen apps (btop, vim, …) redraw immediately after SIGWINCH.
	// If vt10x is still at the old size when that redraw arrives, the
	// output is parsed at the wrong grid dimensions → corruption.
	// The vt10x resize is cheap for alt-screen (no rawBuf replay), so
	// do it here before sending SIGWINCH.
	if p.term.Mode()&vt10x.ModeAltScreen != 0 {
		p.term.Write([]byte("\x1b[0m")) //nolint:errcheck
		p.term.Resize(w-1, h)
	}
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
	if newCols < 1 || newRows < 1 {
		return // terminal too small to be usable
	}
	oldCols, oldRows := p.term.Size()
	if oldCols == newCols && oldRows == newRows {
		return
	}

	if p.term.Mode()&vt10x.ModeAltScreen != 0 {
		// Reset cursor attributes before resize so that vt10x clears the
		// newly created cells (in both the alt and saved normal screen)
		// with default colours instead of the TUI app's current style.
		// Without this, exiting the alt-screen app after a resize leaks
		// its background colour into the normal screen's expanded area.
		p.term.Write([]byte("\x1b[0m")) //nolint:errcheck
		p.term.Resize(newCols, newRows)
		return
	}

	if len(p.rawBuf) == 0 {
		// No history yet (brand-new pane); plain resize is sufficient.
		p.term.Resize(newCols, newRows)
		return
	}

	// Fast path: when only the height changed (cols unchanged), we don't
	// need to re-wrap text.  Just redistribute rows between the scrollback
	// ring and the live terminal grid.
	if oldCols == newCols {
		p.resizeHeightOnly(newCols, oldRows, newRows)
		return
	}

	// Replay raw bytes into a tall scratch terminal so nothing scrolls off
	// during replay and we can read back all rows.
	//
	// Alt-screen sessions (vim, htop, etc.) are stripped from the replay
	// buffer — their absolute-position content doesn't replay meaningfully,
	// and stripping avoids allocating a large alt-screen buffer in the
	// scratch terminal.  Pre-vim shell history is preserved.
	replay := stripAltScreen(p.rawBuf)

	// Estimate the replay height from the raw byte count.  A typical
	// terminal line is ~40-80 visible characters plus ANSI escapes, but
	// can be as short as a single newline.  Using a conservative estimate
	// of rawBuf_bytes / newCols gives an upper bound on the number of
	// wrapped lines.  Cap at scrollbackLines + newRows to avoid
	// over-allocation, but also avoid the full allocation when the buffer
	// is small (e.g. a fresh pane with only a few lines of output).
	estimatedLines := len(replay)/max(newCols, 1) + newRows
	replayH := min(estimatedLines, p.scrollbackLines+newRows)
	if replayH < newRows {
		replayH = newRows
	}

	scratch := vt10x.New(vt10x.WithSize(newCols, replayH))
	// Prepend a full SGR reset so trimmed attribute state doesn't bleed.
	scratch.Write(append([]byte("\x1b[0m"), replay...)) //nolint:errcheck

	contentRows := findContentRows(scratch, newCols, replayH)

	// Split: rows [0, firstVisible) → scrollback; [firstVisible, contentRows) → live terminal.
	firstVisible := contentRows - newRows
	if firstVisible < 0 {
		firstVisible = 0
	}

	// Rebuild scrollback from the replay.
	p.sb = sbRing{maxLines: p.scrollbackLines}
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

// resizeHeightOnly handles the common case where only the row count changed
// (columns stayed the same).  No text re-wrapping is needed — we just
// redistribute existing rows between the scrollback ring and the live grid.
// This avoids the expensive rawBuf replay that resizeAndReflow does for
// column-width changes.
//
// Must be called with p.mu held.
func (p *Pane) resizeHeightOnly(cols, oldRows, newRows int) {
	if newRows > oldRows {
		// Terminal grew taller: pull rows from scrollback into the live grid.
		// Capture the current live grid, resize vt10x, then inject the
		// combined rows (pulled scrollback + old live content).
		extra := newRows - oldRows
		pull := extra
		if pull > p.sb.count {
			pull = p.sb.count
		}

		// Collect rows to inject: pulled scrollback + existing live rows.
		combined := make([][]vt10x.Glyph, newRows)
		for i := 0; i < pull; i++ {
			combined[i] = p.sb.get(p.sb.count - pull + i)
		}
		for r := 0; r < oldRows; r++ {
			combined[pull+r] = captureRow(p.term, r, cols)
		}
		// Blank padding for remaining rows.
		for r := pull + oldRows; r < newRows; r++ {
			combined[r] = make([]vt10x.Glyph, cols)
		}

		// Shrink scrollback by the pulled amount.
		if pull > 0 {
			newSB := sbRing{maxLines: p.scrollbackLines}
			for i := 0; i < p.sb.count-pull; i++ {
				newSB.push(p.sb.get(i))
			}
			p.sb = newSB
		}
		p.sbOff = 0

		p.term = vt10x.New(vt10x.WithSize(cols, newRows))
		reflowInject(p.term, combined)
	} else {
		// Terminal shrank: push excess live rows into scrollback.
		// First find the actual content extent — trailing blank rows should
		// be discarded rather than causing content to scroll off the top.
		contentRows := findContentRows(p.term, cols, oldRows)
		excess := contentRows - newRows
		if excess < 0 {
			excess = 0
		}
		for r := 0; r < excess; r++ {
			p.sb.push(captureRow(p.term, r, cols))
		}
		// Capture the visible rows (starting from where content begins
		// to fit in the new height).
		remaining := make([][]vt10x.Glyph, newRows)
		for r := 0; r < newRows; r++ {
			srcRow := excess + r
			if srcRow < oldRows {
				remaining[r] = captureRow(p.term, srcRow, cols)
			} else {
				remaining[r] = make([]vt10x.Glyph, cols)
			}
		}
		p.sbOff = 0

		p.term = vt10x.New(vt10x.WithSize(cols, newRows))
		reflowInject(p.term, remaining)
	}

	L.Debug("resizeHeightOnly: done", "pane", p.id,
		"cols", cols, "oldRows", oldRows, "newRows", newRows,
		"sb_count", p.sb.count)
}

// utf8Boundary returns the largest index i (0 ≤ i ≤ len(b)) such that b[:i]
// contains only complete UTF-8 sequences.  b[i:] is the trailing incomplete
// sequence (if any) that should be carried over to the next read.
//
// Only the last 3 bytes need to be inspected because the longest UTF-8
// sequence is 4 bytes and a complete sequence at the end is always valid.
func utf8Boundary(b []byte) int {
	n := len(b)
	if n == 0 || b[n-1] < 0x80 {
		return n // ends with ASCII — always complete
	}
	// Walk backwards up to 3 bytes looking for the start byte.
	for i := 1; i <= 3 && i <= n; i++ {
		c := b[n-i]
		if c < 0x80 {
			return n // hit an ASCII byte; everything after is invalid but
			// that can't happen in a well-formed stream — treat as complete.
		}
		if utf8.RuneStart(c) {
			// Found start byte.  A start byte at n-i needs seqLen bytes.
			var seqLen int
			switch {
			case c < 0xE0:
				seqLen = 2
			case c < 0xF0:
				seqLen = 3
			default:
				seqLen = 4
			}
			if i < seqLen {
				return n - i // incomplete — split here
			}
			return n // sequence is complete
		}
		// continuation byte (0x80..0xBF) — keep scanning
	}
	return n
}

// scanCursorStyle returns the DECSCUSR parameter (0-6) from the last
// \x1b[N q sequence in data, or -1 if not found.
// The DECSCUSR format is: CSI Ps SP q  (ESC [ digit space q).
func scanCursorStyle(data []byte) int {
	for i := len(data) - 1; i >= 4; i-- {
		if data[i] == 'q' && data[i-1] == ' ' &&
			data[i-2] >= '0' && data[i-2] <= '6' &&
			data[i-3] == '[' && data[i-4] == '\x1b' {
			return int(data[i-2] - '0')
		}
	}
	return -1
}

// findContentRows scans backwards from the bottom of a scratch terminal to
// find the last non-blank row.  Returns the number of rows with content
// (i.e. the first blank-only row index from the top).
func findContentRows(t vt10x.Terminal, cols, totalRows int) int {
	cur := t.Cursor()
	contentRows := cur.Y + 1
	for r := totalRows - 1; r >= contentRows; r-- {
		blank := true
		for c := 0; c < cols; c++ {
			g := t.Cell(c, r)
			if g.Char != 0 && g.Char != ' ' {
				blank = false
				break
			}
		}
		if !blank {
			contentRows = r + 1
			break
		}
	}
	return contentRows
}

// adjustAfterScrollbackPush updates sbOff and selection coordinates after
// `pushed` rows have been added to the scrollback ring.  oldCount and
// oldSbOff are the values before the push.  Must be called with p.mu held.
func (p *Pane) adjustAfterScrollbackPush(pushed, oldCount, oldSbOff int) {
	if p.sbOff > 0 {
		p.sbOff += pushed
		if p.sbOff > p.sb.count {
			p.sbOff = p.sb.count
		}
	}
	if p.selActive {
		oldVT := oldCount - oldSbOff
		newVT := p.sb.count - p.sbOff
		if d := newVT - oldVT; d != 0 {
			p.selAnchor.row += d
			p.selCursor.row += d
		}
	}
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
	replayH := p.scrollbackLines + rows
	scratch := vt10x.New(vt10x.WithSize(cols, replayH))
	replay := stripAltScreen(p.rawBuf)
	scratch.Write(append([]byte("\x1b[0m"), replay...)) //nolint:errcheck

	contentRows := findContentRows(scratch, cols, replayH)
	firstVisible := contentRows - rows
	if firstVisible < 0 {
		firstVisible = 0
	}
	oldCount := p.sb.count
	p.sb = sbRing{maxLines: p.scrollbackLines}
	for r := 0; r < firstVisible; r++ {
		p.sb.push(captureRow(scratch, r, cols))
	}

	// If the ring grew, adjust selection virtual-row coordinates by the same
	// delta so they still point at the same content after the rebuild.
	if delta := p.sb.count - oldCount; delta > 0 && p.selActive {
		p.selAnchor.row += delta
		p.selCursor.row += delta
	}

	L.Debug("rebuildScrollbackFromRawBuf", "pane", p.id,
		"content_rows", contentRows, "sb_rows", firstVisible, "delta", p.sb.count-oldCount)
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

// cwd returns the working directory of the pane's foreground process (or the
// shell itself).  Returns "" if the information is unavailable or the path
// doesn't exist on the host (e.g. a container-internal path).
func (p *Pane) cwd() string {
	if p.cmd.Process == nil {
		return ""
	}
	pid := p.cmd.Process.Pid
	// Try the foreground process first, fall back to the shell itself.
	if pgid := termFgPGID(pid); pgid > 0 {
		if d, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pgid)); err == nil {
			if info, err := os.Stat(d); err == nil && info.IsDir() {
				return d
			}
		}
	}
	d, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
	if err != nil {
		return ""
	}
	if info, err := os.Stat(d); err == nil && info.IsDir() {
		return d
	}
	return ""
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
// Soft-wrapped rows (attrWrap on the last cell) are joined without a newline
// so that copy-paste preserves the original logical lines.
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
	var prevCells []vt10x.Glyph
	for vRow := start.row; vRow <= end.row; vRow++ {
		var cells []vt10x.Glyph
		if vRow < sbCount {
			cells = p.sb.get(vRow)
		} else if tr := vRow - sbCount; tr >= 0 && tr < rows {
			cells = captureRow(p.term, tr, cols)
		}
		if vRow > start.row {
			// Only insert \n if the previous row ended with a hard break.
			// Soft-wrapped rows have attrWrap on their last cell.
			softWrap := false
			if prevCells != nil && len(prevCells) > 0 {
				softWrap = prevCells[len(prevCells)-1].Mode&vtAttrWrap != 0
			}
			if !softWrap {
				buf.WriteByte('\n')
			}
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
		// Only trim trailing spaces on hard-break rows; soft-wrapped rows
		// are full-width by definition.
		s := line.String()
		if cells == nil || len(cells) == 0 || cells[len(cells)-1].Mode&vtAttrWrap == 0 {
			s = strings.TrimRight(s, " ")
		}
		buf.WriteString(s)
		prevCells = cells
	}
	return buf.String()
}

// handleKittyKeyboard processes kitty keyboard protocol sequences in the
// byte stream emitted by pane applications, strips them before vt10x sees
// them (to prevent DECRC misinterpretation), and responds to queries.
//
// Three sequence types (all use the 'u' final byte):
//
//	\x1b [ ? u         - query: respond with \x1b[?<flags>u (top of stack or 0)
//	\x1b [ > <n> u     - push: save current flags and activate <n>  (spec)
//	\x1b [ = <n> u     - push: same, legacy form used by some apps
//	\x1b [ < <N> u     - pop:  pop N stack levels (N omitted → 1)
//
// Must be called with p.mu held (captureAndWrite contract).
// Writing to p.ptmx (the PTY) does not require p.mu.
func (p *Pane) handleKittyKeyboard(chunk []byte) []byte {
	// We walk through chunk looking for ESC [ followed by a kitty intro byte
	// (?, =, <). copied tracks how far we've flushed into out; out is nil
	// until we actually strip something (avoids allocation when nothing matches).
	var out []byte
	copied := 0 // index up to which chunk has been appended to out

	i := 0
	for i < len(chunk) {
		esc := bytes.Index(chunk[i:], []byte("\x1b["))
		if esc < 0 {
			break // no more ESC [ — remainder is flushed below
		}
		abs := i + esc // absolute position of ESC in chunk
		j := abs + 2   // first byte after '['
		if j >= len(chunk) {
			break // truncated sequence at end of chunk — pass through
		}

		intro := chunk[j]
		stripped := false
		newI := abs // default: don't skip (fall through to next iteration)

		switch intro {
		case '?':
			// \x1b[?u  (exactly 4 bytes: ESC [ ? u)
			if j+1 < len(chunk) && chunk[j+1] == 'u' {
				flags := 0
				if len(p.kittyStack) > 0 {
					flags = p.kittyStack[len(p.kittyStack)-1]
				}
				p.ptmx.Write([]byte(fmt.Sprintf("\x1b[?%du", flags))) //nolint:errcheck
				newI = j + 2
				stripped = true
			}
		case '>', '=':
			// \x1b[><digits>u (spec) or \x1b[=<digits>u (legacy)
			k := j + 1
			for k < len(chunk) && chunk[k] >= '0' && chunk[k] <= '9' {
				k++
			}
			if k < len(chunk) && chunk[k] == 'u' {
				flags := 0
				fmt.Sscanf(string(chunk[j+1:k]), "%d", &flags)
				p.kittyStack = append(p.kittyStack, flags)
				newI = k + 1
				stripped = true
			}
		case '<':
			// \x1b[<N>u  (N is optional digits, default 1)
			k := j + 1
			for k < len(chunk) && chunk[k] >= '0' && chunk[k] <= '9' {
				k++
			}
			if k < len(chunk) && chunk[k] == 'u' {
				count := 1
				fmt.Sscanf(string(chunk[j+1:k]), "%d", &count)
				if count < 1 {
					count = 1
				}
				if count >= len(p.kittyStack) {
					p.kittyStack = p.kittyStack[:0]
				} else {
					p.kittyStack = p.kittyStack[:len(p.kittyStack)-count]
				}
				newI = k + 1
				stripped = true
			}
		}

		if stripped {
			// Flush bytes before this sequence into out.
			if out == nil {
				out = make([]byte, 0, len(chunk))
			}
			out = append(out, chunk[copied:abs]...)
			copied = newI
			i = newI
		} else {
			// Not a kitty sequence; skip past ESC [ and continue.
			i = abs + 2
		}
	}

	if out == nil {
		return chunk // nothing stripped
	}
	// Flush remaining bytes.
	out = append(out, chunk[copied:]...)
	return out
}
