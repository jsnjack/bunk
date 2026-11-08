package main

// Context menus. A right-click on a tab in the rail or the bar opens the
// tab's menu without selecting it: Rename…, Copy directory, Mark as seen,
// Mute notifications, Show documents, Close tab and Close other tabs. A
// right-click in a pane opens the pane's menu: the link under the pointer,
// copy and paste, find, split, zoom, passthrough, the pane's directory,
// Clear scrollback and Close pane. While the program in the pane reports
// the mouse, a plain right-click goes to the program and Shift+right-click
// opens the menu, as in VTE and kitty.
//
// The popover's parent is the root box, which lasts as long as the window:
// refreshChrome replaces the tab items under a menu opened on one. Items
// show the first key of their action, as the options menu does.
//
// Closing from a menu asks first while a pane of what closes runs
// something other than an idle shell, as quitting does; the close_tab key
// and the shell's exit close at once.

import (
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"bunk/internal/session"
)

// ctxItem is one row of a context menu.
type ctxItem struct {
	label, action string // action: the bound action whose first key the row shows
	run           func()
	check         bool // a check item; checked is its state
	checked       bool
	disabled      bool
}

// ctxMenu is the open context menu.
type ctxMenu struct {
	pop     *gtk.Popover
	kind    string // "tab" or "pane"
	tab     *Tab
	pane    *Pane // the pane's menu; nil for a tab's
	items   []ctxItem
	widgets map[string]*gtk.Widget      // each item's button or check button, by label
	checks  map[string]*gtk.CheckButton // the check items, by label
}

// ctxMenuReport is the open context menu in --report.
type ctxMenuReport struct {
	Kind  string          `json:"kind"` // tab or pane
	Tab   int             `json:"tab"`  // 1-based index of its tab
	Pane  int             `json:"pane"` // the pane's ID; -1 in a tab's menu
	Items []ctxItemReport `json:"items"`
}

type ctxItemReport struct {
	Label   string `json:"label"`
	Key     string `json:"key,omitempty"` // the key the row shows
	Enabled bool   `json:"enabled"`
	Check   bool   `json:"check,omitempty"`
	Checked bool   `json:"checked,omitempty"`
}

func (a *App) ctxMenuReport() *ctxMenuReport {
	m := a.ctxMenu
	if m == nil {
		return nil
	}
	r := &ctxMenuReport{Kind: m.kind, Tab: a.tabIndex(m.tab) + 1, Pane: -1, Items: []ctxItemReport{}}
	if m.pane != nil {
		r.Pane = m.pane.id
	}
	for _, it := range m.items {
		ir := ctxItemReport{Label: it.label, Key: a.firstKey(it.action), Enabled: !it.disabled, Check: it.check}
		if w := m.widgets[it.label]; w != nil {
			ir.Enabled = w.Sensitive()
		}
		if c := m.checks[it.label]; c != nil {
			ir.Checked = c.Active()
		}
		r.Items = append(r.Items, ir)
	}
	return r
}

// watchTabMenu opens a tab's menu on a right-click on its item in
// a.tabsBox. The gesture sits on the tab list, as the rename and drag
// gestures do (tabname.go, tabdrag.go).
func (a *App) watchTabMenu() {
	box := a.tabsBox
	g := gtk.NewGestureClick()
	g.SetButton(gdk.BUTTON_SECONDARY)
	g.SetPropagationPhase(gtk.PhaseCapture)
	g.ConnectPressed(func(n int, x, y float64) {
		i := a.tabItemAt(box, x, y)
		if n != 1 || i < 0 || i >= len(a.tabs) {
			return
		}
		g.SetState(gtk.EventSequenceClaimed)
		a.openTabMenu(a.tabs[i], box, x, y)
	})
	box.AddController(g)
}

// openTabMenu opens t's menu at x, y in from's coordinates.
func (a *App) openTabMenu(t *Tab, from gtk.Widgetter, x, y float64) {
	others := make([]*Tab, 0, len(a.tabs))
	for _, o := range a.tabs {
		if o != t {
			others = append(others, o)
		}
	}
	items := [][]ctxItem{
		{
			{label: "Rename…", action: "rename_tab", run: func() { a.renameTab(t) }},
			{label: "Copy directory", run: func() { a.copyDir(t.menuPane()) }},
			{label: "Mark as seen", disabled: !a.hasUnseen(t), run: func() { a.markSeen(t) }},
			{label: "Mute notifications", check: true, checked: t.muted, run: func() { a.setMuted(t, !t.muted) }},
			{label: "Show documents", action: "documents", check: true, checked: t.panel != nil && t.panel.open,
				disabled: t.panel == nil || len(t.panel.docs) == 0, run: func() { a.toggleDocuments(t) }},
		},
		{
			{label: "Close tab", action: "close_tab", run: func() {
				a.confirmClose("Close the tab?", t.panes(), func() { a.closeTab(t) })
			}},
			{label: "Close other tabs", disabled: len(others) == 0, run: func() {
				var panes []*Pane
				for _, o := range others {
					panes = append(panes, o.panes()...)
				}
				a.confirmClose("Close the other tabs?", panes, func() {
					for _, o := range others {
						if a.tabIndex(t) >= 0 {
							a.closeTab(o)
						}
					}
					if a.tabIndex(t) >= 0 {
						a.selectTab(t)
					}
				})
			}},
		},
	}
	a.openCtxMenu(&ctxMenu{kind: "tab", tab: t}, items, from, x, y)
}

// openPaneMenu opens p's menu at x, y in the pane's coordinates.
func (a *App) openPaneMenu(p *Pane, x, y float64) {
	t := p.tab
	var items [][]ctxItem
	if hit, ok := p.linkAt(p.cellAt(x, y)); ok {
		u := hit.url
		items = append(items, []ctxItem{
			{label: "Open link", run: func() { openURL(a, u) }},
			{label: "Copy link address", run: func() { clipboard(false).SetText(u) }},
		})
	}
	p.s.Lock()
	kept := p.s.Scrollback.Count
	p.s.Unlock()
	zoom := "Zoom pane"
	if t.zoom == p {
		zoom = "Unzoom pane"
	}
	items = append(items,
		[]ctxItem{
			{label: "Copy", action: "copy", disabled: p.selectionText() == "", run: func() { a.copySelection(p, false) }},
			{label: "Paste", action: "paste", run: func() { a.paste(p, false) }},
			{label: "Select all", action: "select_all", run: func() { a.selectAll(p) }},
			{label: "Find…", action: "search", run: a.openFind},
		},
		[]ctxItem{
			{label: "Split pane", action: "split", run: func() { a.splitActive(false) }},
			{label: zoom, action: "zoom", disabled: t.root == nil || t.root.pane != nil, run: func() { t.toggleZoom(p) }},
			{label: "Send all keys to terminal", action: "passthrough", check: true, checked: p.passthrough,
				run: func() { a.setPassthrough(p, !p.passthrough) }},
		},
		[]ctxItem{
			{label: "Copy directory", run: func() { a.copyDir(p) }},
			{label: "Open directory", run: func() { a.openDir(p) }},
			{label: "Clear scrollback", disabled: kept == 0, run: p.clearScrollback},
		},
		[]ctxItem{
			{label: "Close pane", run: func() {
				a.confirmClose("Close the pane?", []*Pane{p}, func() { a.closePane(p) })
			}},
		},
	)
	a.openCtxMenu(&ctxMenu{kind: "pane", tab: t, pane: p}, items, p.widget, x, y)
}

// openCtxMenu shows m with the items in groups, pointing at x, y in
// from's coordinates.
func (a *App) openCtxMenu(m *ctxMenu, groups [][]ctxItem, from gtk.Widgetter, x, y float64) {
	a.closeCtxMenu()
	if a.finished || a.modalOpen() {
		return
	}
	if a.switcher != nil {
		a.closeSwitcher()
	}
	b, ok := gtk.BaseWidget(from).ComputeBounds(a.rootBox)
	if !ok {
		return
	}
	pop := gtk.NewPopover()
	pop.AddCSSClass("bunk-menu")
	pop.SetHasArrow(false)
	pop.SetPosition(gtk.PosBottom)
	pop.SetHAlign(gtk.AlignStart)
	m.pop, m.widgets, m.checks = pop, map[string]*gtk.Widget{}, map[string]*gtk.CheckButton{}
	box := gtk.NewBox(gtk.OrientationVertical, 0)
	box.SetSizeRequest(260, -1)
	for _, group := range groups {
		g := gtk.NewBox(gtk.OrientationVertical, 0)
		g.AddCSSClass("menugroup")
		for _, it := range group {
			m.items = append(m.items, it)
			row := gtk.NewBox(gtk.OrientationHorizontal, 12)
			l := gtk.NewLabel(it.label)
			l.SetHExpand(true)
			l.SetXAlign(0)
			row.Append(l)
			row.Append(label(a.firstKey(it.action), "keys"))
			run := it.run
			var w gtk.Widgetter
			if it.check {
				c := gtk.NewCheckButton()
				c.AddCSSClass("menucheck")
				c.SetChild(row)
				c.SetActive(it.checked)
				checked := it.checked
				c.ConnectToggled(func() {
					if c.Active() != checked {
						checked = c.Active()
						pop.Popdown()
						run()
					}
				})
				w = c
				m.checks[it.label] = c
			} else {
				bt := gtk.NewButton()
				bt.AddCSSClass("menuitem")
				bt.SetChild(row)
				bt.ConnectClicked(func() {
					pop.Popdown()
					run()
				})
				w = bt
			}
			gtk.BaseWidget(w).SetSensitive(!it.disabled)
			m.widgets[it.label] = gtk.BaseWidget(w)
			g.Append(w)
		}
		box.Append(g)
	}
	pop.SetChild(box)
	pop.SetParent(a.rootBox)
	rect := gdk.NewRectangle(int(float64(b.X())+x), int(float64(b.Y())+y), 1, 1)
	pop.SetPointingTo(&rect)
	pop.ConnectClosed(func() {
		if a.ctxMenu == m {
			a.ctxMenu = nil
		}
		// An item that opened a dialog keeps the focus there.
		if p := a.activePane(); p != nil && a.switcher == nil && !a.modalOpen() && a.find == nil {
			p.widget.GrabFocus()
		}
		// Unparent after the item's handler: it runs after Popdown.
		coreglib.IdleAdd(pop.Unparent)
	})
	a.ctxMenu = m
	slog.Debug("context menu", "kind", m.kind, "tab", a.tabIndex(m.tab)+1)
	pop.Popup()
}

func (a *App) closeCtxMenu() {
	if m := a.ctxMenu; m != nil {
		m.pop.Popdown()
		a.ctxMenu = nil
	}
}

// clickCtxItem runs the open context menu's item named label as a click
// would; the script's ctx-item step.
func (a *App) clickCtxItem(label string) error {
	m := a.ctxMenu
	if m == nil {
		return fmt.Errorf("no context menu")
	}
	w := m.widgets[label]
	if w == nil {
		return fmt.Errorf("no context menu item %q", label)
	}
	if !w.Sensitive() {
		return fmt.Errorf("context menu item %q is disabled", label)
	}
	if c := m.checks[label]; c != nil {
		c.SetActive(!c.Active())
		return nil
	}
	coreglib.BaseObject(w).Emit("clicked")
	return nil
}

// menuPane is the pane a tab's menu acts on: the focused one.
func (t *Tab) menuPane() *Pane {
	if t.focus != nil && !t.focus.removeOnce {
		return t.focus
	}
	return t.agentPane()
}

// copyDir puts p's directory on the clipboard.
func (a *App) copyDir(p *Pane) {
	if p == nil || p.s == nil {
		return
	}
	dir := p.s.Cwd()
	if dir == "" {
		a.toast("The pane's directory is not known")
		return
	}
	clipboard(false).SetText(dir)
	a.toast("Copied " + dir)
}

// openDir opens p's directory in the file manager.
func (a *App) openDir(p *Pane) {
	dir := p.s.Cwd()
	if dir == "" {
		a.toast("The pane's directory is not known")
		return
	}
	openURL(a, (&url.URL{Scheme: "file", Path: dir}).String())
}

// hasUnseen reports whether marking t as seen would change anything: an
// agent that is done or needs the user, a command's done or failed mark,
// or a notification that is up.
func (a *App) hasUnseen(t *Tab) bool {
	for _, p := range t.panes() {
		switch p.status().State {
		case session.AgentNeedsYou, session.AgentDone:
			return true
		}
		switch p.mark.State {
		case session.AgentDone, session.AgentFailed:
			return true
		}
		if a.alerts.tracker.Shown(p.id) {
			return true
		}
	}
	return false
}

// markSeen does for every pane of t what looking at it does: done and
// inferred needs-you clear (a reported needs-you stays, as the agent still
// waits), command marks clear and notifications are withdrawn.
func (a *App) markSeen(t *Tab) {
	if a.tabIndex(t) < 0 {
		return
	}
	for _, p := range t.panes() {
		p.acknowledge(session.AckSeen)
	}
	t.seen = true
	a.syncMarks()
}

// setMuted turns t's desktop notifications off or back on.
func (a *App) setMuted(t *Tab, on bool) {
	if a.tabIndex(t) < 0 || t.muted == on {
		return
	}
	t.muted = on
	slog.Debug("tab notifications", "tab", t.id, "muted", on)
	a.syncMarks()
}

// clearScrollback drops the pane's scrollback, as ED 3 does.
func (p *Pane) clearScrollback() {
	if p.removeOnce {
		return
	}
	p.s.Lock()
	p.s.OnScrollbackClear()
	p.s.Unlock()
	p.tv.invalidate()
	p.queueRender()
	p.syncBadges()
}

// confirmClose runs close at once when no pane of panes runs anything but
// an idle shell, and otherwise after the user confirms.
func (a *App) confirmClose(heading string, panes []*Pane, close func()) {
	var busy []string
	for _, p := range panes {
		if name, b := p.busy(); b {
			busy = append(busy, fmt.Sprintf("Tab %d: %s", a.tabIndex(p.tab)+1, name))
		}
	}
	if len(busy) == 0 {
		close()
		return
	}
	if a.closeDialog != nil || a.finished {
		return
	}
	d := adw.NewAlertDialog(heading, "Still running:\n"+strings.Join(busy, "\n"))
	d.AddResponse("cancel", "_Cancel")
	d.AddResponse("close", "C_lose")
	d.SetResponseAppearance("close", adw.ResponseDestructive)
	d.SetDefaultResponse("cancel")
	d.SetCloseResponse("cancel")
	d.ConnectResponse(func(r string) {
		a.closeDialog, a.closeBusy = nil, nil
		if r == "close" {
			close()
		}
		if p := a.activePane(); p != nil && !a.finished {
			p.widget.GrabFocus()
		}
	})
	a.closeDialog, a.closeBusy = d, busy
	d.Present(a.win)
}
