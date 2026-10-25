package main

import (
	"math"
	"os"
	"strings"
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
		if keyval == gdk.KEY_Control_L || keyval == gdk.KEY_Control_R {
			p.ctrlKey(true)
		}
		return p.app.onKey(p, keyval, state)
	})
	keys.ConnectKeyReleased(func(keyval, _ uint, state gdk.ModifierType) {
		if keyval == gdk.KEY_Control_L || keyval == gdk.KEY_Control_R {
			p.ctrlKey(false)
		}
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
	motion.ConnectEnter(func(x, y float64) {
		p.pointerIn = true
		p.lastX, p.lastY = x, y
		p.updateHover(x, y, motion.CurrentEventState())
	})
	motion.ConnectLeave(func() {
		p.pointerIn = false
		p.setHover(linkHit{})
	})
	w.AddController(motion)

	scroll := gtk.NewEventControllerScroll(gtk.EventControllerScrollVertical | gtk.EventControllerScrollDiscrete)
	scroll.ConnectScroll(func(_, dy float64) bool { return p.wheel(dy, scroll.CurrentEventState()) })
	w.AddController(scroll)
}

// onKey sends a key press that no window shortcut took and the input
// method did not consume to the terminal. kittyFlags and the modes are
// read under the session lock.
func (a *App) onKey(p *Pane, keyval uint, state gdk.ModifierType) bool {
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

// installShortcuts adds the window's key bindings ([keys] in config.toml).
// They run in the capture phase, before the focused pane or its input
// method sees the key, so they work wherever the focus is.
func (a *App) installShortcuts() {
	keys := gtk.NewEventControllerKey()
	keys.SetPropagationPhase(gtk.PhaseCapture)
	keys.ConnectKeyPressed(func(keyval, keycode uint, state gdk.ModifierType) bool {
		return a.onShortcut(keyval, keycode, state)
	})
	a.win.AddController(keys)
}

// onShortcut runs the action bound to a key press. It returns false, so
// the key goes on to the focused widget (usually the terminal), when no
// action is bound, when the focused pane sends all keys to the terminal
// (passthrough; only its own toggle still works), while a dialog is open,
// and when the action does not apply (zoom without a split). keycode 0
// means unknown.
func (a *App) onShortcut(keyval, keycode uint, state gdk.ModifierType) bool {
	if a.modalOpen() {
		return false
	}
	unshift := func() uint {
		if keycode == 0 {
			return 0
		}
		kv, _, _, _, ok := gdk.DisplayGetDefault().TranslateKey(keycode, state&^gdk.ShiftMask, 0)
		if !ok {
			return 0
		}
		return kv
	}
	name, ok := a.keys().match(keyval, state, unshift)
	if !ok {
		return false
	}
	if p := a.activePane(); p != nil && p.passthrough && name != "passthrough" && a.switcher == nil {
		return false
	}
	return a.runAction(name)
}

// keys is the key map in force.
func (a *App) keys() keymap { return a.settings.keymap }

// modalOpen reports a dialog that takes the keyboard.
func (a *App) modalOpen() bool {
	return a.about != nil || a.shortcutsWin != nil || a.prefsWin != nil || a.quitDialog != nil
}

// runAction runs a bound action and reports whether it applied.
func (a *App) runAction(name string) bool {
	t, p := a.active, a.activePane()
	split := t != nil && t.root != nil && t.root.pane == nil
	// Window actions close the switcher first; pane actions leave its keys
	// alone while it is open.
	global := func(f func()) bool {
		if a.switcher != nil {
			a.closeSwitcher()
		}
		f()
		return true
	}
	if n, ok := strings.CutPrefix(name, "select_tab_"); ok {
		i := int(n[0] - '1')
		if i < 0 || i >= len(a.tabs) {
			return true
		}
		return global(func() { a.selectTab(a.tabs[i]) })
	}
	switch name {
	case "search_next", "search_prev", "search_exit":
		// Only while the find bar has the keyboard: Enter and Esc go to the
		// terminal otherwise.
		return a.findFocused() && a.findAction(name)
	case "switcher":
		a.openSwitcher() // toggles
		return true
	case "next_needs_you":
		return global(a.jumpNeedingYou)
	case "new_tab":
		return global(func() { a.newTab("", a.cwdForNew()) })
	case "close_tab":
		return global(func() { a.closeTab(t) })
	case "new_window":
		return global(func() { a.toast("New window is not built yet") })
	case "next_tab":
		return global(func() { a.selectOffset(+1) })
	case "prev_tab":
		return global(func() { a.selectOffset(-1) })
	case "toggle_rail":
		if a.layout != "rail" {
			return false
		}
		return global(func() { a.setLayout("rail", !a.railOpen) })
	case "font_bigger":
		return global(func() { a.zoom(+1) })
	case "font_smaller":
		return global(func() { a.zoom(-1) })
	case "font_reset":
		return global(func() { a.zoom(0) })
	case "menu":
		return global(func() { a.optionsBtn.Popup() })
	case "preferences":
		return global(a.showPreferences)
	case "shortcuts":
		return global(a.showShortcuts)
	case "quit":
		return global(a.requestQuit)
	}
	if a.switcher != nil || p == nil {
		return false
	}
	switch name {
	case "passthrough":
		a.setPassthrough(p, !p.passthrough)
	case "split":
		a.splitActive(false)
	case "split_context":
		a.splitActive(true)
	case "zoom":
		if !split {
			return false
		}
		t.toggleZoom(p)
	case "nav_up", "nav_down", "nav_left", "nav_right":
		if !split || t.zoom != nil {
			return false
		}
		d := focusDirs[strings.TrimPrefix(name, "nav_")]
		t.moveFocus(d[0], d[1])
	case "scroll_up":
		p.scrollBy(max(1, p.tv.rows-1))
	case "scroll_down":
		p.scrollBy(-max(1, p.tv.rows-1))
	case "search":
		a.openFind()
	case "copy":
		a.copySelection(p)
	case "paste":
		a.paste(p, false)
	default:
		return false
	}
	return true
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
	if err := p.s.WriteInput(input.EncodeMouse(btn, prev, mods, col+1, row+1, mode&vt10x.ModeMouseSgr != 0)); err != nil {
		p.inputRefused(err)
	}
}

// mousePress: with an application mouse mode (and no Shift) the press is
// reported; otherwise the left button starts a selection (double click: a
// word) and the middle button pastes the primary selection.
func (p *Pane) mousePress(button uint, n int, x, y float64, state gdk.ModifierType) {
	p.widget.GrabFocus()
	col, row := p.cellAt(x, y)
	p.lastX, p.lastY = x, y
	mode := p.mouseMode()
	if p.clickLink(button, x, y, state, mode&vt10x.ModeMouseMask != 0) {
		return
	}
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
	p.pointerIn = true
	p.updateHover(x, y, state)
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
