package boxdraw

import "math"

// geometric draws the circles, arcs and corner triangles of U+25C9–U+25E5
// that kitty draws.
func (c *canvas) geometric(r rune) {
	o := pt{float64(c.w) / 2, float64(c.h) / 2}
	rad := min(o.x, o.y)
	switch r {
	case 0x25c9: // ◉
		inner := rad * 2 / 3
		c.disc(o, inner)
		lw := max(1, (rad-inner)/2.5)
		c.paint(1, func(x, y float64) float64 {
			return math.Abs(math.Hypot(x-o.x, y-o.y)-(rad-lw/2)) - lw/2
		})
	case 0x25cb: // ○
		c.spinner(0, 360)
	case 0x25cf: // ●
		c.disc(o, rad)
	case 0x25d6: // ◖
		c.halfEllipse(false, true)
	case 0x25d7: // ◗
		c.halfEllipse(true, true)
	case 0x25dc: // ◜
		c.spinner(180, 270)
	case 0x25dd: // ◝
		c.spinner(270, 360)
	case 0x25de: // ◞
		c.spinner(0, 90)
	case 0x25df: // ◟
		c.spinner(90, 180)
	case 0x25e0: // ◠
		c.spinner(180, 360)
	case 0x25e1: // ◡
		c.spinner(0, 180)
	case 0x25e2: // ◢
		c.cornerTriangle(cornerBottomRight, 1)
	case 0x25e3: // ◣
		c.cornerTriangle(cornerBottomLeft, 1)
	case 0x25e4: // ◤
		c.cornerTriangle(cornerTopLeft, 1)
	case 0x25e5: // ◥
		c.cornerTriangle(cornerTopRight, 1)
	}
}

// spinner strokes an arc of the circle centred in the cell, as wide as
// the cell, from angle a0 to a1 (degrees clockwise from the right).
func (c *canvas) spinner(a0, a1 float64) {
	o := pt{float64(c.w) / 2, float64(c.h) / 2}
	c.arc(o, max(0.5, min(o.x, o.y)-float64(c.t)/2), a0, a1)
}

// halfEllipse fills (or with outline, strokes) the half ellipse that
// spans the cell from its left edge (right: the right half of the
// ellipse) or from its right edge.
func (c *canvas) halfEllipse(right, filled bool) {
	w, h := float64(c.w), float64(c.h)
	ox := 0.0
	if !right {
		ox = w
	}
	oy := h / 2
	if filled {
		c.paint(1, func(x, y float64) float64 { return ellipseSD(x-ox, y-oy, w, h/2) })
		return
	}
	half := float64(c.t) / 2
	c.paint(1, func(x, y float64) float64 { return math.Abs(ellipseSD(x-ox, y-oy, w-half, h/2-half)) - half })
}

// powerline draws the powerline separators U+E0B0–U+E0BF, U+E0D6 and
// U+E0D7.
func (c *canvas) powerline(r rune) {
	w, h := float64(c.w), float64(c.h)
	right := []pt{{0, 0}, {w, h / 2}, {0, h}} // the triangle pointing right
	left := []pt{{w, 0}, {0, h / 2}, {w, h}}
	switch r {
	case 0xe0b0:
		c.polygon(1, right...)
	case 0xe0b1:
		c.polyline(right...)
	case 0xe0b2:
		c.polygon(1, left...)
	case 0xe0b3:
		c.polyline(left...)
	case 0xe0b4:
		c.halfEllipse(true, true)
	case 0xe0b5:
		c.halfEllipse(true, false)
	case 0xe0b6:
		c.halfEllipse(false, true)
	case 0xe0b7:
		c.halfEllipse(false, false)
	case 0xe0b8:
		c.cornerTriangle(cornerBottomLeft, 1)
	case 0xe0ba:
		c.cornerTriangle(cornerBottomRight, 1)
	case 0xe0bc:
		c.cornerTriangle(cornerTopLeft, 1)
	case 0xe0be:
		c.cornerTriangle(cornerTopRight, 1)
	case 0xe0b9, 0xe0bf: // ╲
		c.line(pt{0, 0}, pt{w, h})
	case 0xe0bb, 0xe0bd: // ╱
		c.line(pt{w, 0}, pt{0, h})
	case 0xe0d6, 0xe0d7: // the cell without the triangle pointing left, right
		tri := left
		if r == 0xe0d7 {
			tri = right
		}
		c.rect(0, 0, c.w, c.h)
		c.cut(func(x, y float64) float64 { return polySD(pt{x, y}, tri) })
	}
}

// spinnerArcs are the arcs of Fira Code's spinner, U+EE06–U+EE0B.
var spinnerArcs = [6][2]float64{{235, 305}, {270, 390}, {315, 470}, {360, 540}, {80, 220}, {170, 270}}

// progress draws Fira Code's progress bar, U+EE00–U+EE05 (left, middle
// and right part, empty then filled), and its spinner, U+EE06–U+EE0B. The
// bar's frame runs along the cell's edges, so the parts join; its filling
// keeps a gap of two lines inside the frame.
func (c *canvas) progress(r rune) {
	i := int(r - 0xee00)
	if i >= 6 {
		a := spinnerArcs[i-6]
		c.spinner(a[0], a[1])
		return
	}
	t, seg := c.t, i%3
	c.rect(0, 0, c.w, t)
	c.rect(0, c.h-t, c.w, c.h)
	x0, x1 := 0, c.w
	switch seg {
	case 0:
		c.rect(0, 0, t, c.h)
		x0 = 3 * t
	case 2:
		c.rect(c.w-t, 0, c.w, c.h)
		x1 = c.w - 3*t
	}
	if i >= 3 {
		c.rect(x0, 3*t, x1, c.h-3*t)
	}
}
