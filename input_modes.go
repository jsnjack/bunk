package main

import (
	"bunk/internal/vt10x"
)

func (app *App) handleFocus(focused bool) {
	app.mu.Lock()
	defer app.mu.Unlock()
	if p := app.active; p != nil {
		p.mu.Lock()
		enabled := !p.dead && p.term.Mode()&vt10x.ModeFocus != 0
		p.mu.Unlock()
		if enabled {
			if focused {
				p.writeInput([]byte("\x1b[I"))
			} else {
				p.writeInput([]byte("\x1b[O"))
			}
		}
	}
}
