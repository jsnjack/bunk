// mouse.go - mouse passthrough, focus events, and bracketed-paste forwarding.
//
// Mouse passthrough
// -----------------
// When a shell application enables mouse reporting via DECSET 1000/1002/1003,
// vt10x records the mode flags (ModeMouseButton / ModeMouseMotion /
// ModeMouseMany / ModeMouseX10).  We capture every *tcell.EventMouse the host
// terminal delivers, find the pane under the cursor, check its mouse-mode
// flags, re-encode the event in the format the pane requested (SGR 1006 or
// X10), translate from host-screen coordinates to 1-indexed pane-local
// coordinates, and write the resulting bytes to the pane's PTY master.
//
// Click-to-focus
// --------------
// A button press over an inactive pane switches focus to it regardless of
// whether that pane has requested mouse events.
//
// Focus events (DECSET 1004 / vt10x.ModeFocus)
// ---------------------------------------------
// vt10x tracks DECSET 1004 as ModeFocus.  Whenever the active pane changes we
// send ESC[O (focus out) to the pane losing focus and ESC[I (focus in) to the
// pane gaining focus, but only if the respective pane has ModeFocus set.
// vim/neovim and many other TUI apps rely on these sequences for auto-save,
// cursor-shape changes, etc.
//
// Bracketed paste (DECSET 2004)
// -----------------------------
// vt10x does not track DECSET 2004.  We pre-scan the raw PTY bytes in readPTY
// and set Pane.wantsBracketedPaste ourselves.  When tcell delivers a
// *tcell.EventPaste we wrap the content in ESC[200~…ESC[201~ if the active
// pane has requested it, otherwise we forward the raw text.
//
// ANSI mouse encoding cheat-sheet
// ---------------------------------
//
//	SGR (1006):  ESC [ < Cb ; Cx ; Cy M   (press)
//	             ESC [ < Cb ; Cx ; Cy m   (release)
//	X10/normal:  ESC [ M <b+32> <x+32> <y+32>   (coordinates capped at 223)
//
// tcell vs ANSI button codes:
//
//	tcell.Button1 (left)   → ANSI 0
//	tcell.Button3 (middle) → ANSI 1   (tcell uses Button3 for middle)
//	tcell.Button2 (right)  → ANSI 2
//	WheelUp/Down/Left/Right → 64/65/66/67
package main

import (
	"fmt"
	"time"
	"unicode"

	"github.com/gdamore/tcell/v2"
	"github.com/hinshun/vt10x"
)

// handleMouse processes a host mouse event delivered by tcell.
func (app *App) handleMouse(ev *tcell.EventMouse) {
	x, y := ev.Position()
	btn := ev.Buttons()
	shiftHeld := ev.Modifiers()&tcell.ModShift != 0

	prevBtn := app.prevMouseBtn
	// Don't update prevBtn for wheel events — they don't represent a button
	// state change and would corrupt drag-select detection (making the next
	// Button1 motion look like a fresh press, clearing the selection).
	if btn != tcell.WheelUp && btn != tcell.WheelDown &&
		btn != tcell.WheelLeft && btn != tcell.WheelRight {
		app.prevMouseBtn = btn
	}

	// ── Identify the pane at the cursor position ──────────────────────────
	app.mu.Lock()
	var target *Pane
	if app.zoomedPane != nil {
		// In zoom mode the zoomed pane covers the entire screen.
		target = app.zoomedPane
	} else if app.root != nil {
		for _, leaf := range app.root.leaves() {
			p := leaf.pane
			if x >= p.x && x < p.x+p.w && y >= p.y && y < p.y+p.h {
				target = p
				break
			}
		}
	}

	// ── Click-to-focus ────────────────────────────────────────────────────
	var prevActive *Pane
	// Don't switch focus while a drag-select is in progress.
	if target != nil && btn != tcell.ButtonNone &&
		btn != tcell.WheelUp && btn != tcell.WheelDown &&
		btn != tcell.WheelLeft && btn != tcell.WheelRight &&
		target != app.active && !target.isDead() &&
		app.dragPane == nil {
		prevActive = app.active
		L.Debug("mouse: click-to-focus", "from_pane", func() int {
			if prevActive != nil {
				return prevActive.id
			}
			return -1
		}(), "to_pane", target.id, "x", x, "y", y)
		app.active = target
		app.triggerRedraw()
	}

	var tx, ty int
	if target != nil {
		tx, ty = target.x, target.y
	}
	dragPane := app.dragPane
	app.mu.Unlock()

	// ── Focus events ──────────────────────────────────────────────────────
	if prevActive != nil {
		sendFocusOut(prevActive)
		sendFocusIn(target)
	}

	if target == nil || target.isDead() {
		return
	}

	// ── Resolve mouse mode once ───────────────────────────────────────────
	target.mu.Lock()
	mode := target.term.Mode()
	target.mu.Unlock()
	wantsMouse := mode&vt10x.ModeMouseMask != 0

	// ── Mouse wheel: scrollback vs. passthrough ───────────────────────────
	if btn == tcell.WheelUp || btn == tcell.WheelDown {
		if !wantsMouse {
			scrollAmt := max(1, target.h/4)
			L.Debug("mouse: wheel scroll", "pane", target.id, "btn", btn, "amount", scrollAmt)
			if btn == tcell.WheelUp {
				target.scrollUp(scrollAmt)
			} else {
				target.scrollDown(scrollAmt)
			}

			// If button1 is currently held (drag-select in progress), extend
			// the selection to the newly visible edge so the user can select
			// content beyond the visible viewport by scrolling.
			target.mu.Lock()
			if target.selActive && prevBtn == tcell.Button1 {
				rows := target.h
				if btn == tcell.WheelUp {
					// Scrolled toward the past: extend cursor to top-left of new view.
					vRow := target.sb.count - target.sbOff
					target.selCursor = selPos{row: vRow, col: 0}
				} else {
					// Scrolled toward present: extend cursor to bottom-right.
					vRow := (target.sb.count - target.sbOff) + rows - 1
					cols, _ := target.term.Size()
					target.selCursor = selPos{row: vRow, col: cols - 1}
				}
				L.Debug("mouse: wheel-extend selection", "pane", target.id, "vrow", target.selCursor.row)
			}
			target.mu.Unlock()

			app.triggerRedraw()
			return
		}
		// App wants mouse → fall through to passthrough below.
	}

	// ── Button state helpers (used by scrollbar drag and selection below) ────
	isPress := btn == tcell.Button1 && prevBtn != tcell.Button1
	isDrag := btn == tcell.Button1 && prevBtn == tcell.Button1
	isRelease := btn == tcell.ButtonNone && prevBtn == tcell.Button1

	// ── Scrollbar drag ────────────────────────────────────────────────────
	// The scrollbar occupies the last column of a pane (p.x+p.w-1) when
	// the pane has scrollback history.  We intercept Button1 press/drag/
	// release on that column before the selection and passthrough logic.
	//
	// Drag maths (mirrors drawScrollbar in render.go):
	//   total = sbCount + rows
	//   visibleStart = sbCount - sbOff          (= handleTop * total / rows)
	//   new visibleStart = (newHandleTop) * total / rows
	//   new sbOff       = sbCount - new visibleStart
	if app.sbDragPane != nil && isRelease {
		app.sbDragPane = nil
		app.triggerRedraw()
		return
	}
	if app.sbDragPane != nil && isDrag {
		p := app.sbDragPane
		p.mu.Lock()
		sbCount := p.sb.count
		_, rows := p.term.Size()
		p.mu.Unlock()
		if rows > 0 && sbCount > 0 {
			total := sbCount + rows
			dy := y - app.sbDragAnchorY
			newOff := app.sbDragAnchorOff - dy*total/rows
			if newOff < 0 {
				newOff = 0
			}
			if newOff > sbCount {
				newOff = sbCount
			}
			p.mu.Lock()
			p.sbOff = newOff
			p.mu.Unlock()
		}
		app.triggerRedraw()
		return
	}
	// Detect a Button1 press on the scrollbar column of any pane.
	// Start a drag anchored at the current sbOff — no position jump on click.
	if isPress && target != nil {
		target.mu.Lock()
		sbCount := target.sb.count
		curSbOff := target.sbOff
		target.mu.Unlock()
		if sbCount > 0 && x == target.x+target.w-1 {
			app.sbDragPane = target
			app.sbDragAnchorY = y
			app.sbDragAnchorOff = curSbOff
			app.triggerRedraw()
			return
		}
	}

	// ── Text selection (Button1 drag when no app mouse mode, or Shift held) ──
	// doSelect is true when we should handle drag as a text selection event
	// instead of forwarding it to the pane's PTY.
	doSelect := !wantsMouse || shiftHeld

	if doSelect {
		switch {
		case isPress:
			vPos := screenToVirtual(target, x, y)

			// Double-click: select the word under the cursor.
			const dblClickWindow = 400 * time.Millisecond
			isDouble := time.Since(app.lastClickTime) < dblClickWindow &&
				app.lastClickPane == target &&
				app.lastClickPos == vPos
			app.lastClickTime = time.Now()
			app.lastClickPos = vPos
			app.lastClickPane = target

			if isDouble {
				L.Debug("mouse: double-click word select", "pane", target.id, "vrow", vPos.row, "vcol", vPos.col)
				selectWord(target, vPos)
				app.dblClickActive = true
				app.mu.Lock()
				app.dragPane = target
				app.mu.Unlock()
				app.triggerRedraw()
				return
			}

			L.Debug("mouse: button1 press (clear selection)", "pane", target.id, "vrow", vPos.row, "vcol", vPos.col)
			// Record the pane where the drag starts.
			app.mu.Lock()
			app.dragPane = target
			app.mu.Unlock()
			// Clear any existing selection immediately on press.
			// Selection is only activated once the user starts dragging.
			target.mu.Lock()
			target.selActive = false
			target.selAnchor = vPos
			target.selCursor = vPos
			target.mu.Unlock()
			app.triggerRedraw()
			return

		case isDrag:
			// Clamp to the pane where the drag started.
			sel := dragPane
			if sel == nil {
				sel = target
			}
			// Auto-scroll when the cursor is at the top or bottom edge of the
			// pane.  The goroutine keeps scrolling at a fixed rate while the
			// cursor stays at the edge (mouse motion events stop when stationary).
			atTop := y <= sel.y
			atBot := y >= sel.y+sel.h-1
			if atTop || atBot {
				app.startDragEdgeScroll(sel, atTop)
			} else {
				app.stopDragEdgeScroll()
			}
			vPos := screenToVirtual(sel, x, y)
			L.Debug("mouse: drag select", "pane", sel.id, "vrow", vPos.row, "vcol", vPos.col)
			sel.mu.Lock()
			sel.selActive = true // engage selection on real drag
			sel.selCursor = vPos
			sel.mu.Unlock()
			app.triggerRedraw()
			return

		case isRelease:
			// Stop any edge auto-scroll goroutine.
			app.stopDragEdgeScroll()
			// After a double-click the word selection is already correct;
			// don't overwrite selCursor with the release position.
			app.mu.Lock()
			app.dragPane = nil
			app.mu.Unlock()
			if app.dblClickActive {
				app.dblClickActive = false
				app.triggerRedraw()
				return
			}
			// Clamp release to the pane where the drag started.
			sel := dragPane
			if sel == nil {
				sel = target
			}
			// For a drag-select: finalise the cursor position.
			// We hold sel.mu here, so inline the virtual-coord math
			// instead of calling screenToVirtual (which also locks sel.mu).
			sel.mu.Lock()
			if sel.selActive {
				col := x - sel.x
				row := y - sel.y
				// Clamp coordinates to the pane bounds.
				if col < 0 {
					col = 0
				} else if col >= sel.w {
					col = sel.w - 1
				}
				if row < 0 {
					row = 0
				} else if row >= sel.h {
					row = sel.h - 1
				}
				vRow := (sel.sb.count - sel.sbOff) + row
				sel.selCursor = selPos{row: vRow, col: col}
				L.Debug("mouse: drag-select released", "pane", sel.id, "vrow", vRow, "vcol", col)
			}
			// If selActive is false this was a plain click - selection
			// was already cleared on press, nothing more to do.
			sel.mu.Unlock()
			app.triggerRedraw()
			return
		}
	} else if isPress {
		// App has mouse mode and shift not held: clear any existing selection.
		app.mu.Lock()
		app.dragPane = nil
		app.mu.Unlock()
		target.mu.Lock()
		target.selActive = false
		target.mu.Unlock()
	} else if isRelease {
		app.mu.Lock()
		app.dragPane = nil
		app.mu.Unlock()
	}

	// ── Mouse passthrough to PTY ──────────────────────────────────────────
	if !wantsMouse {
		return
	}
	if btn == tcell.ButtonNone && mode&(vt10x.ModeMouseMotion|vt10x.ModeMouseMany) == 0 {
		return
	}

	px := x - tx + 1
	py := y - ty + 1
	useSGR := mode&vt10x.ModeMouseSgr != 0

	if data := mouseToBytes(btn, prevBtn, ev.Modifiers(), px, py, useSGR); len(data) > 0 {
		target.writeInput(data)
	}
}

// screenToVirtual converts a host-screen position into the pane's unified
// virtual coordinate (scrollback row 0 … sb.count-1, then live rows).
// Must NOT be called with p.mu held.
func screenToVirtual(p *Pane, screenX, screenY int) selPos {
	p.mu.Lock()
	sbOff := p.sbOff
	sbCount := p.sb.count
	p.mu.Unlock()

	col := screenX - p.x
	row := screenY - p.y
	if col < 0 {
		col = 0
	} else if col >= p.w {
		col = p.w - 1
	}
	if row < 0 {
		row = 0
	} else if row >= p.h {
		row = p.h - 1
	}
	return selPos{row: (sbCount - sbOff) + row, col: col}
}

// handlePaste forwards a tcell bracketed-paste event to the active pane.
//
// tcell v2 represents bracketed paste as a pair of EventPaste events:
//   - EventPaste.Start() == true  marks the beginning of paste content
//   - EventPaste.Start() == false marks the end
//
// The actual paste text arrives as regular *tcell.EventKey events between
// the two EventPaste events; handleKey forwards them to the pane's PTY as
// usual.  Our job here is to wrap that content in ESC[200~…ESC[201~ for
// panes that have enabled DECSET 2004 (tracked via Pane.wantsBracketedPaste).
// Panes that haven't opted in receive the content as plain keystrokes.
func (app *App) handlePaste(ev *tcell.EventPaste) {
	app.mu.Lock()
	active := app.active
	app.mu.Unlock()
	if active == nil || active.isDead() {
		return
	}

	active.mu.Lock()
	bracketed := active.wantsBracketedPaste
	active.mu.Unlock()

	L.Debug("handlePaste", "pane", active.id, "start", ev.Start(), "bracketed", bracketed)

	if !bracketed {
		// Pane hasn't requested bracketed paste; content arrives as key events
		// and gets forwarded verbatim by handleKey.  Nothing to do here.
		return
	}

	if ev.Start() {
		active.writeInput([]byte("\x1b[200~"))
	} else {
		active.writeInput([]byte("\x1b[201~"))
	}
}

// mouseToBytes encodes a tcell mouse event into the raw ANSI byte sequence
// the PTY expects.  x and y are 1-indexed pane-local coordinates.
//
// Release detection: when btn is ButtonNone and prevBtn was a real button,
// we generate a release for the button that was just released.
func mouseToBytes(btn, prevBtn tcell.ButtonMask, mod tcell.ModMask, x, y int, sgr bool) []byte {
	release := btn == tcell.ButtonNone && prevBtn != tcell.ButtonNone

	// Which button is relevant for this event?
	activebtn := btn
	if release {
		activebtn = prevBtn
	}

	// Map tcell buttons to ANSI button codes.
	// Note: tcell.Button2=right, tcell.Button3=middle (not the usual ordering).
	var cb int
	switch activebtn {
	case tcell.Button1:
		cb = 0 // left
	case tcell.Button3:
		cb = 1 // middle
	case tcell.Button2:
		cb = 2 // right
	case tcell.WheelUp:
		cb = 64
	case tcell.WheelDown:
		cb = 65
	case tcell.WheelLeft:
		cb = 66
	case tcell.WheelRight:
		cb = 67
	case tcell.ButtonNone:
		cb = 35 // pure motion: base code 3 + 32
	default:
		return nil
	}

	if mod&tcell.ModShift != 0 {
		cb |= 4
	}
	if mod&tcell.ModAlt != 0 {
		cb |= 8
	}
	if mod&tcell.ModCtrl != 0 {
		cb |= 16
	}

	if sgr {
		final := 'M'
		if release {
			final = 'm'
		}
		return []byte(fmt.Sprintf("\x1b[<%d;%d;%d%c", cb, x, y, final))
	}

	// X10/normal encoding - coordinates capped at 223 (byte limit).
	if x > 223 || y > 223 {
		return nil
	}
	if release {
		cb = 3
	}
	return []byte{'\x1b', '[', 'M', byte(cb + 32), byte(x + 32), byte(y + 32)}
}

// sendFocusIn sends ESC[I (focus gained) to p if it has DECSET 1004 enabled
// (vt10x.ModeFocus).
func sendFocusIn(p *Pane) {
	if p == nil {
		return
	}
	p.mu.Lock()
	wants := p.term.Mode()&vt10x.ModeFocus != 0
	p.mu.Unlock()
	if wants {
		L.Debug("sendFocusIn", "pane", p.id)
		p.writeInput([]byte("\x1b[I"))
	}
}

// sendFocusOut sends ESC[O (focus lost) to p if it has DECSET 1004 enabled.
func sendFocusOut(p *Pane) {
	if p == nil {
		return
	}
	p.mu.Lock()
	wants := p.term.Mode()&vt10x.ModeFocus != 0
	p.mu.Unlock()
	if wants {
		L.Debug("sendFocusOut", "pane", p.id)
		p.writeInput([]byte("\x1b[O"))
	}
}

// ---------------------------------------------------------------------------
// Word selection (double-click)
// ---------------------------------------------------------------------------

// selectWord selects the word that contains vPos in pane p.
// Word characters: letters, digits, underscore, hyphen, dot, slash, tilde,
// at-sign, plus, colon - covers shell identifiers, file paths, and URLs.
func selectWord(p *Pane, vPos selPos) {
	p.mu.Lock()
	defer p.mu.Unlock()

	cols, rows := p.term.Size()
	sbCount := p.sb.count

	// Fetch the row cells at vPos.
	var cells []vt10x.Glyph
	if vPos.row < sbCount {
		cells = p.sb.get(vPos.row)
	} else if tr := vPos.row - sbCount; tr >= 0 && tr < rows {
		cells = captureRow(p.term, tr, cols)
	}
	if cells == nil {
		L.Debug("selectWord: no cells at position", "pane", p.id, "vrow", vPos.row, "vcol", vPos.col)
		return
	}

	// If clicked on whitespace, clear selection.
	if vPos.col >= len(cells) || !isWordChar(cells[vPos.col].Char) {
		L.Debug("selectWord: whitespace, clearing selection", "pane", p.id)
		p.selActive = false
		return
	}

	// Expand left.
	start := vPos.col
	for start > 0 && isWordChar(cells[start-1].Char) {
		start--
	}
	// Expand right.
	end := vPos.col
	for end+1 < len(cells) && isWordChar(cells[end+1].Char) {
		end++
	}

	L.Debug("selectWord: selected", "pane", p.id, "row", vPos.row, "start_col", start, "end_col", end)
	p.selActive = true
	p.selAnchor = selPos{row: vPos.row, col: start}
	p.selCursor = selPos{row: vPos.row, col: end}
}

// isWordChar reports whether r is part of a "word" for selection purposes.
func isWordChar(r rune) bool {
	if r == 0 || r == ' ' {
		return false
	}
	if unicode.IsLetter(r) || unicode.IsDigit(r) {
		return true
	}
	switch r {
	case '_', '-', '.', '/', '~', '@', '+', ':', '%', '=':
		return true
	}
	return false
}

// startDragEdgeScroll starts (or restarts) a goroutine that scrolls sel in the
// given direction while the cursor stays at the pane edge during a drag-select.
// scrollUp=true scrolls toward the past; false scrolls toward the present.
// If a goroutine is already running in the same direction nothing changes;
// if the direction changed the old goroutine is stopped first.
func (app *App) startDragEdgeScroll(sel *Pane, scrollUp bool) {
	// If already scrolling in the same direction, leave it running.
	if app.dragEdgeStop != nil {
		return
	}
	stop := make(chan struct{})
	app.dragEdgeStop = stop
	go func() {
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if scrollUp {
					sel.scrollUp(1)
				} else {
					sel.scrollDown(1)
				}
				// Extend selCursor to the visible edge after scrolling.
				sel.mu.Lock()
				if sel.selActive {
					cols, _ := sel.term.Size()
					sbCount := sel.sb.count
					sbOff := sel.sbOff
					if scrollUp {
						// Extend to the new top-left of the view.
						vRow := sbCount - sbOff
						sel.selCursor = selPos{row: vRow, col: 0}
					} else {
						// Extend to the new bottom-right of the view.
						vRow := (sbCount - sbOff) + sel.h - 1
						sel.selCursor = selPos{row: vRow, col: cols - 1}
					}
				}
				sel.mu.Unlock()
				app.triggerRedraw()
			}
		}
	}()
}

// stopDragEdgeScroll stops the edge-auto-scroll goroutine if one is running.
func (app *App) stopDragEdgeScroll() {
	if app.dragEdgeStop != nil {
		close(app.dragEdgeStop)
		app.dragEdgeStop = nil
	}
}
