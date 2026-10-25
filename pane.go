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
	"bunk/internal/session"
	"bytes"
	"fmt"
	"time"

	"github.com/creack/pty"
)

// Pane is bunk's view of a Session: its place on the host screen and the
// render and status state of the tcell front end.
type Pane struct {
	*session.Session

	x, y int // top-left corner on the host screen (content area, 0-indexed)
	w, h int // width and height of the content area in cells

	// Dirty-render tracking: renderPane compares these against the current
	// overlay state to decide between full repaint and dirty-row-only repaint.
	// All fields protected by mu.
	lastRenderSbOff       int
	lastRenderSelActive   bool
	lastRenderSelAnchor   session.SelPos
	lastRenderSelCursor   session.SelPos
	lastRenderSearchHLGen int
	lastRenderStatusKey   string
	lastRenderColorGen    uint64

	// forceFullRepaint makes the next renderPane call repaint every row
	// regardless of vt10x dirty state.  Set when tcell's cell buffer no
	// longer reflects this pane's content — e.g. after another pane was
	// zoomed fullscreen on top of it.  Protected by mu.
	forceFullRepaint bool

	// transientLineClear is set after a CR-space-CR clear-only progress chunk.
	// The vt10x state is temporarily blank, but a replacement chunk usually
	// follows immediately. Render skips frames while this is set so it doesn't
	// sample and paint the transient blank row.
	// Protected by mu.
	transientLineClear      bool
	transientLineClearUntil time.Time

	// progressCursorHiddenUntil suppresses the host cursor during rapid
	// carriage-return status/progress rewrites. Some CLI progress bars do not
	// hide the cursor themselves; showing a block cursor while the row is
	// rewritten hundreds of times per second reads as flicker.
	// Protected by mu.
	progressCursorHiddenUntil time.Time

	passthrough bool // protected by mu

	// Temporary status message (e.g. "COPIED") shown in the status bar.
	// Clears automatically after statusMsgEnd.  Protected by mu.
	statusMsg    string
	statusMsgEnd time.Time

	// Output-hook state, owned by the session's reader goroutine.
	transientClearTimer   *time.Timer
	progressCursorTimer   *time.Timer
	pendingTransientClear bool
	pendingClearUntil     time.Time
	pendingInPlaceUpdate  bool
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
//	colors    - default OSC 10/11/12 colours for the pane (from theme or host probe)
//	oscBuf    - receives OSC 7/8/52/133 sequences to forward to the host terminal
func NewPane(id, x, y, w, h, scrollback int, dir string, spawnArgs []string, colors hostOSCColors, redraw chan struct{}, paneDead chan *Pane, done chan struct{}, oscBuf *oscBuffer, cellAspect ...float64) (*Pane, error) {
	if w < 2 || h < 1 {
		return nil, fmt.Errorf("pane too small: %dx%d", w, h)
	}
	// The PTY is one column narrower than the pane: the last column holds
	// the scrollbar. BUNK=1 lets shell rc files detect they're running
	// inside bunk and skip auto-launching it again.
	s, err := session.Start(session.Options{
		ID: id, Cols: w - 1, Rows: h, Scrollback: scrollback, Dir: dir, Args: spawnArgs,
		Env:     []string{"BUNK=1"},
		ThemeFG: colors.fg, ThemeBG: colors.bg, ThemeCursor: colors.cursor,
		CellAspect: cellAspect,
	})
	if err != nil {
		return nil, err
	}
	p := &Pane{Session: s, x: x, y: y, w: w, h: h}
	L.Debug("pane spawned", "id", p.ID, "x", x, "y", y, "w", w, "h", h)
	s.Run(p.hooks(redraw, paneDead, done, oscBuf), done)
	return p, nil
}

// hooks connects p's session to bunk: OSC passthrough to the host, repaint
// signals, progress-bar repaint pacing and pane removal on exit.
func (p *Pane) hooks(redraw chan struct{}, paneDead chan *Pane, done chan struct{}, oscBuf *oscBuffer) session.Hooks {
	wake := func() {
		select {
		case redraw <- struct{}{}:
		default:
		}
	}
	return session.Hooks{
		Applied: p.outputApplied,
		Output: func(_ []byte, scrolled, syncing bool) {
			p.outputDone(scrolled, syncing, wake)
		},
		OSC: func(seq []byte) {
			oscBuf.append(seq)
			mirrorOSC52ToNativeClipboard(seq)
		},
		Redraw: wake,
		Exited: func() {
			select {
			case paneDead <- p:
			case <-done:
			}
		},
	}
}

// outputApplied runs under the session lock right after a chunk reached the
// emulator, so render never samples a transient blank row before it is
// flagged.
func (p *Pane) outputApplied(chunk []byte, scrolled, syncing bool) {
	now := time.Now()
	visibleTransientClear := isTransientLineClear(chunk) && !scrolled && !syncing
	p.transientLineClear = visibleTransientClear
	until := time.Time{}
	if visibleTransientClear {
		until = now.Add(transientLineClearDelay)
	}
	p.transientLineClearUntil = until
	visibleInPlaceLineUpdate := isInPlaceLineUpdate(chunk) && !scrolled && !syncing
	if visibleInPlaceLineUpdate {
		p.progressCursorHiddenUntil = now.Add(progressCursorHideDelay)
	} else if bytes.Contains(chunk, []byte("\n")) {
		p.progressCursorHiddenUntil = time.Time{}
	}
	p.pendingTransientClear = visibleTransientClear
	p.pendingClearUntil = until
	p.pendingInPlaceUpdate = visibleInPlaceLineUpdate
}

// outputDone wakes the render loop after a chunk, unless the user is reading
// scrollback (new output is buffered silently so the view doesn't jump) or
// the app is building a synchronized frame (repaint when it ends). A
// clear-only progress chunk waits briefly for its replacement.
func (p *Pane) outputDone(scrolled, syncing bool, wake func()) {
	if scrolled || syncing {
		return
	}
	if p.transientClearTimer != nil {
		p.transientClearTimer.Stop()
	}
	if p.pendingTransientClear {
		until := p.pendingClearUntil
		p.transientClearTimer = time.AfterFunc(transientLineClearDelay, func() {
			p.clearTransientLineClearIfUntil(until)
			wake()
		})
		return
	}
	if p.pendingInPlaceUpdate {
		if p.progressCursorTimer == nil {
			p.progressCursorTimer = time.AfterFunc(progressCursorHideDelay, wake)
		} else {
			p.progressCursorTimer.Reset(progressCursorHideDelay)
		}
	}
	wake()
}

func (p *Pane) clearTransientLineClearIfUntil(until time.Time) {
	p.Lock()
	if p.transientLineClearUntil.Equal(until) {
		p.transientLineClear = false
		p.transientLineClearUntil = time.Time{}
	}
	p.Unlock()
}

const transientLineClearDelay = 40 * time.Millisecond
const progressCursorHideDelay = 120 * time.Millisecond

func isTransientLineClear(chunk []byte) bool {
	if len(chunk) < 3 || bytes.ContainsAny(chunk, "\n\x1b") {
		return false
	}

	sawClear := false
	for i := 0; i < len(chunk); {
		if chunk[i] != '\r' {
			return false
		}
		i++

		spaces := 0
		for i < len(chunk) && chunk[i] == ' ' {
			i++
			spaces++
		}
		if spaces == 0 || i >= len(chunk) || chunk[i] != '\r' {
			return false
		}
		i++
		sawClear = true
	}
	return sawClear
}

func isInPlaceLineUpdate(chunk []byte) bool {
	return len(chunk) > 0 &&
		bytes.Contains(chunk, []byte("\r")) &&
		!bytes.ContainsAny(chunk, "\n\x1b")
}

// SetStatus shows msg in the pane's status bar for dur, then clears it.
func (p *Pane) SetStatus(msg string, dur time.Duration) {
	p.Lock()
	p.statusMsg = msg
	p.statusMsgEnd = time.Now().Add(dur)
	p.Unlock()
}

// resize updates the pane's screen position and dimensions, sends TIOCSWINSZ
// to the PTY (causing the shell to receive SIGWINCH), and resizes the vt10x
// grid.  If the width changes, the existing terminal content is captured and
// re-injected so it reflows naturally to the new column width instead of being
// silently truncated.
func (p *Pane) resize(x, y, w, h int) {
	L.Debug("pane resize", "pane", p.ID, "x", x, "y", y, "w", w, "h", h)
	p.Lock()
	p.x, p.y, p.w, p.h = x, y, w, h
	if p.ResizeAndReflow(w-1, h) {
		p.forceFullRepaint = true
	}
	cw, ch := p.Term.CellPixels()
	p.Unlock()
	if p.PTY != nil {
		pty.Setsize(p.PTY, session.Winsize(w-1, h, cw, ch)) //nolint:errcheck
	}
}

// markFullRepaint forces the next renderPane call to repaint every row of
// this pane, even when vt10x reports no dirty rows.  Used when the tcell
// cell buffer under this pane holds another pane's content (zoom exit).
func (p *Pane) markFullRepaint() {
	p.Lock()
	p.forceFullRepaint = true
	p.Unlock()
}
