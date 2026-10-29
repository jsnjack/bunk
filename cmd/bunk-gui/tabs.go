package main

import (
	"fmt"
	"log/slog"
	"math"
	"os"
	"unsafe"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"bunk/internal/input"
	"bunk/internal/notify"
	"bunk/internal/session"
)

const defaultTitle = "bunk-gui"

// Tab is one tab: a BSP tree of panes shown as nested GtkPaned widgets
// inside holder, a child of the window's GtkStack. Only the visible stack
// child is mapped, so panes of hidden tabs keep parsing output but never
// render.
type Tab struct {
	app    *App
	holder *gtk.Box
	root   *node
	focus  *Pane // the pane that has (or last had) keyboard focus
	zoom   *Pane // the zoomed pane, nil when none
	// id names the tab in desktop notifications: the ID of the pane it
	// opened with, which no other tab can have.
	id int
	// name is the user's name for the tab (tabname.go); "" shows the
	// agent or process.
	name string
}

// node is a BSP tree node: a pane (leaf) or a split of two subtrees.
type node struct {
	pane     *Pane
	paned    *gtk.Paned
	a, b     *node
	parent   *node
	vertical bool // children stacked top/bottom
	// ratio is the share of a to give a restored split once GTK lays it
	// out (restore.go); 0 when none is pending.
	ratio float64
}

func (n *node) widget() gtk.Widgetter {
	if n.pane != nil {
		return n.pane.frame
	}
	return n.paned
}

// panes lists the tab's panes in tree order (left/top first).
func (t *Tab) panes() []*Pane {
	var out []*Pane
	var walk func(n *node)
	walk = func(n *node) {
		if n == nil {
			return
		}
		if n.pane != nil {
			out = append(out, n.pane)
			return
		}
		walk(n.a)
		walk(n.b)
	}
	walk(t.root)
	return out
}

func (t *Tab) find(p *Pane) *node {
	var hit *node
	var walk func(n *node)
	walk = func(n *node) {
		if n == nil || hit != nil {
			return
		}
		if n.pane == p {
			hit = n
			return
		}
		walk(n.a)
		walk(n.b)
	}
	walk(t.root)
	return hit
}

// agentPane is the pane the tab's label describes: the first pane running a
// known agent, otherwise the focused pane.
func (t *Tab) agentPane() *Pane {
	for _, p := range t.panes() {
		if p.status().Agent != "" {
			return p
		}
	}
	return t.focus
}

// newTab starts a session running cmd (sh -c), or $SHELL when cmd is "",
// in dir, and shows it in a new selected tab.
func (a *App) newTab(cmd, dir string) *Tab {
	t := &Tab{app: a, holder: gtk.NewBox(gtk.OrientationVertical, 0)}
	t.holder.SetHExpand(true)
	t.holder.SetVExpand(true)
	cols, rows := a.initCols, a.initRows
	if cur := a.activePane(); cur != nil && cur.cols > 1 {
		cols, rows = cur.cols, cur.rows // a new tab starts at the window's size
	}
	p := a.newPane(t, argsFor(cmd), dir, cols, rows)
	if p == nil {
		return nil
	}
	t.id = p.id
	t.root = &node{pane: p}
	t.focus = p
	t.holder.Append(p.frame)
	a.tabs = append(a.tabs, t)
	a.stack.AddChild(t.holder)
	a.selectTab(t)
	a.refreshChrome()
	return t
}

func argsFor(cmd string) []string {
	if cmd == "" {
		return nil
	}
	return []string{"/bin/sh", "-c", cmd}
}

// newPane starts a session for tab t and its TermView.
func (a *App) newPane(t *Tab, args []string, dir string, cols, rows int) *Pane {
	return a.newPaneWith(t, args, dir, cols, rows, nil)
}

// newPaneWith is newPane that first shows history, the scrollback of a
// restored pane (session.History), ahead of the child's output.
func (a *App) newPaneWith(t *Tab, args []string, dir string, cols, rows int, history []byte) *Pane {
	p := &Pane{id: len(a.panes), app: a, tab: t, tv: newTermView(&a.theme)}
	a.panes = append(a.panes, p)
	ptr, cv := viewNewC(p.id)
	p.view = cv
	viewSetBGC(cv, a.theme.Background.Hex())
	p.widget = wrapWidget(ptr)
	// The pane sits in an overlay for its badges and find bar.
	p.frame = gtk.NewOverlay()
	p.frame.SetChild(p.widget)
	p.newBadgeRow()
	atlas, err := a.atlasFor(a.monScale)
	if err != nil {
		a.fatal("%v", err)
		return nil
	}
	p.atlas = atlas
	p.attachInput()

	opts := session.Options{
		ID: p.id, Cols: max(2, cols), Rows: max(1, rows), Scrollback: session.DefaultScrollbackLines, Dir: dir, Args: args,
		ThemeFG: xparse(a.theme.Foreground), ThemeBG: xparse(a.theme.Background), ThemeCursor: xparse(a.theme.Cursor),
		CellAspect: []float64{float64(atlas.CellH) / float64(atlas.CellW)},
		// Real device pixels, so applications size images 1:1.
		CellPixels: [2]int{atlas.CellW, atlas.CellH},
		Backspace:  input.KeyBackspace,
		Agents:     a.settings.Agents,
		ImageBytes: a.cfg.imageBytes,
		InputBytes: a.cfg.inputBytes,
		// Shell start-up files tell bunk-gui apart from the terminal it
		// was started from, e.g. to start bunk's TUI only outside it.
		Env: []string{"TERM_PROGRAM=bunk-gui", "TERM_PROGRAM_VERSION=" + Version},
	}
	// Agent hooks report through the notify socket with the session's token.
	if a.notify != nil {
		opts.KnownToken = a.notify.Issued
		if tok, err := notify.NewToken(); err == nil {
			p.token, opts.NotifySocket, opts.NotifySession = tok, a.notify.Path(), tok
		} else {
			slog.Warn("notify token", "err", err)
		}
	}
	s, err := session.Start(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bunk-gui: start session: %v\n", err)
		a.toast("Could not start a session: " + err.Error())
		a.panes[p.id] = nil
		return nil
	}
	// A program that runs out of memory in the pane must not end bunk-gui's
	// scope, with every other pane (scope.go).
	if bus := a.app.DBusConnection(); bus != nil && s.Cmd.Process != nil {
		moveToScope(bus, p.id, s.Cmd.Process.Pid)
	}
	p.s, p.cols, p.rows = s, max(2, cols), max(1, rows)
	p.title = defaultTitle
	p.st = s.Status()
	if len(history) > 0 {
		s.Feed(history)
	}
	s.Run(session.Hooks{
		Applied: p.onApplied, Output: p.onOutput, OSC: p.onOSC,
		Redraw: p.wake, Exited: p.onExited, Closed: p.onClosed,
		StatusChanged: p.onStatusChanged,
	}, a.done)
	if p.token != "" {
		a.notify.Register(p.token, s)
	}
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

// activePane is the focused pane of the selected tab.
func (a *App) activePane() *Pane {
	if a.active == nil {
		return nil
	}
	return a.active.focus
}

func (a *App) tabIndex(t *Tab) int {
	for i, x := range a.tabs {
		if x == t {
			return i
		}
	}
	return -1
}

// selectTab shows t and focuses its focused pane.
func (a *App) selectTab(t *Tab) {
	if t == nil {
		return
	}
	if a.find != nil && a.find.p.tab != t {
		a.closeFind()
	}
	a.active = t
	a.stack.SetVisibleChild(t.holder)
	if t.focus != nil {
		t.focus.widget.GrabFocus()
		a.win.SetTitle(t.focus.title)
		t.focus.queueRender()
	}
	a.refreshChrome()
}

// selectOffset selects the tab d positions away, wrapping around.
func (a *App) selectOffset(d int) {
	if n := len(a.tabs); n > 0 {
		a.selectTab(a.tabs[((a.tabIndex(a.active)+d)%n+n)%n])
	}
}

// moveTab moves the selected tab d places along the rail or the bar. At
// either end it stays where it is.
func (a *App) moveTab(d int) {
	i := a.tabIndex(a.active)
	j := i + d
	if i < 0 || j < 0 || j >= len(a.tabs) {
		return
	}
	a.tabs[i], a.tabs[j] = a.tabs[j], a.tabs[i]
	a.refreshChrome()
}

// closeTab ends every session of t (SIGHUP) and removes the tab. Closing
// the last tab quits.
func (a *App) closeTab(t *Tab) {
	i := a.tabIndex(t)
	if i < 0 {
		return
	}
	for _, p := range t.panes() {
		a.removePane(p)
	}
	a.tabs = append(a.tabs[:i], a.tabs[i+1:]...)
	if len(a.tabs) == 0 {
		a.finish("last tab closed")
		return
	}
	if a.active == t {
		a.selectTab(a.tabs[min(i, len(a.tabs)-1)])
	}
	a.stack.Remove(t.holder) // unrealizes the views: their GL objects are freed
	a.refreshChrome()
}

// removePane ends p's session (SIGHUP). Unrealizing the widget frees the GL
// objects and drops the pane from App.panes.
func (a *App) removePane(p *Pane) {
	if p.removeOnce {
		return
	}
	p.removeOnce = true
	if a.find != nil && a.find.p == p {
		a.closeFind()
	}
	if p.token != "" {
		a.notify.Unregister(p.token)
	}
	p.s.Close()
}

// paneDone runs when p's child is finished (Hooks.Closed, or Exited without
// Closed). The pane closes; a tab closes with its last pane; the last pane
// of the last tab draws one more frame, so the final output is on screen
// (and in --dump), then the app quits.
func (a *App) paneDone(p *Pane) {
	if p.removeOnce || a.exitArmed {
		return
	}
	t := p.tab
	if len(t.panes()) > 1 {
		a.closePane(p)
		return
	}
	if len(a.tabs) > 1 {
		a.closeTab(t)
		return
	}
	a.exitArmed = true
	p.queueRender()
	coreglib.TimeoutAdd(1000, func() bool { a.finish("child exited (no frame)"); return false })
}

// split puts a new pane next to p. auto picks the direction from p's pixel
// aspect (side by side when wider than tall). The new pane starts in p's
// directory; with args it runs them (e.g. a shell inside p's container).
func (a *App) split(p *Pane, args []string) *Pane {
	t := p.tab
	n := t.find(p)
	if n == nil {
		return nil
	}
	if t.zoom != nil {
		t.unzoom()
	}
	w, h := p.widget.Width(), p.widget.Height()
	vertical := h > w // stack top/bottom when taller than wide
	cols, rows := max(2, p.cols/2), p.rows
	if vertical {
		cols, rows = p.cols, max(1, p.rows/2)
	}
	np := a.newPane(t, args, p.s.Cwd(), cols, rows)
	if np == nil {
		return nil
	}
	orient := gtk.OrientationHorizontal
	if vertical {
		orient = gtk.OrientationVertical
	}
	a.win.SetFocus(nil) // see closePane
	paned := a.newPaned(orient, n)
	t.replace(n, paned)
	paned.SetStartChild(p.frame)
	paned.SetEndChild(np.frame)
	if vertical {
		paned.SetPosition(h / 2)
	} else {
		paned.SetPosition(w / 2)
	}
	old := &node{pane: p, parent: n}
	n.pane, n.paned, n.vertical = nil, paned, vertical
	n.a, n.b = old, &node{pane: np, parent: n}
	np.widget.GrabFocus()
	t.focus = np
	t.restyle()
	a.refreshChrome()
	return np
}

// newPaned returns the divider of split n. A restored split gets its
// ratio when GTK first lays it out, which for a tab not shown yet is when
// it is first selected.
func (a *App) newPaned(orient gtk.Orientation, n *node) *gtk.Paned {
	paned := gtk.NewPaned(orient)
	paned.SetWideHandle(false)
	paned.SetShrinkStartChild(false)
	paned.SetShrinkEndChild(false)
	paned.NotifyProperty("position", func() { a.splitMoved(paned) })
	paned.NotifyProperty("max-position", func() {
		size := paned.Width()
		if orient == gtk.OrientationVertical {
			size = paned.Height()
		}
		if n.ratio <= 0 || size <= 1 {
			return
		}
		pos := int(math.Round(n.ratio * float64(size-1))) // the divider takes 1 px
		n.ratio = 0
		// Not during the allocation that announced the size.
		coreglib.IdleAdd(func() { paned.SetPosition(pos) })
	})
	return paned
}

// replace puts w where n's widget is in the tree's GTK hierarchy and
// detaches n's widget.
func (t *Tab) replace(n *node, w gtk.Widgetter) {
	old := n.widget()
	switch par := n.parent; {
	case par == nil:
		t.holder.Remove(old)
		t.holder.Append(w)
	case par.a == n:
		par.paned.SetStartChild(nil)
		par.paned.SetStartChild(w)
	default:
		par.paned.SetEndChild(nil)
		par.paned.SetEndChild(w)
	}
}

// closePane removes p from its tab; its sibling takes the parent split's
// place.
func (a *App) closePane(p *Pane) {
	t := p.tab
	n := t.find(p)
	if n == nil {
		return
	}
	if t.zoom != nil {
		t.unzoom()
	}
	a.removePane(p)
	par := n.parent
	if par == nil {
		a.closeTab(t)
		return
	}
	sib := par.a
	if sib == n {
		sib = par.b
	}
	if t.focus == p {
		t.focus = t.firstPane(sib)
	}
	// Drop the keyboard focus while widgets move between GtkPaneds, which
	// otherwise keep dangling focus children.
	a.win.SetFocus(nil)
	par.paned.SetStartChild(nil)
	par.paned.SetEndChild(nil)
	t.replace(par, sib.widget())
	sib.parent = par.parent
	switch {
	case par.parent == nil:
		t.root = sib
	case par.parent.a == par:
		par.parent.a = sib
	default:
		par.parent.b = sib
	}
	if a.active == t {
		t.focus.widget.GrabFocus() // re-parenting dropped the focus
	}
	t.restyle()
	a.refreshChrome()
}

func (t *Tab) firstPane(n *node) *Pane {
	for n.pane == nil {
		n = n.a
	}
	return n.pane
}

// toggleZoom shows p alone in its tab (F12), or restores the splits.
func (t *Tab) toggleZoom(p *Pane) {
	if t == nil || p == nil {
		return
	}
	if t.zoom != nil {
		t.unzoom()
		return
	}
	n := t.find(p)
	if n == nil || n.parent == nil {
		return
	}
	t.zoom = p
	for c := n; c.parent != nil; c = c.parent {
		sib := c.parent.a
		if sib == c {
			sib = c.parent.b
		}
		sib.widget().(interface{ SetVisible(bool) }).SetVisible(false)
	}
	t.restyle()
	p.syncBadges()
}

func (t *Tab) unzoom() {
	was := t.zoom
	t.zoom = nil
	var walk func(n *node)
	walk = func(n *node) {
		if n == nil {
			return
		}
		n.widget().(interface{ SetVisible(bool) }).SetVisible(true)
		walk(n.a)
		walk(n.b)
	}
	walk(t.root)
	t.restyle()
	if was != nil {
		was.syncBadges()
	}
}

// restyle redraws panes whose dimming changed: with more than one visible
// pane, every pane but the focused one is dimmed.
func (t *Tab) restyle() {
	for _, p := range t.panes() {
		p.queueRender()
	}
}

// dimmed reports whether p is drawn faded (an inactive pane of a split).
func (t *Tab) dimmed(p *Pane) bool {
	return t.zoom == nil && t.root != nil && t.root.pane == nil && t.focus != p
}

// moveFocus focuses the nearest pane in a direction (Alt+arrows): the pane
// whose rectangle lies beyond the focused one on that side and overlaps it
// most on the other axis.
func (t *Tab) moveFocus(dx, dy int) {
	cur := t.focus
	if cur == nil || t.zoom != nil {
		return
	}
	cb, ok := cur.widget.ComputeBounds(t.holder)
	if !ok {
		return
	}
	cx, cy, cw, ch := float64(cb.X()), float64(cb.Y()), float64(cb.Width()), float64(cb.Height())
	var best *Pane
	bestScore := 1e18
	for _, p := range t.panes() {
		if p == cur {
			continue
		}
		b, ok := p.widget.ComputeBounds(t.holder)
		if !ok {
			continue
		}
		x, y, w, h := float64(b.X()), float64(b.Y()), float64(b.Width()), float64(b.Height())
		var gap, overlap float64
		switch {
		case dx < 0 && x+w <= cx+2:
			gap, overlap = cx-(x+w), min(y+h, cy+ch)-max(y, cy)
		case dx > 0 && x >= cx+cw-2:
			gap, overlap = x-(cx+cw), min(y+h, cy+ch)-max(y, cy)
		case dy < 0 && y+h <= cy+2:
			gap, overlap = cy-(y+h), min(x+w, cx+cw)-max(x, cx)
		case dy > 0 && y >= cy+ch-2:
			gap, overlap = y-(cy+ch), min(x+w, cx+cw)-max(x, cx)
		default:
			continue
		}
		if overlap <= 0 {
			continue
		}
		if score := gap*1000 - overlap; score < bestScore {
			best, bestScore = p, score
		}
	}
	if best != nil {
		best.widget.GrabFocus()
	}
}

// onPaneFocus runs when a pane's view gets keyboard focus.
func (a *App) onPaneFocus(p *Pane) {
	if p.removeOnce {
		return
	}
	t := p.tab
	if t.focus != p {
		t.focus = p
		t.restyle()
	}
	p.acknowledge(session.AckSeen)
	if a.active == t {
		a.win.SetTitle(p.title)
	}
	a.syncMarks() // a pane the user now watches drops its mark
}

// setTitle records an OSC 0/2 title for p (shown as the window title while
// p is focused).
func (a *App) setTitle(p *Pane, title string) {
	if p.removeOnce {
		return
	}
	if title == "" {
		title = defaultTitle
	}
	p.title = title
	if a.activePane() == p {
		a.win.SetTitle(title)
	}
}

// jumpNeedingYou selects the next tab (after the selected one) whose agent
// needs the user, else the next whose agent is done (next_needs_you).
// Focusing the pane acknowledges it.
func (a *App) jumpNeedingYou() {
	i := a.tabIndex(a.active)
	for _, want := range []session.AgentState{session.AgentNeedsYou, session.AgentDone} {
		for k := 1; k <= len(a.tabs); k++ {
			t := a.tabs[(i+k)%len(a.tabs)]
			for _, p := range t.panes() {
				if p.status().State == want {
					a.selectTab(t)
					if t.focus != p {
						p.widget.GrabFocus()
					}
					a.onPaneFocus(p)
					return
				}
			}
		}
	}
	a.toast("No agent needs you")
}

// setPassthrough turns sending all keys to the terminal on or off for p:
// every key but the passthrough binding then goes to the terminal.
func (a *App) setPassthrough(p *Pane, on bool) {
	p.passthrough = on
	p.syncBadges()
	a.refreshChrome()
	a.syncPassCheck()
}

// syncPassCheck shows the focused pane's passthrough in the options menu.
func (a *App) syncPassCheck() {
	p := a.activePane()
	if a.passCheck == nil || p == nil {
		return
	}
	a.syncingMenu = true
	a.passCheck.SetActive(p.passthrough)
	a.syncingMenu = false
}
