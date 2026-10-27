package main

// Quitting (action quit, the window's ×) asks first while any pane runs
// something other than an idle shell.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"

	"bunk/internal/session"
)

var shellNames = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true, "ksh": true, "mksh": true, "tcsh": true,
	"csh": true, "ash": true, "nu": true, "elvish": true, "xonsh": true, "pwsh": true,
}

// busy returns the pane's foreground process and whether it is something
// other than the pane's own shell waiting at its prompt.
func (p *Pane) busy() (string, bool) {
	if p.s == nil || p.s.Cmd == nil || p.s.Cmd.Process == nil || p.exited || p.removeOnce {
		return "", false
	}
	pid := p.s.Cmd.Process.Pid
	fg := session.TermFgPGID(pid)
	name := filepath.Base(p.s.Cmd.Path)
	if fg > 0 {
		if comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", fg)); err == nil {
			name = strings.TrimSpace(string(comm))
		}
	}
	return name, (fg > 0 && fg != pid) || !shellNames[name]
}

// busyTabs lists "Tab N: process" for every busy pane.
func (a *App) busyTabs() []string {
	var out []string
	for i, t := range a.tabs {
		for _, p := range t.panes() {
			if name, busy := p.busy(); busy {
				out = append(out, fmt.Sprintf("Tab %d: %s", i+1, name))
			}
		}
	}
	return out
}

// requestQuit quits at once when nothing runs, else asks.
func (a *App) requestQuit() {
	if a.quitDialog != nil {
		return
	}
	busy := a.busyTabs()
	if len(busy) == 0 {
		a.finish("quit")
		return
	}
	d := adw.NewAlertDialog("Quit bunk-gui?", "Still running:\n"+strings.Join(busy, "\n"))
	d.AddResponse("cancel", "_Cancel")
	d.AddResponse("quit", "_Quit")
	d.SetResponseAppearance("quit", adw.ResponseDestructive)
	d.SetDefaultResponse("cancel")
	d.SetCloseResponse("cancel")
	d.ConnectResponse(func(r string) {
		a.quitDialog, a.quitBody = nil, nil
		if r == "quit" {
			a.finish("quit")
			return
		}
		if p := a.activePane(); p != nil {
			p.widget.GrabFocus()
		}
	})
	a.quitDialog, a.quitBody = d, busy
	d.Present(a.win)
}

// onCloseRequest handles the window's ×: the same as quit.
func (a *App) onCloseRequest() bool {
	if a.finished {
		return false
	}
	a.requestQuit()
	return true
}
