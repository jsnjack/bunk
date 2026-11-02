package main

import (
	"log/slog"

	"bunk/internal/vt10x"
)

// syncFocusReports tells programs that asked for focus events (DECSET
// 1004) when they gain or lose the focus, as bunk's TUI does: ESC [ I to
// the pane the user now works in, ESC [ O to the one left. A pane has the
// focus while it is the focused pane of the selected tab in the active
// window. It runs with every chrome refresh, which follows focus, tab
// and window activation changes, and writes only on a change.
func (a *App) syncFocusReports() {
	var want *Pane
	if a.win != nil && a.win.IsActive() {
		want = a.activePane()
	}
	if want != nil && want.removeOnce {
		want = nil
	}
	old := a.focusIn
	if want == old {
		return
	}
	a.focusIn = want
	if old != nil && !old.removeOnce {
		old.focusReport(false)
	}
	if want != nil {
		want.focusReport(true)
	}
}

// focusReport sends a focus event when the program asked for them.
func (p *Pane) focusReport(in bool) {
	p.s.Lock()
	wants := !p.s.Dead && p.s.Term.Mode()&vt10x.ModeFocus != 0
	p.s.Unlock()
	if !wants {
		return
	}
	report := "\x1b[O"
	if in {
		report = "\x1b[I"
	}
	if err := p.s.WriteInput([]byte(report)); err != nil {
		slog.Debug("focus report refused", "pane", p.id, "err", err)
	}
}
