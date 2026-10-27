// Package boxdraw draws the characters that terminals draw themselves
// instead of taking them from the font: box drawing, block elements and
// shades, braille, the symbols for legacy computing (sextants, octants,
// wedges and their supplement), powerline separators, Fira Code's
// progress bar and spinner, and the branch drawing symbols of git graphs.
// The set is the one kitty draws.
//
// A font's glyph for such a character seldom fills the cell to its edges,
// so neighbouring cells show seams. Draw fills the cell exactly, and its
// lines sit where the lines of neighbouring cells do, so they join. bunk-gui
// puts these masks in its glyph atlas in place of the font's.
package boxdraw

import "math"

// Has reports whether Draw draws r.
func Has(r rune) bool {
	switch {
	case r >= 0x2500 && r <= 0x259f, // box drawing, block elements
		r == 0x25c9, r == 0x25cb, r == 0x25cf, r == 0x25d6, r == 0x25d7,
		r >= 0x25dc && r <= 0x25e5,                           // arcs and corner triangles
		r >= 0x2800 && r <= 0x28ff,                           // braille
		r >= 0xe0b0 && r <= 0xe0bf, r == 0xe0d6, r == 0xe0d7, // powerline
		r >= 0xee00 && r <= 0xee0b,   // Fira Code progress bar and spinner
		r >= 0xf5d0 && r <= 0xf60d,   // branch drawing
		r >= 0x1cc1b && r <= 0x1cc3f, // legacy computing supplement
		r >= 0x1cd00 && r <= 0x1cde5,
		r >= 0x1ce16 && r <= 0x1ce19,
		r >= 0x1ce51 && r <= 0x1ceaf,
		r >= 0x1fb00 && r <= 0x1fbae, // legacy computing
		r >= 0x1fbce && r <= 0x1fbef:
		return true
	}
	return false
}

// LineWidth returns the width in pixels of a light line in a cell cellW
// pixels wide. Heavy lines are about twice as wide, and a double line is
// two light lines with a light line's gap between them.
func LineWidth(cellW int) int {
	return max(1, int(math.Round(float64(cellW)/6.5)))
}

// Draw draws r into dst as coverage, one byte per pixel from 0 (empty) to
// 255 (covered): w×h pixels whose rows start stride bytes apart. line is
// the width of a light line (LineWidth of one cell's width; a wide
// character spans two cells). Draw reports false and leaves dst alone when
// r is not one of the characters it draws.
func Draw(r rune, dst []byte, stride, w, h, line int) bool {
	if !Has(r) {
		return false
	}
	if w <= 0 || h <= 0 {
		return true
	}
	c := newCanvas(dst, stride, w, h, line)
	c.fill(0, 0, w, h, 0)
	c.draw(r)
	return true
}

func (c *canvas) draw(r rune) {
	switch {
	case r <= 0x257f:
		c.boxDrawing(r)
	case r <= 0x259f:
		c.block(r)
	case r <= 0x25ff:
		c.geometric(r)
	case r <= 0x28ff:
		c.braille(uint(r - 0x2800))
	case r <= 0xe0ff:
		c.powerline(r)
	case r <= 0xeeff:
		c.progress(r)
	case r <= 0xf6ff:
		c.branch(r)
	case r <= 0x1cc3f:
		c.supplementLines(r)
	case r <= 0x1cdff:
		c.grid(2, 4, uint(octants[r-0x1cd00]))
	case r <= 0x1ceaf:
		c.supplementBlocks(r)
	default:
		c.legacy(r)
	}
}

// canvas is the mask being drawn. Coordinates are pixels from the cell's
// top left corner; a pixel's centre is at +0.5.
type canvas struct {
	pix    []byte
	stride int
	w, h   int
	t      int     // width of a light line
	heavy  int     // width of a heavy line
	cx, cy float64 // centre lines of a light vertical and horizontal line
}

func newCanvas(pix []byte, stride, w, h, line int) *canvas {
	t := max(1, line)
	c := &canvas{pix: pix, stride: stride, w: w, h: h, t: t}
	// A heavy line keeps the light line's parity, so both have the same
	// centre and a light line meets a heavy one without a step.
	c.heavy = t + 2*max(1, t/2)
	x0, _ := c.vband(t)
	y0, _ := c.hband(t)
	c.cx = float64(x0) + float64(t)/2
	c.cy = float64(y0) + float64(t)/2
	return c
}

// part returns the k-th of n equal divisions of size, rounded to a pixel,
// so every character that splits the cell at a fraction splits it at the
// same pixel: the half block ends where the quarter blocks' second row
// does.
func part(size, k, n int) int { return (2*k*size + n) / (2 * n) }

// hband and vband return the rows of a horizontal line and the columns of
// a vertical line of width th through the middle of the cell.
func (c *canvas) hband(th int) (int, int) { y := (c.h - th) / 2; return y, y + th }
func (c *canvas) vband(th int) (int, int) { x := (c.w - th) / 2; return x, x + th }

// fill sets the pixels of a rectangle, clipped to the cell, to v.
func (c *canvas) fill(x0, y0, x1, y1 int, v byte) {
	x0, y0 = max(x0, 0), max(y0, 0)
	x1, y1 = min(x1, c.w), min(y1, c.h)
	for y := y0; y < y1; y++ {
		row := c.pix[y*c.stride:]
		for x := x0; x < x1; x++ {
			row[x] = v
		}
	}
}

// rect covers a rectangle fully.
func (c *canvas) rect(x0, y0, x1, y1 int) { c.fill(x0, y0, x1, y1, 255) }

// span covers the rectangle from x0/n to x1/n of the width and from y0/n
// to y1/n of the height, at least a pixel each way, so an eighth of a
// small cell still shows.
func (c *canvas) span(x0, x1, y0, y1, n int) {
	ax, bx := atLeastOne(part(c.w, x0, n), part(c.w, x1, n), c.w)
	ay, by := atLeastOne(part(c.h, y0, n), part(c.h, y1, n), c.h)
	c.rect(ax, ay, bx, by)
}

// atLeastOne widens an empty range [a, b) of size to one pixel.
func atLeastOne(a, b, size int) (int, int) {
	if b > a {
		return a, b
	}
	a = min(a, size-1)
	return a, a + 1
}

// shade covers a rectangle with alpha (0 to 1) where it is not already
// covered more.
func (c *canvas) shade(x0, y0, x1, y1 int, alpha float64) {
	v := byte(math.Round(alpha * 255))
	x0, y0 = max(x0, 0), max(y0, 0)
	x1, y1 = min(x1, c.w), min(y1, c.h)
	for y := y0; y < y1; y++ {
		row := c.pix[y*c.stride:]
		for x := x0; x < x1; x++ {
			row[x] = max(row[x], v)
		}
	}
}

// coverage is how much of a pixel a shape covers, from the signed distance
// in pixels of the pixel's centre to the shape's edge (negative inside): a
// pixel half inside is half covered, as an edge through its centre gives.
func coverage(d float64) float64 { return min(1, max(0, 0.5-d)) }

// paint adds a shape given by its signed distance function, with alpha.
func (c *canvas) paint(alpha float64, sd func(x, y float64) float64) {
	for y := 0; y < c.h; y++ {
		row := c.pix[y*c.stride:]
		for x := 0; x < c.w; x++ {
			if cv := coverage(sd(float64(x)+0.5, float64(y)+0.5)); cv > 0 {
				row[x] = max(row[x], byte(math.Round(cv*alpha*255)))
			}
		}
	}
}

// cut removes a shape given by its signed distance function.
func (c *canvas) cut(sd func(x, y float64) float64) {
	for y := 0; y < c.h; y++ {
		row := c.pix[y*c.stride:]
		for x := 0; x < c.w; x++ {
			if cv := coverage(sd(float64(x)+0.5, float64(y)+0.5)); cv > 0 {
				row[x] = byte(math.Round(float64(row[x]) * (1 - cv)))
			}
		}
	}
}

// ---- signed distance functions ----

type pt struct{ x, y float64 }

// lineDist is the distance from p to the infinite line through a and b.
func lineDist(p, a, b pt) float64 {
	ex, ey := b.x-a.x, b.y-a.y
	return math.Abs(ex*(p.y-a.y)-ey*(p.x-a.x)) / math.Hypot(ex, ey)
}

// segDist is the distance from p to the segment from a to b.
func segDist(p, a, b pt) float64 {
	ex, ey := b.x-a.x, b.y-a.y
	l2 := ex*ex + ey*ey
	if l2 == 0 {
		return math.Hypot(p.x-a.x, p.y-a.y)
	}
	t := min(1, max(0, ((p.x-a.x)*ex+(p.y-a.y)*ey)/l2))
	return math.Hypot(p.x-a.x-t*ex, p.y-a.y-t*ey)
}

// polySD is the signed distance to a convex polygon: exact inside and
// along the edges, slightly short beyond the corners.
func polySD(p pt, poly []pt) float64 {
	area := 0.0
	for i, a := range poly {
		b := poly[(i+1)%len(poly)]
		area += a.x*b.y - b.x*a.y
	}
	sign := 1.0
	if area < 0 {
		sign = -1
	}
	d := math.Inf(-1)
	for i, a := range poly {
		b := poly[(i+1)%len(poly)]
		ex, ey := b.x-a.x, b.y-a.y
		d = max(d, -sign*(ex*(p.y-a.y)-ey*(p.x-a.x))/math.Hypot(ex, ey))
	}
	return d
}

// ellipseSD is the approximate signed distance from (x, y), relative to
// the centre, to an ellipse with radii rx and ry; it is close to exact near
// the ellipse, which is all strokes and edges need.
func ellipseSD(x, y, rx, ry float64) float64 {
	k0 := math.Hypot(x/rx, y/ry)
	k1 := math.Hypot(x/(rx*rx), y/(ry*ry))
	if k1 == 0 {
		return -min(rx, ry)
	}
	return k0 * (k0 - 1) / k1
}

// arcDist is the distance from p to the arc of the circle around o with
// radius r from angle a0 to a1 (degrees, clockwise from the right, as the
// y axis points down).
func arcDist(p, o pt, r, a0, a1 float64) float64 {
	if a1-a0 >= 360 {
		return math.Abs(math.Hypot(p.x-o.x, p.y-o.y) - r)
	}
	s, e := a0*math.Pi/180, a1*math.Pi/180
	rel := math.Mod(math.Atan2(p.y-o.y, p.x-o.x)-s, 2*math.Pi)
	if rel < 0 {
		rel += 2 * math.Pi
	}
	if rel <= e-s {
		return math.Abs(math.Hypot(p.x-o.x, p.y-o.y) - r)
	}
	return min(math.Hypot(p.x-o.x-r*math.Cos(s), p.y-o.y-r*math.Sin(s)),
		math.Hypot(p.x-o.x-r*math.Cos(e), p.y-o.y-r*math.Sin(e)))
}

// ---- strokes and fills ----

// polygon fills a convex polygon.
func (c *canvas) polygon(alpha float64, poly ...pt) {
	c.paint(alpha, func(x, y float64) float64 { return polySD(pt{x, y}, poly) })
}

// disc fills a circle.
func (c *canvas) disc(o pt, r float64) {
	c.paint(1, func(x, y float64) float64 { return math.Hypot(x-o.x, y-o.y) - r })
}

// arc strokes an arc of a circle with a light line.
func (c *canvas) arc(o pt, r, a0, a1 float64) {
	half := float64(c.t) / 2
	c.paint(1, func(x, y float64) float64 { return arcDist(pt{x, y}, o, r, a0, a1) - half })
}

// line strokes the infinite line through a and b with a light line. Lines
// that run from edge to edge use it, so the stroke ends square at the
// edges, where the next cell's stroke goes on.
func (c *canvas) line(a, b pt) {
	half := float64(c.t) / 2
	c.paint(1, func(x, y float64) float64 { return lineDist(pt{x, y}, a, b) - half })
}

// polyline strokes the segments through pts with a light line, joined
// round. An end on the cell's edge is drawn as if it went on, so it ends
// square at the edge.
func (c *canvas) polyline(pts ...pt) {
	half := float64(c.t) / 2
	segs := make([][2]pt, 0, len(pts)-1)
	for i := 1; i < len(pts); i++ {
		a, b := pts[i-1], pts[i]
		if i == 1 {
			a = c.beyondEdge(a, b)
		}
		if i == len(pts)-1 {
			b = c.beyondEdge(b, a)
		}
		segs = append(segs, [2]pt{a, b})
	}
	c.paint(1, func(x, y float64) float64 {
		d := math.Inf(1)
		for _, s := range segs {
			d = min(d, segDist(pt{x, y}, s[0], s[1]))
		}
		return d - half
	})
}

// beyondEdge moves p, the end of a segment from q, a line width further
// along the segment when p is on the cell's edge.
func (c *canvas) beyondEdge(p, q pt) pt {
	if p.x > 0 && p.x < float64(c.w) && p.y > 0 && p.y < float64(c.h) {
		return p
	}
	ex, ey := p.x-q.x, p.y-q.y
	l := math.Hypot(ex, ey)
	if l == 0 {
		return p
	}
	k := float64(c.t) / l
	return pt{p.x + ex*k, p.y + ey*k}
}

// grid covers the parts of the cell split into cols×rows whose bits are
// set in mask, row by row from the top left.
func (c *canvas) grid(cols, rows int, mask uint) {
	for row := range rows {
		for col := range cols {
			if mask&(1<<(row*cols+col)) != 0 {
				c.rect(part(c.w, col, cols), part(c.h, row, rows), part(c.w, col+1, cols), part(c.h, row+1, rows))
			}
		}
	}
}

// separated is grid with a gap around each part. Each part keeps half the
// gap on each side, so the gap between parts is the same within a cell
// and across cells.
func (c *canvas) separated(cols, rows int, mask uint) {
	g := max(1, int(math.Round(float64(c.w)/8)))
	for row := range rows {
		for col := range cols {
			if mask&(1<<(row*cols+col)) != 0 {
				x0, x1 := part(c.w, col, cols), part(c.w, col+1, cols)
				y0, y1 := part(c.h, row, rows), part(c.h, row+1, rows)
				c.rect(x0+g/2, y0+g/2, x1-(g-g/2), y1-(g-g/2))
			}
		}
	}
}
