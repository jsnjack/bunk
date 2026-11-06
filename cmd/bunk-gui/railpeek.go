package main

// The rail peek: holding Alt alone in a window with the collapsed rail
// shows a label beside each tab's ring. A label holds the key that selects
// the tab (Alt+N, without the Alt), its state dot, its name or agent, and
// repo ⎇ branch and the places it runs, as the expanded rail shows them.
// The labels float over the panes, which keep their size, and take no
// clicks.
//
//   - They show once Alt has been held for railPeekDelay with no other
//     key, so Alt+A, Alt+1 and a shell's Alt+B never flash them.
//   - Alt+N while they show selects a tab, and the labels follow.
//   - Releasing Alt, the window going inactive, a dialog or the switcher
//     opening, and leaving the collapsed rail hide them.
//
// The labels sit at the rail items' places, which GTK knows only after
// it lays the items out, so after-paint places them (placeRailPeek): at
// once when Alt shows them, and in the frame after a chrome rebuild.

import (
	"fmt"
	"strings"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"
)

// railPeekDelay is how long Alt is held alone before the labels show.
const railPeekDelay = 400 // ms

// railPeekRowH is a label's height in logical pixels, inside the 40 px
// rail item it sits beside.
const railPeekRowH = 30

// railPeek is the state of the rail peek.
type railPeek struct {
	// seq numbers each press of Alt; a timer that finds another number
	// was overtaken by a release or another key. Timers are never
	// removed: removing one that has run makes GLib log a critical.
	seq     uint64
	pending bool // Alt is held alone and the timer runs

	layer *gtk.Fixed // the labels while shown; nil when hidden
	rows  []railPeekRow
	gen   uint64 // a.chromeGen the rows show; another means rebuild
}

// railPeekRow is one tab's label and the rail item it sits beside.
type railPeekRow struct {
	t      *Tab
	box    *gtk.Box
	labels []*gtk.Label
	item   gtk.Widgetter
	text   string // the label's texts joined by spaces, for the report

	x, y   float64 // where the label was put in the layer
	placed bool
}

// railPeekKey follows the keys for the rail peek. down is a press.
func (a *App) railPeekKey(keyval uint, state gdk.ModifierType, down bool) {
	pk := &a.peek
	alt := keyval == gdk.KEY_Alt_L || keyval == gdk.KEY_Alt_R
	switch {
	case alt && !down:
		pk.seq++
		pk.pending = false
		a.hideRailPeek()
	case alt && down:
		if pk.pending || pk.layer != nil {
			return // a repeat of the held key
		}
		if state&(gdk.ControlMask|gdk.ShiftMask|gdk.SuperMask) != 0 || !a.railPeekFits() {
			return
		}
		pk.seq++
		pk.pending = true
		seq := pk.seq
		coreglib.TimeoutAdd(railPeekDelay, func() bool {
			if pk.seq == seq && pk.pending {
				pk.pending = false
				a.showRailPeek()
			}
			return false
		})
	case down && pk.pending:
		// A combination such as Alt+A: no peek this time.
		pk.seq++
		pk.pending = false
	}
}

// railPeekFits reports whether the peek may show: the collapsed rail in
// the active window, with no dialog or switcher over it.
func (a *App) railPeekFits() bool {
	return a.layout == "rail" && !a.railOpen && !a.finished && a.win.IsActive() &&
		!a.modalOpen() && a.switcher == nil && a.tabsBox != nil
}

func (a *App) showRailPeek() {
	pk := &a.peek
	if pk.layer != nil || !a.railPeekFits() {
		return
	}
	pk.layer = gtk.NewFixed()
	pk.layer.SetCanTarget(false) // clicks reach the panes below
	pk.layer.AddCSSClass("railpeek-layer")
	a.overlay.AddOverlay(pk.layer)
	a.buildRailPeek()
	a.placeRailPeek()
}

// hideRailPeek removes the labels, if they show.
func (a *App) hideRailPeek() {
	pk := &a.peek
	if pk.layer == nil {
		return
	}
	a.overlay.RemoveOverlay(pk.layer)
	pk.layer, pk.rows = nil, nil
}

// buildRailPeek makes a label for each rail item, from the tab infos the
// chrome shows.
func (a *App) buildRailPeek() {
	pk := &a.peek
	for _, r := range pk.rows {
		pk.layer.Remove(r.box)
	}
	pk.rows = pk.rows[:0]
	pk.gen = a.chromeGen
	infos := a.tabInfos()
	if len(infos) != len(a.tabItems) {
		return // a rebuild is on its way; the next after-paint retries
	}
	for i, in := range infos {
		box := gtk.NewBox(gtk.OrientationHorizontal, 8)
		box.AddCSSClass("railpeek")
		if in.active {
			box.AddCSSClass("active")
		}
		if in.needs {
			box.AddCSSClass("needs")
		}
		box.SetSizeRequest(-1, railPeekRowH)
		var texts []string
		var labels []*gtk.Label
		add := func(l *gtk.Label) {
			l.SetVAlign(gtk.AlignCenter)
			box.Append(l)
			texts = append(texts, l.Text())
			labels = append(labels, l)
		}
		if k := strings.TrimPrefix(a.firstKey(fmt.Sprintf("select_tab_%d", in.index+1)), "Alt+"); k != "" {
			add(label(k, "peekkey"))
		}
		add(label(in.look.dot, "dot", in.look.cls))
		name := in.name
		if name == "" {
			name = in.agent
		}
		if in.more > 0 {
			name += fmt.Sprintf(" +%d", in.more)
		}
		add(label(name, "agent"))
		repo := label(in.repo, "repo")
		repo.SetEllipsize(pango.EllipsizeEnd)
		repo.SetMaxWidthChars(40)
		add(repo)
		if in.loc != "" {
			loc := label(in.loc, "loc")
			loc.SetEllipsize(pango.EllipsizeEnd)
			loc.SetMaxWidthChars(30)
			add(loc)
		}
		pk.layer.Put(box, 0, 0)
		// GtkFixed gives a child its minimum size, where an ellipsizing
		// label is only "…": ask for the natural width, which the labels'
		// max-width-chars bound.
		_, nat, _, _ := box.Measure(gtk.OrientationHorizontal, -1)
		box.SetSizeRequest(nat, railPeekRowH)
		pk.rows = append(pk.rows, railPeekRow{t: in.t, box: box, labels: labels, item: a.tabItems[i].item,
			text: strings.Join(texts, " ")})
	}
}

// placeRailPeek puts each label right of the rail, level with its item.
// It runs after every paint while the labels show, and moves nothing
// that is in place, so it draws no frames of its own.
func (a *App) placeRailPeek() {
	pk := &a.peek
	if pk.layer == nil {
		return
	}
	if !a.railPeekFits() {
		a.hideRailPeek()
		return
	}
	if pk.gen != a.chromeGen {
		a.buildRailPeek()
	}
	rail, ok := gtk.BaseWidget(a.chrome).ComputeBounds(a.overlay)
	if !ok {
		return
	}
	x := float64(rail.X()+rail.Width()) + 6
	for i := range pk.rows {
		r := &pk.rows[i]
		b, ok := gtk.BaseWidget(r.item).ComputeBounds(a.overlay)
		if !ok {
			continue
		}
		y := float64(b.Y()) + (float64(b.Height())-railPeekRowH)/2
		// ChildPosition gives the content's place, inside the label's
		// border and padding, so the row keeps the place it was given.
		if !r.placed || r.x != x || r.y != y {
			pk.layer.Move(r.box, x, y)
			r.x, r.y, r.placed = x, y, true
		}
	}
}
