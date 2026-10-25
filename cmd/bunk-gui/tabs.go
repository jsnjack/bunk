package main

import (
	"fmt"
	"os"
	"unsafe"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"bunk/internal/input"
	"bunk/internal/session"
)

const defaultTitle = "bunk-gui"

// setupTabs builds the window content: an AdwTabBar that only appears with
// two or more tabs, above an AdwTabView whose pages are TermViews. Only the
// selected page is mapped, so hidden tabs keep parsing output but never
// render.
func (a *App) setupTabs() gtk.Widgetter {
	a.tabs = adw.NewTabView()
	// The view's own shortcuts would take Alt+1..9 and more from
	// applications; onKey handles the tab keys bunk-gui uses.
	a.tabs.SetShortcuts(adw.TabViewShortcutNone)
	a.tabs.ConnectClosePage(func(page *adw.TabPage) bool {
		if p := a.paneOf(page); p != nil {
			a.removePane(p)
		}
		a.tabs.ClosePageFinish(page, true)
		if a.tabs.NPages() == 0 {
			a.finish("last tab closed")
		}
		return true
	})
	a.tabs.NotifyProperty("selected-page", a.onSelect)

	bar := adw.NewTabBar()
	bar.SetView(a.tabs)
	bar.SetAutohide(true)

	box := gtk.NewBox(gtk.OrientationVertical, 0)
	box.Append(bar)
	box.Append(a.tabs)
	return box
}

// newTab starts a session running cmd (sh -c), or $SHELL when cmd is "",
// in dir, and shows it in a new selected tab.
func (a *App) newTab(cmd, dir string) *Pane {
	p := &Pane{id: len(a.panes), app: a, tv: newTermView()}
	a.panes = append(a.panes, p)
	ptr, cv := viewNewC(p.id)
	p.view = cv
	p.widget = wrapWidget(ptr)
	atlas, err := a.atlasFor(a.monScale)
	if err != nil {
		a.fatal("%v", err)
		return nil
	}
	p.atlas = atlas
	p.attachInput()

	cols, rows := a.initCols, a.initRows
	if cur := a.activePane(); cur != nil && cur.cols > 1 {
		cols, rows = cur.cols, cur.rows // a new tab starts at the window's size
	}
	var args []string
	if cmd != "" {
		args = []string{"/bin/sh", "-c", cmd}
	}
	hex := func(rgb uint32) string { return session.XParseColor(fmt.Sprintf("%06x", rgb)) }
	s, err := session.Start(session.Options{
		ID: p.id, Cols: cols, Rows: rows, Scrollback: session.DefaultScrollbackLines, Dir: dir, Args: args,
		ThemeFG: hex(theme.fg), ThemeBG: hex(theme.bg), ThemeCursor: hex(theme.fg),
		CellAspect: []float64{float64(atlas.CellH) / float64(atlas.CellW)},
		Backspace:  input.KeyBackspace,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "bunk-gui: start session: %v\n", err)
		a.panes[p.id] = nil
		if a.tabs.NPages() == 0 {
			a.fatal("no session")
		}
		return nil
	}
	p.s, p.cols, p.rows = s, cols, rows
	s.Run(session.Hooks{
		Applied: p.onApplied, Output: p.onOutput, OSC: p.onOSC,
		Redraw: p.wake, Exited: p.onExited, Closed: p.onClosed,
	}, a.done)

	p.title = defaultTitle
	p.page = a.tabs.Append(p.widget)
	p.page.SetTitle(p.title)
	a.tabs.SetSelectedPage(p.page)
	return p
}

func wrapWidget(ptr unsafe.Pointer) *gtk.Widget {
	obj := coreglib.Take(ptr)
	w := obj.WalkCast(func(o coreglib.Objector) bool {
		_, ok := o.(gtk.Widgetter)
		return ok
	})
	return gtk.BaseWidget(w.(gtk.Widgetter))
}

// activePane is the pane of the selected tab.
func (a *App) activePane() *Pane {
	if a.tabs == nil {
		return nil
	}
	page := a.tabs.SelectedPage()
	if page == nil {
		return nil
	}
	return a.paneOf(page)
}

func (a *App) paneOf(page *adw.TabPage) *Pane {
	want := coreglib.InternObject(page).Native()
	for _, p := range a.panes {
		if p != nil && !p.removeOnce && p.page != nil && coreglib.InternObject(p.page).Native() == want {
			return p
		}
	}
	return nil
}

func (a *App) onSelect() {
	p := a.activePane()
	if p == nil {
		return
	}
	p.widget.GrabFocus()
	a.win.SetTitle(p.title)
	p.queueRender()
}

// closeTab closes p's tab (Ctrl+Shift+W); close-page ends the session.
func (a *App) closeTab(p *Pane) {
	if p.page != nil {
		a.tabs.ClosePage(p.page)
	}
}

// removePane ends p's session (SIGHUP). The TabView destroys the widget,
// whose unrealize frees the GL objects and drops the pane from App.panes.
func (a *App) removePane(p *Pane) {
	if p.removeOnce {
		return
	}
	p.removeOnce = true
	p.s.Close()
}

// paneDone runs when p's child is finished (Hooks.Closed, or Exited without
// Closed). A tab closes at once; the last tab draws one more frame, so the
// final output is on screen (and in -dump), then the app quits.
func (a *App) paneDone(p *Pane) {
	if p.removeOnce || a.exitArmed {
		return
	}
	if a.tabs.NPages() > 1 {
		a.closeTab(p)
		return
	}
	a.exitArmed = true
	p.queueRender()
	coreglib.TimeoutAdd(1000, func() bool { a.finish("child exited (no frame)"); return false })
}

// setTitle shows an OSC 0/2 title on p's tab (and the window when p is
// selected).
func (a *App) setTitle(p *Pane, title string) {
	if p.removeOnce {
		return
	}
	if title == "" {
		title = defaultTitle
	}
	p.title = title
	p.page.SetTitle(title)
	if a.activePane() == p {
		a.win.SetTitle(title)
	}
}
