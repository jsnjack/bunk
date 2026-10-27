package boxdraw

import "math"

// weight is the style of a line.
type weight uint8

const (
	none weight = iota
	light
	heavy
	double
)

// arms are the lines from a cell's centre to its left, right, top and
// bottom edges.
type arms struct{ l, r, u, d weight }

// lineArms holds the box drawing characters U+2500–U+257F that are
// straight lines from the centre to the edges; the dashes, arcs and
// diagonals between them are drawn on their own.
var lineArms = [0x80]arms{
	0x00: {l: light, r: light},                         // ─ light horizontal
	0x01: {l: heavy, r: heavy},                         // ━ heavy horizontal
	0x02: {u: light, d: light},                         // │ light vertical
	0x03: {u: heavy, d: heavy},                         // ┃ heavy vertical
	0x0c: {r: light, d: light},                         // ┌ light down and right
	0x0d: {r: heavy, d: light},                         // ┍ down light and right heavy
	0x0e: {r: light, d: heavy},                         // ┎ down heavy and right light
	0x0f: {r: heavy, d: heavy},                         // ┏ heavy down and right
	0x10: {l: light, d: light},                         // ┐ light down and left
	0x11: {l: heavy, d: light},                         // ┑ down light and left heavy
	0x12: {l: light, d: heavy},                         // ┒ down heavy and left light
	0x13: {l: heavy, d: heavy},                         // ┓ heavy down and left
	0x14: {r: light, u: light},                         // └ light up and right
	0x15: {r: heavy, u: light},                         // ┕ up light and right heavy
	0x16: {r: light, u: heavy},                         // ┖ up heavy and right light
	0x17: {r: heavy, u: heavy},                         // ┗ heavy up and right
	0x18: {l: light, u: light},                         // ┘ light up and left
	0x19: {l: heavy, u: light},                         // ┙ up light and left heavy
	0x1a: {l: light, u: heavy},                         // ┚ up heavy and left light
	0x1b: {l: heavy, u: heavy},                         // ┛ heavy up and left
	0x1c: {r: light, u: light, d: light},               // ├ light vertical and right
	0x1d: {r: heavy, u: light, d: light},               // ┝ vertical light and right heavy
	0x1e: {r: light, u: heavy, d: light},               // ┞ up heavy and right down light
	0x1f: {r: light, u: light, d: heavy},               // ┟ down heavy and right up light
	0x20: {r: light, u: heavy, d: heavy},               // ┠ vertical heavy and right light
	0x21: {r: heavy, u: heavy, d: light},               // ┡ down light and right up heavy
	0x22: {r: heavy, u: light, d: heavy},               // ┢ up light and right down heavy
	0x23: {r: heavy, u: heavy, d: heavy},               // ┣ heavy vertical and right
	0x24: {l: light, u: light, d: light},               // ┤ light vertical and left
	0x25: {l: heavy, u: light, d: light},               // ┥ vertical light and left heavy
	0x26: {l: light, u: heavy, d: light},               // ┦ up heavy and left down light
	0x27: {l: light, u: light, d: heavy},               // ┧ down heavy and left up light
	0x28: {l: light, u: heavy, d: heavy},               // ┨ vertical heavy and left light
	0x29: {l: heavy, u: heavy, d: light},               // ┩ down light and left up heavy
	0x2a: {l: heavy, u: light, d: heavy},               // ┪ up light and left down heavy
	0x2b: {l: heavy, u: heavy, d: heavy},               // ┫ heavy vertical and left
	0x2c: {l: light, r: light, d: light},               // ┬ light down and horizontal
	0x2d: {l: heavy, r: light, d: light},               // ┭ left heavy and right down light
	0x2e: {l: light, r: heavy, d: light},               // ┮ right heavy and left down light
	0x2f: {l: heavy, r: heavy, d: light},               // ┯ down light and horizontal heavy
	0x30: {l: light, r: light, d: heavy},               // ┰ down heavy and horizontal light
	0x31: {l: heavy, r: light, d: heavy},               // ┱ right light and left down heavy
	0x32: {l: light, r: heavy, d: heavy},               // ┲ left light and right down heavy
	0x33: {l: heavy, r: heavy, d: heavy},               // ┳ heavy down and horizontal
	0x34: {l: light, r: light, u: light},               // ┴ light up and horizontal
	0x35: {l: heavy, r: light, u: light},               // ┵ left heavy and right up light
	0x36: {l: light, r: heavy, u: light},               // ┶ right heavy and left up light
	0x37: {l: heavy, r: heavy, u: light},               // ┷ up light and horizontal heavy
	0x38: {l: light, r: light, u: heavy},               // ┸ up heavy and horizontal light
	0x39: {l: heavy, r: light, u: heavy},               // ┹ right light and left up heavy
	0x3a: {l: light, r: heavy, u: heavy},               // ┺ left light and right up heavy
	0x3b: {l: heavy, r: heavy, u: heavy},               // ┻ heavy up and horizontal
	0x3c: {l: light, r: light, u: light, d: light},     // ┼ light vertical and horizontal
	0x3d: {l: heavy, r: light, u: light, d: light},     // ┽ left heavy and right vertical light
	0x3e: {l: light, r: heavy, u: light, d: light},     // ┾ right heavy and left vertical light
	0x3f: {l: heavy, r: heavy, u: light, d: light},     // ┿ vertical light and horizontal heavy
	0x40: {l: light, r: light, u: heavy, d: light},     // ╀ up heavy and down horizontal light
	0x41: {l: light, r: light, u: light, d: heavy},     // ╁ down heavy and up horizontal light
	0x42: {l: light, r: light, u: heavy, d: heavy},     // ╂ vertical heavy and horizontal light
	0x43: {l: heavy, r: light, u: heavy, d: light},     // ╃ left up heavy and right down light
	0x44: {l: light, r: heavy, u: heavy, d: light},     // ╄ right up heavy and left down light
	0x45: {l: heavy, r: light, u: light, d: heavy},     // ╅ left down heavy and right up light
	0x46: {l: light, r: heavy, u: light, d: heavy},     // ╆ right down heavy and left up light
	0x47: {l: heavy, r: heavy, u: heavy, d: light},     // ╇ down light and up horizontal heavy
	0x48: {l: heavy, r: heavy, u: light, d: heavy},     // ╈ up light and down horizontal heavy
	0x49: {l: heavy, r: light, u: heavy, d: heavy},     // ╉ right light and left vertical heavy
	0x4a: {l: light, r: heavy, u: heavy, d: heavy},     // ╊ left light and right vertical heavy
	0x4b: {l: heavy, r: heavy, u: heavy, d: heavy},     // ╋ heavy vertical and horizontal
	0x50: {l: double, r: double},                       // ═ double horizontal
	0x51: {u: double, d: double},                       // ║ double vertical
	0x52: {r: double, d: light},                        // ╒ down single and right double
	0x53: {r: light, d: double},                        // ╓ down double and right single
	0x54: {r: double, d: double},                       // ╔ double down and right
	0x55: {l: double, d: light},                        // ╕ down single and left double
	0x56: {l: light, d: double},                        // ╖ down double and left single
	0x57: {l: double, d: double},                       // ╗ double down and left
	0x58: {r: double, u: light},                        // ╘ up single and right double
	0x59: {r: light, u: double},                        // ╙ up double and right single
	0x5a: {r: double, u: double},                       // ╚ double up and right
	0x5b: {l: double, u: light},                        // ╛ up single and left double
	0x5c: {l: light, u: double},                        // ╜ up double and left single
	0x5d: {l: double, u: double},                       // ╝ double up and left
	0x5e: {r: double, u: light, d: light},              // ╞ vertical single and right double
	0x5f: {r: light, u: double, d: double},             // ╟ vertical double and right single
	0x60: {r: double, u: double, d: double},            // ╠ double vertical and right
	0x61: {l: double, u: light, d: light},              // ╡ vertical single and left double
	0x62: {l: light, u: double, d: double},             // ╢ vertical double and left single
	0x63: {l: double, u: double, d: double},            // ╣ double vertical and left
	0x64: {l: double, r: double, d: light},             // ╤ down single and horizontal double
	0x65: {l: light, r: light, d: double},              // ╥ down double and horizontal single
	0x66: {l: double, r: double, d: double},            // ╦ double down and horizontal
	0x67: {l: double, r: double, u: light},             // ╧ up single and horizontal double
	0x68: {l: light, r: light, u: double},              // ╨ up double and horizontal single
	0x69: {l: double, r: double, u: double},            // ╩ double up and horizontal
	0x6a: {l: double, r: double, u: light, d: light},   // ╪ vertical single and horizontal double
	0x6b: {l: light, r: light, u: double, d: double},   // ╫ vertical double and horizontal single
	0x6c: {l: double, r: double, u: double, d: double}, // ╬ double vertical and horizontal
	0x74: {l: light},                                   // ╴ light left
	0x75: {u: light},                                   // ╵ light up
	0x76: {r: light},                                   // ╶ light right
	0x77: {d: light},                                   // ╷ light down
	0x78: {l: heavy},                                   // ╸ heavy left
	0x79: {u: heavy},                                   // ╹ heavy up
	0x7a: {r: heavy},                                   // ╺ heavy right
	0x7b: {d: heavy},                                   // ╻ heavy down
	0x7c: {l: light, r: heavy},                         // ╼ light left and heavy right
	0x7d: {u: light, d: heavy},                         // ╽ light up and heavy down
	0x7e: {l: heavy, r: light},                         // ╾ heavy left and light right
	0x7f: {u: heavy, d: light},                         // ╿ heavy up and light down
}

// Rounded corners, named by the box corner they make: arcDR (╭) turns
// from the right arm down, arcUL (╯) from the top arm left.
const (
	arcDR = 1 << iota // ╭
	arcDL             // ╮
	arcUR             // ╰
	arcUL             // ╯
	lineH             // ─ across the cell
	lineV             // │ across the cell
)

func (c *canvas) boxDrawing(r rune) {
	i := r - 0x2500
	switch {
	case r >= 0x2504 && r <= 0x250b: // ┄┅┆┇ triple, ┈┉┊┋ quadruple
		c.dashes(3+int(i-4)/4, light+weight(i&1), i&2 != 0)
	case r >= 0x254c && r <= 0x254f: // ╌╍╎╏
		c.dashes(2, light+weight(i&1), i&2 != 0)
	case r == 0x256d:
		c.curves(arcDR)
	case r == 0x256e:
		c.curves(arcDL)
	case r == 0x256f:
		c.curves(arcUL)
	case r == 0x2570:
		c.curves(arcUR)
	case r == 0x2571: // ╱
		c.line(pt{float64(c.w), 0}, pt{0, float64(c.h)})
	case r == 0x2572: // ╲
		c.line(pt{0, 0}, pt{float64(c.w), float64(c.h)})
	case r == 0x2573: // ╳
		c.line(pt{float64(c.w), 0}, pt{0, float64(c.h)})
		c.line(pt{0, 0}, pt{float64(c.w), float64(c.h)})
	default:
		c.lines(lineArms[i])
	}
}

func (c *canvas) width(wt weight) int {
	switch wt {
	case light:
		return c.t
	case heavy:
		return c.heavy
	case double:
		return 3 * c.t
	}
	return 0
}

// lines draws the arms of a box drawing character. An arm reaches across
// the lines crossing it, so corners are square and junctions have no
// notch.
func (c *canvas) lines(a arms) {
	if a.l == double || a.r == double || a.u == double || a.d == double {
		c.doubleLines(a)
		return
	}
	xl, xr := c.w/2, c.w/2 // where the left arm ends, the right one starts
	if tv := max(c.width(a.u), c.width(a.d)); tv > 0 {
		xr, xl = c.vband(tv)
	}
	yu, yd := c.h/2, c.h/2
	if th := max(c.width(a.l), c.width(a.r)); th > 0 {
		yd, yu = c.hband(th)
	}
	if a.l != none {
		y0, y1 := c.hband(c.width(a.l))
		c.rect(0, y0, xl, y1)
	}
	if a.r != none {
		y0, y1 := c.hband(c.width(a.r))
		c.rect(xr, y0, c.w, y1)
	}
	if a.u != none {
		x0, x1 := c.vband(c.width(a.u))
		c.rect(x0, 0, x1, yu)
	}
	if a.d != none {
		x0, x1 := c.vband(c.width(a.d))
		c.rect(x0, yd, x1, c.h)
	}
}

// doubleLines draws arms of which some are double. A double arm is a band
// three light lines wide with its middle cleared: the channel. Where two
// double arms meet, the channels join and the outer lines turn the corner.
// A single line that crosses the double ones goes straight through; one
// that ends at them stops at the nearer line, or at the far one when the
// double lines end there too (╓, ╒).
func (c *canvas) doubleLines(a arms) {
	t := c.t
	sx0, sx1 := c.vband(t)
	sy0, sy1 := c.hband(t)
	dx0, dy0 := (c.w-3*t)/2, (c.h-3*t)/2
	hd, vd := a.l == double || a.r == double, a.u == double || a.d == double
	hs, vs := a.l == light || a.r == light, a.u == light || a.d == light

	type box struct{ x0, y0, x1, y1 int }
	var channels []box
	if a.l == double {
		end, gap := c.w/2, c.w/2
		switch {
		case vd:
			end, gap = dx0+3*t, dx0+2*t
		case vs:
			end, gap = sx1, sx1
		}
		c.rect(0, dy0, end, dy0+3*t)
		channels = append(channels, box{0, dy0 + t, gap, dy0 + 2*t})
	}
	if a.r == double {
		start, gap := c.w/2, c.w/2
		switch {
		case vd:
			start, gap = dx0, dx0+t
		case vs:
			start, gap = sx0, sx0
		}
		c.rect(start, dy0, c.w, dy0+3*t)
		channels = append(channels, box{gap, dy0 + t, c.w, dy0 + 2*t})
	}
	if a.u == double {
		end, gap := c.h/2, c.h/2
		switch {
		case hd:
			end, gap = dy0+3*t, dy0+2*t
		case hs:
			end, gap = sy1, sy1
		}
		c.rect(dx0, 0, dx0+3*t, end)
		channels = append(channels, box{dx0 + t, 0, dx0 + 2*t, gap})
	}
	if a.d == double {
		start, gap := c.h/2, c.h/2
		switch {
		case hd:
			start, gap = dy0, dy0+t
		case hs:
			start, gap = sy0, sy0
		}
		c.rect(dx0, start, dx0+3*t, c.h)
		channels = append(channels, box{dx0 + t, gap, dx0 + 2*t, c.h})
	}
	for _, b := range channels {
		c.fill(b.x0, b.y0, b.x1, b.y1, 0)
	}

	switch {
	case a.l == light && a.r == light:
		c.rect(0, sy0, c.w, sy1)
	case a.u == double && a.d == double: // ends at the nearer line
		if a.l == light {
			c.rect(0, sy0, dx0+t, sy1)
		}
		if a.r == light {
			c.rect(dx0+2*t, sy0, c.w, sy1)
		}
	default: // caps the double lines
		if a.l == light {
			c.rect(0, sy0, dx0+3*t, sy1)
		}
		if a.r == light {
			c.rect(dx0, sy0, c.w, sy1)
		}
	}
	switch {
	case a.u == light && a.d == light:
		c.rect(sx0, 0, sx1, c.h)
	case a.l == double && a.r == double:
		if a.u == light {
			c.rect(sx0, 0, sx1, dy0+t)
		}
		if a.d == light {
			c.rect(sx0, dy0+2*t, sx1, c.h)
		}
	default:
		if a.u == light {
			c.rect(sx0, 0, sx1, dy0+3*t)
		}
		if a.d == light {
			c.rect(sx0, dy0, sx1, c.h)
		}
	}
}

// dashes draws a line broken into n dashes. Each dash keeps half of its
// gap on each side, so the gaps are even across cells too.
func (c *canvas) dashes(n int, wt weight, vertical bool) {
	size := c.w
	if vertical {
		size = c.h
	}
	gap := max(1, int(math.Round(float64(size)/float64(n)*0.3)))
	th := c.width(wt)
	for i := range n {
		a, b := part(size, i, n), part(size, i+1, n)
		g := max(0, min(gap, b-a-1))
		a, b = a+g/2, b-(g-g/2)
		if vertical {
			x0, x1 := c.vband(th)
			c.rect(x0, a, x1, b)
		} else {
			y0, y1 := c.hband(th)
			c.rect(a, y0, b, y1)
		}
	}
}

// curves draws the rounded corners in mask with ─ or │ across the cell,
// for ╭╮╯╰ and the branch drawing symbols.
func (c *canvas) curves(mask int) {
	if mask&lineH != 0 {
		c.lines(arms{l: light, r: light})
	}
	if mask&lineV != 0 {
		c.lines(arms{u: light, d: light})
	}
	for _, k := range [...]struct {
		bit    int
		hx, vy float64
	}{{arcDR, 1, 1}, {arcDL, -1, 1}, {arcUR, 1, -1}, {arcUL, -1, -1}} {
		if mask&k.bit != 0 {
			c.roundedCorner(k.hx, k.vy)
		}
	}
}

// roundedCorner draws a corner whose arms leave towards hx (1 right, -1
// left) and vy (1 down, -1 up) and turn along a quarter circle. Its arms
// are as wide as light lines and sit on their centres, and the radius
// leaves a straight pixel at both edges, so the arms meet the next cell's
// lines exactly.
func (c *canvas) roundedCorner(hx, vy float64) {
	w, h := float64(c.w), float64(c.h)
	r := max(0, min(c.cx, w-c.cx, c.cy, h-c.cy)-1)
	ox, oy := c.cx+hx*r, c.cy+vy*r // centre of the arc
	half := float64(c.t) / 2
	c.paint(1, func(x, y float64) float64 {
		dh := math.Abs(y - c.cy) // the horizontal arm, from the arc to the edge
		if hx*(x-ox) < 0 {
			dh = math.Hypot(x-ox, y-c.cy)
		}
		dv := math.Abs(x - c.cx)
		if vy*(y-oy) < 0 {
			dv = math.Hypot(x-c.cx, y-oy)
		}
		d := min(dh, dv)
		if hx*(x-ox) <= 0 && vy*(y-oy) <= 0 {
			d = min(d, math.Abs(math.Hypot(x-ox, y-oy)-r))
		}
		return d - half
	})
}

// ---- branch drawing symbols, U+F5D0–U+F60D ----

// branchCurves are the rounded branches of U+F5D6–U+F5ED.
var branchCurves = [...]int{
	arcDR, arcDL, arcUR, arcUL, // ╭ ╮ ╰ ╯
	lineV | arcUR, lineV | arcDR, arcUR | arcDR, lineV | arcUL, lineV | arcDL, arcDL | arcUL,
	lineH | arcDL, lineH | arcDR, arcDR | arcDL, lineH | arcUL, lineH | arcUR, arcUR | arcUL,
	lineV | arcUR | arcUL, lineV | arcDR | arcDL, lineH | arcDL | arcUL, lineH | arcUR | arcDR,
	lineV | arcDR | arcUL, lineV | arcDL | arcUR, lineH | arcDR | arcUL, lineH | arcDL | arcUR,
}

// commitLines are the lines from a commit's circle, U+F5EE–U+F60D in
// pairs: a filled circle, then a hollow one.
var commitLines = [16]arms{
	{}, {r: light}, {l: light}, {l: light, r: light},
	{d: light}, {u: light}, {u: light, d: light}, {r: light, d: light},
	{l: light, d: light}, {r: light, u: light}, {l: light, u: light}, {r: light, u: light, d: light},
	{l: light, u: light, d: light}, {l: light, r: light, d: light}, {l: light, r: light, u: light},
	{l: light, r: light, u: light, d: light},
}

func (c *canvas) branch(r rune) {
	switch {
	case r == 0xf5d0:
		c.lines(arms{l: light, r: light})
	case r == 0xf5d1:
		c.lines(arms{u: light, d: light})
	case r == 0xf5d2:
		c.fading(4, false, false)
	case r == 0xf5d3:
		c.fading(4, false, true)
	case r == 0xf5d4:
		c.fading(5, true, false)
	case r == 0xf5d5:
		c.fading(5, true, true)
	case r <= 0xf5ed:
		c.curves(branchCurves[r-0xf5d6])
	default:
		i := r - 0xf5ee
		c.commit(commitLines[i/2], i%2 == 0)
	}
}

// fading draws a line of n dashes that shorten towards one end: the right
// or bottom end, or with reverse the left or top one. The full end starts
// at the edge, where a line in the next cell goes on.
func (c *canvas) fading(n int, vertical, reverse bool) {
	size := c.w
	if vertical {
		size = c.h
	}
	for i := range n {
		a := part(size, i, n)
		l := max(1, (part(size, i+1, n)-a)*(n-i)/(n+1))
		b := a + l
		if reverse {
			a, b = size-b, size-a
		}
		if vertical {
			x0, x1 := c.vband(c.t)
			c.rect(x0, a, x1, b)
		} else {
			y0, y1 := c.hband(c.t)
			c.rect(a, y0, b, y1)
		}
	}
}

// commit draws a commit of a git graph: a circle on the lines a, filled
// or hollow. The circle stays a pixel inside the cell, so it never
// touches the next cell's.
func (c *canvas) commit(a arms, solid bool) {
	c.lines(a)
	o := pt{c.cx, c.cy}
	r := min(0.9*min(float64(c.w), float64(c.h))/2, min(c.cx, float64(c.w)-c.cx, c.cy, float64(c.h)-c.cy)-1)
	if solid {
		c.disc(o, r)
		return
	}
	inner := r - float64(c.t)
	c.cut(func(x, y float64) float64 { return math.Hypot(x-o.x, y-o.y) - inner })
	c.paint(1, func(x, y float64) float64 {
		return math.Abs(math.Hypot(x-o.x, y-o.y)-(r+inner)/2) - float64(c.t)/2
	})
}

// ---- legacy computing supplement lines, U+1CC1B–U+1CC3F ----

// hlineAt and vlineAt draw a light line centred on the pixel boundary at.
func (c *canvas) hlineAt(x0, x1, at int) { c.rect(x0, at-c.t/2, x1, at-c.t/2+c.t) }
func (c *canvas) vlineAt(y0, y1, at int) { c.rect(at-c.t/2, y0, at-c.t/2+c.t, y1) }

func (c *canvas) supplementLines(r rune) {
	sx0, sx1 := c.vband(c.t)
	sy0, sy1 := c.hband(c.t)
	q1, q3 := part(c.h, 1, 4), part(c.h, 3, 4)
	switch {
	case r == 0x1cc1b: // horizontal and upper right
		c.lines(arms{l: light, r: light})
		c.vlineAt(0, sy1, part(c.w, 3, 4))
	case r == 0x1cc1c: // horizontal and lower right
		c.lines(arms{l: light, r: light})
		c.vlineAt(sy0, c.h, part(c.w, 3, 4))
	case r == 0x1cc1d: // top and upper left
		c.hlineAt(0, sx1, q1)
		c.rect(sx0, 0, sx1, q1-c.t/2+c.t)
	case r == 0x1cc1e: // bottom and lower left
		c.hlineAt(0, sx1, q3)
		c.rect(sx0, q3-c.t/2, sx1, c.h)
	case r == 0x1cc1f: // double diagonal upper right to lower left
		c.doubleDiagonal(pt{float64(c.w), 0}, pt{0, float64(c.h)})
	case r == 0x1cc20: // double diagonal upper left to lower right
		c.doubleDiagonal(pt{0, 0}, pt{float64(c.w), float64(c.h)})
	case r <= 0x1cc2f:
		c.separated(2, 2, uint(r-0x1cc21+1))
	default:
		c.twelfth(int(r - 0x1cc30))
	}
}

// doubleDiagonal draws two light lines parallel to the line through a and
// b, a light line's width apart.
func (c *canvas) doubleDiagonal(a, b pt) {
	ex, ey := b.x-a.x, b.y-a.y
	l := math.Hypot(ex, ey)
	nx, ny := -ey/l*float64(c.t), ex/l*float64(c.t)
	c.line(pt{a.x + nx, a.y + ny}, pt{b.x + nx, b.y + ny})
	c.line(pt{a.x - nx, a.y - ny}, pt{b.x - nx, b.y - ny})
}

// twelfths place the arcs of U+1CC30–U+1CC3F: an ellipse inscribed in n×n
// cells, of which this cell is at col, row. Twelfths split a 4×4 circle,
// quarters a 2×2 one.
var twelfths = [16]struct{ n, col, row int }{
	{4, 0, 0}, {4, 1, 0}, {4, 2, 0}, {4, 3, 0},
	{4, 0, 1}, {2, 0, 0}, {2, 1, 0}, {4, 3, 1},
	{4, 0, 2}, {2, 0, 1}, {2, 1, 1}, {4, 3, 2},
	{4, 0, 3}, {4, 1, 3}, {4, 2, 3}, {4, 3, 3},
}

// twelfth draws this cell's part of the ellipse. Every cell strokes the
// same ellipse, so the parts join.
func (c *canvas) twelfth(i int) {
	p := twelfths[i]
	w, h := float64(c.w), float64(c.h)
	half := float64(c.t) / 2
	n := float64(p.n) / 2
	ox, oy := n*w-float64(p.col)*w, n*h-float64(p.row)*h
	rx, ry := n*w-half, n*h-half
	c.paint(1, func(x, y float64) float64 { return math.Abs(ellipseSD(x-ox, y-oy, rx, ry)) - half })
}
