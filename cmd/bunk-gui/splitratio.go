package main

// While a divider is held, a label on it shows how its split shares the
// space ("60% │ 40%"), so an even split is easy to find. bunk's TUI has no
// divider to drag.

/*
#cgo pkg-config: gtk4
#include <gtk/gtk.h>
#include <stdint.h>

extern void goButtonPressed(double x, double y);
extern void goButtonReleased(void);

static gboolean split_event(GtkEventControllerLegacy *c, GdkEvent *ev, gpointer data) {
	double x, y;
	switch (gdk_event_get_event_type(ev)) {
	case GDK_BUTTON_PRESS:
		if (gdk_event_get_position(ev, &x, &y))
			goButtonPressed(x, y);
		break;
	case GDK_BUTTON_RELEASE:
		goButtonReleased();
		break;
	default:
		break;
	}
	return FALSE;
}

// split_watch sees the window's button events before any widget does.
// gotk4 v0.4.1 cannot pass a GdkEvent to a Go signal handler: it treats
// the event as a GObject.
static void split_watch(uintptr_t win) {
	GtkEventController *c = gtk_event_controller_legacy_new();
	gtk_event_controller_set_propagation_phase(c, GTK_PHASE_CAPTURE);
	g_signal_connect(c, "event", G_CALLBACK(split_event), NULL);
	gtk_widget_add_controller(GTK_WIDGET((GtkWindow *)win), c);
}
*/
import "C"

import (
	"fmt"
	"math"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// handleReach is how far beside its 1 px line a divider takes a press:
// GtkPaned's HANDLE_EXTRA_SIZE.
const handleReach = 6

type splitRatio struct {
	box   *gtk.Box
	a, b  *gtk.Label
	sep   *gtk.Separator
	paned *gtk.Paned // the split whose divider is held; nil when none
}

// installSplitRatio watches presses before any widget takes them: a press
// on a divider shows its split's ratio until the button is released.
func (a *App) installSplitRatio() { C.split_watch(C.uintptr_t(nativePtr(a.win))) }

// buttonPressed takes a press at surface coordinates x, y.
func (a *App) buttonPressed(x, y float64) {
	sx, sy := a.win.SurfaceTransform()
	if n := a.dividerAt(x-sx, y-sy); n != nil {
		a.showSplitRatio(n.paned)
	}
}

// dividerAt is the split of the selected tab whose divider takes a press
// at window coordinates x, y.
func (a *App) dividerAt(x, y float64) *node {
	t := a.active
	if t == nil || t.zoom != nil {
		return nil
	}
	var walk func(n *node) *node
	walk = func(n *node) *node {
		if n == nil || n.paned == nil {
			return nil
		}
		if dx, dy, dw, dh, ok := a.dividerRect(n); ok {
			if n.vertical {
				dy, dh = dy-handleReach, dh+2*handleReach
			} else {
				dx, dw = dx-handleReach, dw+2*handleReach
			}
			if x >= dx && x < dx+dw && y >= dy && y < dy+dh {
				return n
			}
		}
		if hit := walk(n.a); hit != nil {
			return hit
		}
		return walk(n.b)
	}
	return walk(t.root)
}

func (a *App) showSplitRatio(p *gtk.Paned) {
	r := a.ratio
	if r == nil {
		r = &splitRatio{}
		r.box = gtk.NewBox(gtk.OrientationHorizontal, 0)
		r.box.AddCSSClass("splitratio")
		r.box.SetHAlign(gtk.AlignStart)
		r.box.SetVAlign(gtk.AlignStart)
		r.box.SetCanTarget(false)
		r.a, r.b = gtk.NewLabel(""), gtk.NewLabel("")
		r.sep = gtk.NewSeparator(gtk.OrientationVertical)
		r.box.Append(r.a)
		r.box.Append(r.sep)
		r.box.Append(r.b)
		a.overlay.AddOverlay(r.box)
		a.ratio = r
	}
	r.paned = p
	if p.Orientation() == gtk.OrientationVertical {
		r.box.SetOrientation(gtk.OrientationVertical)
		r.sep.SetOrientation(gtk.OrientationHorizontal)
	} else {
		r.box.SetOrientation(gtk.OrientationHorizontal)
		r.sep.SetOrientation(gtk.OrientationVertical)
	}
	r.box.SetVisible(true) // before updateSplitRatio measures it
	a.updateSplitRatio()
}

func (a *App) hideSplitRatio() {
	if r := a.ratio; r != nil && r.paned != nil {
		r.paned = nil
		r.box.SetVisible(false)
	}
}

// splitMoved follows a split's divider while it is held.
func (a *App) splitMoved(p *gtk.Paned) {
	if r := a.ratio; r != nil && r.paned == p {
		a.updateSplitRatio()
	}
}

// updateSplitRatio shows the held split's shares and centres the label
// on its divider.
func (a *App) updateSplitRatio() {
	r := a.ratio
	p := r.paned
	start, end := p.StartChild(), p.EndChild()
	b, ok := p.ComputeBounds(a.overlay)
	if start == nil || end == nil || !ok {
		a.hideSplitRatio()
		return
	}
	vertical := p.Orientation() == gtk.OrientationVertical
	size := func(w gtk.Widgetter) int {
		if vertical {
			return gtk.BaseWidget(w).Height()
		}
		return gtk.BaseWidget(w).Width()
	}
	// The children's sizes add up to the space the divider shares out;
	// the position is the start child's size, ahead of the allocation.
	total := size(start) + size(end)
	if total <= 0 {
		return
	}
	pos := p.Position()
	share := min(100, max(0, int(math.Round(100*float64(pos)/float64(total)))))
	r.a.SetText(fmt.Sprintf("%d%%", share))
	r.b.SetText(fmt.Sprintf("%d%%", 100-share))
	// A measure includes the margins this sets.
	_, w, _, _ := r.box.Measure(gtk.OrientationHorizontal, -1)
	_, h, _, _ := r.box.Measure(gtk.OrientationVertical, -1)
	w -= r.box.MarginStart()
	h -= r.box.MarginTop()
	cx, cy := float64(b.X())+float64(b.Width())/2, float64(b.Y())+float64(b.Height())/2
	if vertical {
		cy = float64(b.Y()) + float64(pos)
	} else {
		cx = float64(b.X()) + float64(pos)
	}
	r.box.SetMarginStart(max(0, int(cx)-w/2))
	r.box.SetMarginTop(max(0, int(cy)-h/2))
}
