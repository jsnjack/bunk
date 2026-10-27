package main

// Tab names. The user renames a tab by double-clicking it in the rail or
// the bar, through "Rename tab…" in the options menu, or with the
// rename_tab action, which has no default key. The name takes the place of
// the agent or process name in the rail, the bar and the switcher; the
// dot, the state, repo ⎇ branch, the location and the notice stay, and the
// tooltip keeps the agent. An empty name gives the automatic one back.
//
// The double-click is watched on the tab list, as tab dragging is
// (tabdrag.go), so it outlives the tab items. The first click selects the
// tab, which rebuilds them, and the second press can come before the new
// items are laid out: the tab is the one under the first press.

import (
	"log/slog"
	"strings"
	"unicode"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// tabNameLimit caps a tab's name, in characters.
const tabNameLimit = 64

// renameDialog is the open rename dialog.
type renameDialog struct {
	dlg   *adw.AlertDialog
	entry *gtk.Entry
	t     *Tab
}

// watchTabRename opens the rename dialog on a double-click on a tab item
// in a.tabsBox.
func (a *App) watchTabRename() {
	box := a.tabsBox
	g := gtk.NewGestureClick()
	g.SetPropagationPhase(gtk.PhaseCapture) // before the tab's button takes the press
	var first *Tab                          // the tab under the first press
	g.ConnectPressed(func(n int, x, y float64) {
		switch n {
		case 1:
			first = nil
			if i := a.tabItemAt(box, x, y); i >= 0 && i < len(a.tabs) {
				first = a.tabs[i]
			}
		case 2:
			if t := first; t != nil {
				first = nil
				// After the press: the button still waits for its release.
				coreglib.IdleAdd(func() { a.renameTab(t) })
			}
		}
	})
	box.AddController(g)
}

// renameTab asks for t's name. The entry shows the name the tab has, and
// the automatic name when it has none.
func (a *App) renameTab(t *Tab) {
	if a.rename != nil || a.finished || a.tabIndex(t) < 0 {
		return
	}
	auto := ""
	for _, in := range a.tabInfos() {
		if in.t == t {
			auto = in.agent
		}
	}
	d := adw.NewAlertDialog("Rename tab", "Leave the name empty to show the agent or program again.")
	e := gtk.NewEntry()
	e.SetText(t.name)
	e.SetPlaceholderText(auto)
	e.SetMaxLength(tabNameLimit)
	e.SetActivatesDefault(true)
	d.SetExtraChild(e)
	d.AddResponse("cancel", "_Cancel")
	d.AddResponse("rename", "_Rename")
	d.SetResponseAppearance("rename", adw.ResponseSuggested)
	d.SetDefaultResponse("rename")
	d.SetCloseResponse("cancel")
	d.ConnectResponse(func(r string) {
		a.rename = nil
		if r == "rename" {
			a.setTabName(t, e.Text())
		}
		if p := a.activePane(); p != nil {
			p.widget.GrabFocus()
		}
	})
	a.rename = &renameDialog{dlg: d, entry: e, t: t}
	d.Present(a.win)
	e.GrabFocus()
}

// setTabName gives t the name name, cleaned; an empty one gives the
// automatic name back.
func (a *App) setTabName(t *Tab, name string) {
	if a.tabIndex(t) < 0 {
		return
	}
	name = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, name))
	if r := []rune(name); len(r) > tabNameLimit {
		name = strings.TrimSpace(string(r[:tabNameLimit]))
	}
	if name == t.name {
		return
	}
	slog.Debug("tab renamed", "tab", t.id, "name", name)
	t.name = name
	a.refreshChrome()
}
