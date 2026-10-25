package main

import (
	"math"
	"os"
	"unicode"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/rivo/uniseg"

	"bunk/internal/input"
	"bunk/internal/session"
	"bunk/internal/vt10x"
)

// attachInput installs the pane's keyboard, input method, focus and pointer
// controllers. Key events go through the input method first: plain text
// arrives as "commit", everything else (Ctrl/Alt combinations, special keys,
// shortcuts) as "key-pressed".
func (p *Pane) attachInput() {
	w := p.widget
	p.im = gtk.NewIMMulticontext()
	p.im.SetClientWidget(w)
	p.imPtr = nativePtr(p.im)
	p.im.ConnectCommit(p.imCommit)
	p.im.ConnectPreeditChanged(func() {
		text, _, cursor := p.im.PreeditString()
		p.setPreedit(text, cursor)
	})
	p.im.ConnectPreeditEnd(func() { p.setPreedit("", 0) })

	keys := gtk.NewEventControllerKey()
	keys.SetIMContext(p.im)
	keys.ConnectKeyPressed(func(keyval, _ uint, state gdk.ModifierType) bool {
		return p.app.onKey(p, keyval, state)
	})
	w.AddController(keys)

	focus := gtk.NewEventControllerFocus()
	focus.ConnectEnter(func() {
		p.im.FocusIn()
		p.app.onPaneFocus(p)
	})
	focus.ConnectLeave(func() { p.im.FocusOut() })
	w.AddController(focus)

	click := gtk.NewGestureClick()
	click.SetButton(0) // all buttons
	click.ConnectPressed(func(n int, x, y float64) {
		p.mousePress(click.CurrentButton(), n, x, y, click.CurrentEventState())
	})
	click.ConnectReleased(func(_ int, x, y float64) {
		p.mouseRelease(x, y, click.CurrentEventState())
	})
	w.AddController(click)

	motion := gtk.NewEventControllerMotion()
	motion.ConnectMotion(func(x, y float64) { p.mouseMotion(x, y, motion.CurrentEventState()) })
	w.AddController(motion)

	scroll := gtk.NewEventControllerScroll(gtk.EventControllerScrollVertical | gtk.EventControllerScrollDiscrete)
	scroll.ConnectScroll(func(_, dy float64) bool { return p.wheel(dy, scroll.CurrentEventState()) })
	w.AddController(scroll)
}

// onKey handles a key press that the input method did not consume: window
// shortcuts first, then terminal input. kittyFlags and the modes are read
// under the session lock.
func (a *App) onKey(p *Pane, keyval uint, state gdk.ModifierType) bool {
	ctrl, shift := state&gdk.ControlMask != 0, state&gdk.ShiftMask != 0
	switch {
	case ctrl && shift && (keyval == gdk.KEY_C || keyval == gdk.KEY_c):
		a.copySelection(p)
		return true
	case ctrl && shift && (keyval == gdk.KEY_V || keyval == gdk.KEY_v):
		a.paste(p, false)
		return true
	case shift && !ctrl && keyval == gdk.KEY_Page_Up:
		p.scrollBy(max(1, p.tv.rows-1))
		return true
	case shift && !ctrl && keyval == gdk.KEY_Page_Down:
		p.scrollBy(-max(1, p.tv.rows-1))
		return true
	}
	ev, ok := keyEvent(keyval, state)
	if !ok {
		return false
	}
	p.s.Lock()
	kitty := 0
	if n := len(p.s.KittyStack); n > 0 {
		kitty = p.s.KittyStack[n-1]
	}
	modes := session.InputModes(kitty, p.s.Term.Mode())
	p.s.Unlock()
	var data []byte
	if b, ok := ctrlByte(ev); ok && kitty&1 == 0 {
		data = []byte{b}
	} else {
		data = input.EncodeKey(ev, modes)
	}
	if len(data) == 0 {
		return false
	}
	p.acknowledge()
	p.snapToLive()
	p.app.noteInjected()
	p.write(data)
	return true
}

// installShortcuts adds the window's shortcuts. They run in the capture
// phase, before the focused pane or its input method sees the key, so they
// work wherever the focus is. Keys that only make sense in some states (Alt+
// arrows and F12 with a split, Ctrl+B with the rail) reach the terminal
// otherwise.
func (a *App) installShortcuts() {
	keys := gtk.NewEventControllerKey()
	keys.SetPropagationPhase(gtk.PhaseCapture)
	keys.ConnectKeyPressed(func(keyval, _ uint, state gdk.ModifierType) bool {
		return a.onShortcut(keyval, state)
	})
	a.win.AddController(keys)
}

func (a *App) onShortcut(keyval uint, state gdk.ModifierType) bool {
	state &= gdk.ControlMask | gdk.ShiftMask | gdk.AltMask | gdk.SuperMask
	ctrl, shift, alt := state&gdk.ControlMask != 0, state&gdk.ShiftMask != 0, state&gdk.AltMask != 0
	if state&gdk.SuperMask != 0 {
		return false
	}
	t, p := a.active, a.activePane()
	split := t != nil && t.root != nil && t.root.pane == nil
	run := func(f func()) bool {
		if a.switcher != nil {
			a.closeSwitcher()
		}
		f()
		return true
	}
	switch {
	case ctrl && !shift && !alt && keyval == gdk.KEY_space:
		a.openSwitcher() // toggles
		return true
	case ctrl && !shift && !alt && (keyval == gdk.KEY_j || keyval == gdk.KEY_J):
		return run(a.jumpNeedingYou)
	case ctrl && !shift && !alt && keyval >= gdk.KEY_1 && keyval <= gdk.KEY_9:
		if i := int(keyval - gdk.KEY_1); i < len(a.tabs) {
			return run(func() { a.selectTab(a.tabs[i]) })
		}
		return true
	case ctrl && !shift && !alt && (keyval == gdk.KEY_b || keyval == gdk.KEY_B) && a.layout == "rail":
		return run(func() { a.setLayout("rail", !a.railOpen) })
	case ctrl && !alt && (keyval == gdk.KEY_equal || keyval == gdk.KEY_plus || keyval == gdk.KEY_KP_Add):
		return run(func() { a.zoom(+1) })
	case ctrl && !shift && !alt && (keyval == gdk.KEY_minus || keyval == gdk.KEY_KP_Subtract):
		// Ctrl+Shift+- (Ctrl+_, readline undo) still reaches the terminal.
		return run(func() { a.zoom(-1) })
	case ctrl && !shift && !alt && (keyval == gdk.KEY_0 || keyval == gdk.KEY_KP_0):
		return run(func() { a.zoom(0) })
	case ctrl && !shift && !alt && (keyval == gdk.KEY_q || keyval == gdk.KEY_Q):
		return run(func() { a.finish("quit") })
	// Ctrl+F (Find in the menu) reaches the terminal until Find exists:
	// shells and vim use it.
	case ctrl && !shift && !alt && keyval == gdk.KEY_comma:
		return run(func() { a.menuButtons["Preferences"].Emit("clicked") })
	case ctrl && !alt && keyval == gdk.KEY_question:
		return run(func() { a.menuButtons["Keyboard shortcuts"].Emit("clicked") })
	case ctrl && shift && (keyval == gdk.KEY_N || keyval == gdk.KEY_n):
		return run(func() { a.toast("New window is not built yet") })
	case ctrl && shift && (keyval == gdk.KEY_T || keyval == gdk.KEY_t):
		return run(func() { a.newTab("", a.cwdForNew()) })
	case ctrl && shift && (keyval == gdk.KEY_W || keyval == gdk.KEY_w):
		return run(func() { a.closeTab(t) })
	case ctrl && !shift && keyval == gdk.KEY_Page_Up, ctrl && keyval == gdk.KEY_ISO_Left_Tab:
		return run(func() { a.selectOffset(-1) })
	case ctrl && !shift && (keyval == gdk.KEY_Page_Down || keyval == gdk.KEY_Tab):
		return run(func() { a.selectOffset(+1) })
	case a.switcher != nil:
		return false // the switcher's entry gets the rest
	case !ctrl && !shift && keyval == gdk.KEY_F1 && p != nil:
		a.splitActive(alt)
		return true
	case !ctrl && !shift && !alt && keyval == gdk.KEY_F12 && split:
		t.toggleZoom(p)
		return true
	case alt && !ctrl && !shift && split && t.zoom == nil:
		switch keyval {
		case gdk.KEY_Left:
			t.moveFocus(-1, 0)
		case gdk.KEY_Right:
			t.moveFocus(1, 0)
		case gdk.KEY_Up:
			t.moveFocus(0, -1)
		case gdk.KEY_Down:
			t.moveFocus(0, 1)
		default:
			return false
		}
		return true
	}
	return false
}

// splitActive splits the focused pane (F1). With sameContainer (Alt+F1) the
// new pane runs $SHELL inside the container the focused pane is in.
func (a *App) splitActive(sameContainer bool) {
	p := a.activePane()
	if p == nil {
		return
	}
	var args []string
	if sameContainer {
		p.s.Lock()
		ct, id := p.s.ContainerType, p.s.ContainerID
		p.s.Unlock()
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		args = session.ContainerSpawnArgs(id, ct, shell)
		switch {
		case ct == "":
			a.toast("This pane is not in a container")
			return
		case args == nil && ct != "lxd":
			a.toast("Cannot open a shell in a " + ct + " container")
			return
		}
	}
	a.split(p, args)
}

// imCommit sends text the input method composed (plain typing, compose and
// dead keys, IME candidates).
func (p *Pane) imCommit(text string) {
	if text == "" {
		return
	}
	p.acknowledge()
	p.snapToLive()
	p.app.noteInjected()
	p.write([]byte(text))
}

// noteInjected records how long a key injected by a test took to reach the
// PTY write (main thread).
func (a *App) noteInjected() {
	if a.injectAt != 0 {
		a.st.injectWrite.Add(float64(monoNs()-a.injectAt) / 1e3)
		a.injectAt = 0
	}
}

// setPreedit shows IME composition text at the cursor. cursor counts
// characters, as GTK reports it.
func (p *Pane) setPreedit(text string, cursor int) {
	tv := p.tv
	tv.preedit = tv.preedit[:0]
	for _, r := range text {
		tv.preedit = append(tv.preedit, preeditCell{r: r, w: max(1, uniseg.StringWidth(string(r)))})
	}
	tv.preeditCursor = max(0, min(cursor, len(tv.preedit)))
	tv.preeditDirty = true
	p.queueRender()
}

func modsFrom(state gdk.ModifierType) input.Mod {
	var m input.Mod
	if state&gdk.ShiftMask != 0 {
		m |= input.ModShift
	}
	if state&gdk.ControlMask != 0 {
		m |= input.ModCtrl
	}
	if state&gdk.AltMask != 0 {
		m |= input.ModAlt
	}
	return m
}

func gdkButton(b uint) input.Button {
	switch b {
	case 1:
		return input.ButtonLeft
	case 2:
		return input.ButtonMiddle
	case 3:
		return input.ButtonRight
	}
	return input.ButtonOther
}

// cellAt maps widget coordinates to a grid cell, clamped to the grid.
func (p *Pane) cellAt(x, y float64) (col, row int) {
	g := viewGeomC(p.view)
	if g.scale <= 0 || p.atlas == nil {
		return 0, 0
	}
	col = int(math.Floor((x - g.bx) * g.scale / float64(p.atlas.CellW)))
	row = int(math.Floor((y - g.by) * g.scale / float64(p.atlas.CellH)))
	return max(0, min(col, p.tv.cols-1)), max(0, min(row, p.tv.rows-1))
}

// virtual converts a visible cell to selection coordinates (scrollback rows
// first, then live rows). Must be called with the session locked.
func (p *Pane) virtualLocked(col, row int) session.SelPos {
	return session.SelPos{Row: p.s.Scrollback.Count - p.s.ScrollOffset + row, Col: col}
}

func (p *Pane) mouseMode() vt10x.ModeFlag {
	p.s.Lock()
	defer p.s.Unlock()
	return p.s.Term.Mode()
}

// report sends a mouse event to the application in the encoding it asked for.
func (p *Pane) report(btn, prev input.Button, mods input.Mod, col, row int, mode vt10x.ModeFlag) {
	p.s.WriteInput(input.EncodeMouse(btn, prev, mods, col+1, row+1, mode&vt10x.ModeMouseSgr != 0))
}

// mousePress: with an application mouse mode (and no Shift) the press is
// reported; otherwise the left button starts a selection (double click: a
// word) and the middle button pastes the primary selection.
func (p *Pane) mousePress(button uint, n int, x, y float64, state gdk.ModifierType) {
	p.widget.GrabFocus()
	col, row := p.cellAt(x, y)
	p.lastX, p.lastY = x, y
	mode := p.mouseMode()
	if mode&vt10x.ModeMouseMask != 0 && state&gdk.ShiftMask == 0 {
		p.setSelection(false, session.SelPos{}, session.SelPos{})
		btn := gdkButton(button)
		p.mouseBtn, p.mouseCell = btn, [2]int{col, row}
		p.report(btn, input.ButtonNone, modsFrom(state), col, row, mode)
		return
	}
	switch button {
	case 1:
		p.s.Lock()
		v := p.virtualLocked(col, row)
		p.s.Unlock()
		if n >= 2 {
			p.selectWord(v)
			p.app.copyPrimary(p)
			p.selecting = false
			return
		}
		p.setSelection(false, v, v)
		p.selecting = true
	case 2:
		p.app.paste(p, true)
	}
}

// mouseMotion extends a selection, or reports drags and motion when the
// application asked for them (1002: drags, 1003: all motion).
func (p *Pane) mouseMotion(x, y float64, state gdk.ModifierType) {
	p.lastX, p.lastY = x, y
	col, row := p.cellAt(x, y)
	if p.selecting {
		p.s.Lock()
		v := p.virtualLocked(col, row)
		changed := v != p.s.SelCursor || (!p.s.SelActive && v != p.s.SelAnchor)
		if changed {
			p.s.SelActive = true
			p.s.SelCursor = v
		}
		p.s.Unlock()
		if changed {
			p.queueRender()
		}
		return
	}
	mode := p.mouseMode()
	if mode&vt10x.ModeMouseMask == 0 || (p.mouseCell == [2]int{col, row}) {
		return
	}
	p.mouseCell = [2]int{col, row}
	switch {
	case p.mouseBtn != input.ButtonNone && mode&(vt10x.ModeMouseMotion|vt10x.ModeMouseMany) != 0:
		p.report(p.mouseBtn, p.mouseBtn, modsFrom(state), col, row, mode)
	case p.mouseBtn == input.ButtonNone && mode&vt10x.ModeMouseMany != 0:
		p.report(input.ButtonNone, input.ButtonNone, modsFrom(state), col, row, mode)
	}
}

// mouseRelease ends a selection (and sets the primary selection) or
// reports the release.
func (p *Pane) mouseRelease(x, y float64, state gdk.ModifierType) {
	if p.selecting {
		p.selecting = false
		p.s.Lock()
		active := p.s.SelActive
		p.s.Unlock()
		if active {
			p.app.copyPrimary(p)
		}
		return
	}
	if p.mouseBtn == input.ButtonNone {
		return
	}
	col, row := p.cellAt(x, y)
	if mode := p.mouseMode(); mode&vt10x.ModeMouseMask != 0 {
		p.report(input.ButtonNone, p.mouseBtn, modsFrom(state), col, row, mode)
	}
	p.mouseBtn = input.ButtonNone
}

// wheel reports wheel notches to an application in a mouse mode, otherwise
// scrolls the scrollback 3 lines per notch.
func (p *Pane) wheel(dy float64, state gdk.ModifierType) bool {
	n := int(math.Round(dy))
	if n == 0 {
		n = int(math.Copysign(1, dy))
	}
	mode := p.mouseMode()
	if mode&vt10x.ModeMouseMask != 0 && state&gdk.ShiftMask == 0 {
		btn := input.WheelDown
		if n < 0 {
			btn = input.WheelUp
		}
		col, row := p.cellAt(p.lastX, p.lastY)
		for i := 0; i < abs(n); i++ {
			p.report(btn, input.ButtonNone, modsFrom(state), col, row, mode)
		}
		return true
	}
	p.scrollBy(-3 * n)
	return true
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func (p *Pane) setSelection(active bool, anchor, cursor session.SelPos) {
	p.s.Lock()
	p.s.SelActive, p.s.SelAnchor, p.s.SelCursor = active, anchor, cursor
	p.s.Unlock()
	p.queueRender()
}

// selectWord selects the word around v, as bunk's TUI does on double click.
func (p *Pane) selectWord(v session.SelPos) {
	p.s.Lock()
	defer func() {
		p.s.Unlock()
		p.queueRender()
	}()
	cols, rows := p.s.Term.Size()
	var cells []vt10x.Glyph
	if count := p.s.Scrollback.Count; v.Row < count {
		cells = p.s.Scrollback.Get(v.Row)
	} else if r := v.Row - count; r >= 0 && r < rows {
		cells = session.CaptureRow(p.s.Term, r, cols)
	}
	if v.Col > 0 && v.Col < len(cells) && cells[v.Col].Width == -1 {
		v.Col--
	}
	if v.Col >= len(cells) || !isWordChar(cells[v.Col].Char) {
		p.s.SelActive = false
		return
	}
	start, end := v.Col, v.Col
	for start > 0 && (cells[start-1].Width == -1 || isWordChar(cells[start-1].Char)) {
		start--
	}
	for end+1 < len(cells) && (cells[end+1].Width == -1 || isWordChar(cells[end+1].Char)) {
		end++
	}
	p.s.SelActive = true
	p.s.SelAnchor = session.SelPos{Row: v.Row, Col: start}
	p.s.SelCursor = session.SelPos{Row: v.Row, Col: end}
}

// isWordChar matches bunk's TUI: letters, digits and path/URL punctuation.
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
