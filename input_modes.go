package main

import (
	"bunk/internal/vt10x"
)

func (app *App) handleFocus(focused bool) {
	app.mu.Lock()
	defer app.mu.Unlock()
	if p := app.active; p != nil {
		p.Lock()
		enabled := !p.Dead && p.Term.Mode()&vt10x.ModeFocus != 0
		p.Unlock()
		if enabled {
			report := "\x1b[O"
			if focused {
				report = "\x1b[I"
			}
			// Under app.mu: log a refusal instead of flashing it.
			if err := p.WriteInput([]byte(report)); err != nil {
				L.Debug("handleFocus: focus report refused", "pane", p.ID, "err", err)
			}
		}
	}
}
