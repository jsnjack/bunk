package main

// Tabs move by dragging them in the rail or the bar. A line marks where
// the tab goes, and the move happens on release. The gesture sits on the
// tab list, so it outlives the tab items it marks. While a tab is pressed,
// refreshChrome waits: a rebuild would replace the button being clicked.

import (
	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// dragThreshold is how far a press moves before it drags a tab instead of
// clicking it, in logical pixels (GTK's default dnd-drag-threshold).
const dragThreshold = 8

type tabDrag struct {
	t      *Tab
	x, y   float64 // the press, in the tab list's coordinates
	active bool    // past the threshold: the click is cancelled
	slot   int     // where the tab goes: before the item with this index
	stale  bool    // refreshChrome waited for the press to end
	marked gtk.Widgetter
}

// watchTabDrag lets the tabs in a.tabsBox be dragged.
func (a *App) watchTabDrag() {
	box := a.tabsBox
	g := gtk.NewGestureDrag()
	g.SetPropagationPhase(gtk.PhaseCapture) // before the tab's button takes the press
	g.ConnectDragBegin(func(x, y float64) {
		i := a.tabItemAt(box, x, y)
		if i < 0 {
			g.SetState(gtk.EventSequenceDenied)
			return
		}
		a.tabDrag = &tabDrag{t: a.tabs[i], x: x, y: y, slot: i}
	})
	g.ConnectDragUpdate(func(dx, dy float64) {
		d := a.tabDrag
		if d == nil {
			return
		}
		if !d.active {
			if dx*dx+dy*dy < dragThreshold*dragThreshold {
				return
			}
			d.active = true
			g.SetState(gtk.EventSequenceClaimed)
			if it := a.tabItemOf(d.t); it != nil {
				gtk.BaseWidget(it).AddCSSClass("dragged")
			}
		}
		a.markDropSlot(box, d.x+dx, d.y+dy)
	})
	g.ConnectDragEnd(func(dx, dy float64) {
		d := a.tabDrag
		a.tabDrag = nil
		if d == nil {
			return
		}
		if !d.active {
			// A click: the button sees the release after this gesture, so
			// rebuild after it.
			if d.stale {
				coreglib.IdleAdd(a.refreshChrome)
			}
			return
		}
		a.unmarkDrop(d)
		if it := a.tabItemOf(d.t); it != nil {
			gtk.BaseWidget(it).RemoveCSSClass("dragged")
		}
		a.moveTabTo(d.t, d.slot)
	})
	box.AddController(g)
}

// tabItemAt is the index of the tab item at x, y in box, or -1.
func (a *App) tabItemAt(box *gtk.Box, x, y float64) int {
	for i, it := range a.tabItems {
		if b, ok := gtk.BaseWidget(it.item).ComputeBounds(box); ok &&
			x >= float64(b.X()) && x < float64(b.X()+b.Width()) && y >= float64(b.Y()) && y < float64(b.Y()+b.Height()) {
			return i
		}
	}
	return -1
}

func (a *App) tabItemOf(t *Tab) gtk.Widgetter {
	if i := a.tabIndex(t); i >= 0 && i < len(a.tabItems) {
		return a.tabItems[i].item
	}
	return nil
}

// markDropSlot finds the slot for a tab dragged to x, y in box (before the
// first item whose centre is past the pointer) and marks it with a line.
func (a *App) markDropSlot(box *gtk.Box, x, y float64) {
	d := a.tabDrag
	vertical := box.Orientation() == gtk.OrientationVertical
	slot := 0
	for _, it := range a.tabItems {
		b, ok := gtk.BaseWidget(it.item).ComputeBounds(box)
		if !ok {
			continue
		}
		mid, at := float64(b.X())+float64(b.Width())/2, x
		if vertical {
			mid, at = float64(b.Y())+float64(b.Height())/2, y
		}
		if at > mid {
			slot++
		}
	}
	a.unmarkDrop(d)
	d.slot = slot
	i := a.tabIndex(d.t)
	if slot == i || slot == i+1 || len(a.tabItems) == 0 {
		return // it would stay where it is
	}
	cls := "drop-before"
	if slot == len(a.tabItems) {
		slot, cls = slot-1, "drop-after"
	}
	d.marked = a.tabItems[slot].item
	gtk.BaseWidget(d.marked).AddCSSClass(cls)
}

func (a *App) unmarkDrop(d *tabDrag) {
	if d.marked != nil {
		gtk.BaseWidget(d.marked).RemoveCSSClass("drop-before")
		gtk.BaseWidget(d.marked).RemoveCSSClass("drop-after")
		d.marked = nil
	}
}

// moveTabTo moves t to slot (an index in a.tabs before the move).
func (a *App) moveTabTo(t *Tab, slot int) {
	i := a.tabIndex(t)
	if i < 0 {
		return
	}
	if slot > i {
		slot--
	}
	slot = min(max(slot, 0), len(a.tabs)-1)
	if slot != i {
		a.tabs = append(a.tabs[:i], a.tabs[i+1:]...)
		a.tabs = append(a.tabs[:slot], append([]*Tab{t}, a.tabs[slot:]...)...)
	}
	a.refreshChrome() // also the changes held back during the drag
}
