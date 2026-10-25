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
			if focused {
				p.WriteInput([]byte("\x1b[I"))
			} else {
				p.WriteInput([]byte("\x1b[O"))
			}
		}
	}
}
