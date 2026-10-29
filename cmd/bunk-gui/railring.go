package main

// The collapsed rail draws every tab as one ring (rail_style = "ring",
// the default): an arc per pane in the colour of its state, with an empty
// centre; a tab of one pane is a closed ring. Each arc faces its pane:
// panes side by side take the left and right halves, stacked ones the
// top and bottom (ringArcs). rail_style = "map" draws a tab's state
// glyph, or for 2 to 4 panes their dots placed like the panes (railMap).

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/diamondburned/gotk4/pkg/cairo"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// stateRGB is the colour of each state class, as chromeCSS gives it
// (.working, .needs-fg and the others); TestStateRGBMatchesCSS keeps the
// two in step.
var stateRGB = map[string]uint32{
	"working": 0x5aa9ff, "needs-fg": 0xffb454, "idle": 0x9aa0a8, "failed": 0xff8a8a, "done": 0x7ee2a8,
}

// railRingSize is the ring's side in logical pixels, inside the 40 px
// rail button.
const railRingSize = 22

// railRingCount is the most panes whose ring shows no count: past it the
// arcs are too short to tell apart, so the count goes under the ring.
const railRingCount = 6

// ringArc is one pane's arc: its dot and the direction of the arc's
// middle, in radians clockwise from the top.
type ringArc struct {
	look
	at float64
}

// ringArcs gives the panes under n equal arcs that face the panes. Each
// split halves its space, as in railMap, and a pane's direction is the
// one from the middle of the tab to the middle of its space. The arcs go
// around in the order of those directions, turned as a whole to lie as
// close to them as they can (the circular mean of the differences).
func ringArcs(n *node, dot func(*Pane) look) []ringArc {
	var arcs []ringArc
	var walk func(n *node, x, y, w, h float64)
	walk = func(n *node, x, y, w, h float64) {
		if n.pane != nil {
			dx, dy := x+w/2-0.5, y+h/2-0.5
			at := 0.0
			if dx != 0 || dy != 0 {
				at = math.Mod(math.Atan2(dx, -dy)+2*math.Pi, 2*math.Pi)
			}
			arcs = append(arcs, ringArc{dot(n.pane), at})
			return
		}
		if n.vertical {
			walk(n.a, x, y, w, h/2)
			walk(n.b, x, y+h/2, w, h/2)
		} else {
			walk(n.a, x, y, w/2, h)
			walk(n.b, x+w/2, y, w/2, h)
		}
	}
	walk(n, 0, 0, 1, 1)
	sort.SliceStable(arcs, func(i, j int) bool { return arcs[i].at < arcs[j].at })
	step := 2 * math.Pi / float64(len(arcs))
	var sin, cos float64
	for i, a := range arcs {
		d := a.at - float64(i)*step
		sin, cos = sin+math.Sin(d), cos+math.Cos(d)
	}
	turn := math.Atan2(sin, cos)
	for i := range arcs {
		arcs[i].at = math.Mod(turn+float64(i)*step+4*math.Pi, 2*math.Pi)
	}
	return arcs
}

// ringDots writes a ring as the report shows it: each arc's dot and the
// direction of its middle in degrees clockwise from the top, from the
// top round, "○@30 ○@150 ◐@270".
func ringDots(arcs []ringArc) string {
	sorted := append([]ringArc(nil), arcs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].at < sorted[j].at })
	parts := make([]string, len(sorted))
	for i, a := range sorted {
		parts[i] = fmt.Sprintf("%s@%d", a.dot, int(math.Round(a.at*180/math.Pi))%360)
	}
	return strings.Join(parts, " ")
}

// railRing draws the arcs. A plain shell's arc (the hollow dot) is
// dimmed, so the agents' arcs stand out.
func railRing(arcs []ringArc) *gtk.DrawingArea {
	d := gtk.NewDrawingArea()
	d.SetContentWidth(railRingSize)
	d.SetContentHeight(railRingSize)
	d.SetHAlign(gtk.AlignCenter)
	d.SetVAlign(gtk.AlignCenter)
	d.SetCanTarget(false) // clicks go to the rail button
	d.SetDrawFunc(func(_ *gtk.DrawingArea, cr *cairo.Context, w, h int) {
		cx, cy := float64(w)/2, float64(h)/2
		const line = 4.0
		r := math.Min(cx, cy) - line/2
		n := float64(len(arcs))
		gap := 0.0
		if n > 1 {
			gap = 2.2 / r // about 2 px between arcs
		}
		cr.SetLineWidth(line)
		cr.SetLineCap(cairo.LineCapButt)
		for _, l := range arcs {
			mid := l.at - math.Pi/2 // cairo's angles start at the right
			from, to := mid-math.Pi/n+gap/2, mid+math.Pi/n-gap/2
			alpha := 1.0
			if l.dot == "○" {
				alpha = 0.45
			}
			setRGB(cr, l.cls, alpha)
			cr.NewPath()
			cr.Arc(cx, cy, r, from, to)
			cr.Stroke()
		}
	})
	return d
}

func setRGB(cr *cairo.Context, cls string, alpha float64) {
	c := stateRGB[cls]
	cr.SetSourceRGBA(float64(c>>16&0xff)/255, float64(c>>8&0xff)/255, float64(c&0xff)/255, alpha)
}
