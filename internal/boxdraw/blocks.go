package boxdraw

import "math"

// Shades are flat coverage: ░ a quarter, ▒ half, ▓ three quarters. They
// tile without a pattern's seams at any cell size.
const (
	lightShade  = 0.25
	mediumShade = 0.5
	darkShade   = 0.75
)

// quadrants are the masks of ▖▗▘▙▚▛▜▝▞▟ (U+2596–U+259F) for grid(2, 2):
// 1 top left, 2 top right, 4 bottom left, 8 bottom right.
var quadrants = [10]uint{4, 8, 1, 13, 9, 7, 11, 2, 6, 14}

// octants are the masks of U+1CD00–U+1CDE5 for grid(2, 4), from their
// names (BLOCK OCTANT-1235 sets octants 1, 2, 3 and 5, numbered row by row
// from the top left). They are the patterns no other block character has.
var octants = [230]uint8{
	0x04, 0x06, 0x07, 0x08, 0x09, 0x0b, 0x0c, 0x0d, 0x0e, 0x10, 0x11, 0x12,
	0x13, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f,
	0x20, 0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x29, 0x2a, 0x2b, 0x2c,
	0x2d, 0x2e, 0x2f, 0x30, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38,
	0x39, 0x3a, 0x3b, 0x3c, 0x3d, 0x3e, 0x41, 0x42, 0x43, 0x44, 0x45, 0x46,
	0x47, 0x48, 0x49, 0x4a, 0x4b, 0x4c, 0x4d, 0x4e, 0x4f, 0x51, 0x52, 0x53,
	0x54, 0x56, 0x57, 0x58, 0x59, 0x5b, 0x5c, 0x5d, 0x5e, 0x60, 0x61, 0x62,
	0x63, 0x64, 0x65, 0x66, 0x67, 0x68, 0x69, 0x6a, 0x6b, 0x6c, 0x6d, 0x6e,
	0x6f, 0x70, 0x71, 0x72, 0x73, 0x74, 0x75, 0x76, 0x77, 0x78, 0x79, 0x7a,
	0x7b, 0x7c, 0x7d, 0x7e, 0x7f, 0x81, 0x82, 0x83, 0x84, 0x85, 0x86, 0x87,
	0x88, 0x89, 0x8a, 0x8b, 0x8c, 0x8d, 0x8e, 0x8f, 0x90, 0x91, 0x92, 0x93,
	0x94, 0x95, 0x96, 0x97, 0x98, 0x99, 0x9a, 0x9b, 0x9c, 0x9d, 0x9e, 0x9f,
	0xa1, 0xa2, 0xa3, 0xa4, 0xa6, 0xa7, 0xa8, 0xa9, 0xab, 0xac, 0xad, 0xae,
	0xb0, 0xb1, 0xb2, 0xb3, 0xb4, 0xb5, 0xb6, 0xb7, 0xb8, 0xb9, 0xba, 0xbb,
	0xbc, 0xbd, 0xbe, 0xbf, 0xc1, 0xc2, 0xc3, 0xc4, 0xc5, 0xc6, 0xc7, 0xc8,
	0xc9, 0xca, 0xcb, 0xcc, 0xcd, 0xce, 0xcf, 0xd0, 0xd1, 0xd2, 0xd3, 0xd4,
	0xd5, 0xd6, 0xd7, 0xd8, 0xd9, 0xda, 0xdb, 0xdc, 0xdd, 0xde, 0xdf, 0xe0,
	0xe1, 0xe2, 0xe3, 0xe4, 0xe5, 0xe6, 0xe7, 0xe8, 0xe9, 0xea, 0xeb, 0xec,
	0xed, 0xee, 0xef, 0xf1, 0xf2, 0xf3, 0xf4, 0xf6, 0xf7, 0xf8, 0xf9, 0xfb,
	0xfd, 0xfe,
}

// block draws the block elements U+2580–U+259F.
func (c *canvas) block(r rune) {
	switch {
	case r == 0x2580: // ▀
		c.span(0, 8, 0, 4, 8)
	case r <= 0x2588: // ▁▂▃▄▅▆▇█: the lower k eighths
		c.span(0, 8, 8-int(r-0x2580), 8, 8)
	case r <= 0x258f: // ▉▊▋▌▍▎▏: the left 7 to 1 eighths
		c.span(0, 8-int(r-0x2588), 0, 8, 8)
	case r == 0x2590: // ▐
		c.span(4, 8, 0, 8, 8)
	case r == 0x2591:
		c.shade(0, 0, c.w, c.h, lightShade)
	case r == 0x2592:
		c.shade(0, 0, c.w, c.h, mediumShade)
	case r == 0x2593:
		c.shade(0, 0, c.w, c.h, darkShade)
	case r == 0x2594: // ▔
		c.span(0, 8, 0, 1, 8)
	case r == 0x2595: // ▕
		c.span(7, 8, 0, 8, 8)
	default:
		c.grid(2, 2, quadrants[r-0x2596])
	}
}

// braille draws U+2800–U+28FF: dots 1–3 run down the left column, 4–6
// down the right, and 7 and 8 sit under them. Every dot is centred in its
// eighth of the cell, so the dots are evenly spaced across cells too.
func (c *canvas) braille(bits uint) {
	d := max(1, min(int(math.Round(float64(c.w)/4)), int(math.Round(float64(c.h)/8))))
	for i, at := range [8][2]int{{0, 0}, {0, 1}, {0, 2}, {1, 0}, {1, 1}, {1, 2}, {0, 3}, {1, 3}} {
		if bits&(1<<i) == 0 {
			continue
		}
		x0, x1 := part(c.w, at[0], 2), part(c.w, at[0]+1, 2)
		y0, y1 := part(c.h, at[1], 4), part(c.h, at[1]+1, 4)
		x, y := x0+(x1-x0-d)/2, y0+(y1-y0-d)/2
		c.rect(x, y, x+d, y+d)
	}
}

// mosaics are the smooth mosaic wedges U+1FB3C–U+1FB67: the part of the
// cell below (lower) or above the line from a to b, in fractions of the
// cell. The lines run through the sextant grid.
var mosaics = [44]struct {
	lower          bool
	ax, ay, bx, by float64
}{
	{true, 0, 2. / 3, 0.5, 1}, {true, 0, 2. / 3, 1, 1}, {true, 0, 1. / 3, 0.5, 1}, {true, 0, 1. / 3, 1, 1}, {true, 0, 0, 0.5, 1},
	{true, 0, 1. / 3, 0.5, 0}, {true, 0, 1. / 3, 1, 0}, {true, 0, 2. / 3, 0.5, 0}, {true, 0, 2. / 3, 1, 0}, {true, 0, 1, 0.5, 0}, {true, 0, 2. / 3, 1, 1. / 3},
	{true, 0.5, 1, 1, 2. / 3}, {true, 0, 1, 1, 2. / 3}, {true, 0.5, 1, 1, 1. / 3}, {true, 0, 1, 1, 1. / 3}, {true, 0.5, 1, 1, 0},
	{true, 0.5, 0, 1, 1. / 3}, {true, 0, 0, 1, 1. / 3}, {true, 0.5, 0, 1, 2. / 3}, {true, 0, 0, 1, 2. / 3}, {true, 0.5, 0, 1, 1}, {true, 0, 1. / 3, 1, 2. / 3},
	{false, 0, 2. / 3, 0.5, 1}, {false, 0, 2. / 3, 1, 1}, {false, 0, 1. / 3, 0.5, 1}, {false, 0, 1. / 3, 1, 1}, {false, 0, 0, 0.5, 1},
	{false, 0, 1. / 3, 0.5, 0}, {false, 0, 1. / 3, 1, 0}, {false, 0, 2. / 3, 0.5, 0}, {false, 0, 2. / 3, 1, 0}, {false, 0, 1, 0.5, 0},
	{false, 0, 2. / 3, 1, 1. / 3}, {false, 0.5, 1, 1, 2. / 3}, {false, 0, 1, 1, 2. / 3}, {false, 0.5, 1, 1, 1. / 3}, {false, 0, 1, 1, 1. / 3}, {false, 0.5, 1, 1, 0},
	{false, 0.5, 0, 1, 1. / 3}, {false, 0, 0, 1, 1. / 3}, {false, 0.5, 0, 1, 2. / 3}, {false, 0, 0, 1, 2. / 3}, {false, 0.5, 0, 1, 1}, {false, 0, 1. / 3, 1, 2. / 3},
}

// mosaic fills the part of the cell on one side of a line.
func (c *canvas) mosaic(i int) {
	m := mosaics[i]
	w, h := float64(c.w), float64(c.h)
	a, b := pt{m.ax * w, m.ay * h}, pt{m.bx * w, m.by * h}
	ex, ey := b.x-a.x, b.y-a.y
	l := math.Hypot(ex, ey)
	c.paint(1, func(x, y float64) float64 {
		d := (ex*(y-a.y) - ey*(x-a.x)) / l // > 0 below the line, as a is left of b
		if m.lower {
			return -d
		}
		return d
	})
}

// Edges of the cell, for the triangles that reach from one to the centre.
const (
	edgeLeft = iota
	edgeTop
	edgeRight
	edgeBottom
)

// edgeTriangle fills the triangle from an edge to the cell's centre, or
// with inverted everything else.
func (c *canvas) edgeTriangle(edge int, inverted bool) {
	w, h := float64(c.w), float64(c.h)
	m := pt{w / 2, h / 2}
	var poly []pt
	switch edge {
	case edgeLeft:
		poly = []pt{{0, 0}, m, {0, h}}
	case edgeTop:
		poly = []pt{{0, 0}, {w, 0}, m}
	case edgeRight:
		poly = []pt{{w, 0}, {w, h}, m}
	default:
		poly = []pt{{0, h}, m, {w, h}}
	}
	if inverted {
		c.rect(0, 0, c.w, c.h)
		c.cut(func(x, y float64) float64 { return polySD(pt{x, y}, poly) })
		return
	}
	c.polygon(1, poly...)
}

// Corners of the cell, for the triangles that fill half of it.
const (
	cornerTopLeft = iota
	cornerTopRight
	cornerBottomLeft
	cornerBottomRight
)

// cornerTriangle fills the half of the cell on the side of a corner of
// the diagonal that does not touch it.
func (c *canvas) cornerTriangle(corner int, alpha float64) {
	w, h := float64(c.w), float64(c.h)
	switch corner {
	case cornerTopLeft:
		c.polygon(alpha, pt{0, 0}, pt{w, 0}, pt{0, h})
	case cornerTopRight:
		c.polygon(alpha, pt{0, 0}, pt{w, 0}, pt{w, h})
	case cornerBottomLeft:
		c.polygon(alpha, pt{0, 0}, pt{w, h}, pt{0, h})
	default:
		c.polygon(alpha, pt{w, 0}, pt{w, h}, pt{0, h})
	}
}

// midLines are the diagonals between the middles of the edges of
// U+1FBA0–U+1FBAE: 1 left to top, 2 right to top, 4 left to bottom, 8
// right to bottom.
var midLines = [15]int{1, 2, 4, 8, 1 | 4, 2 | 8, 4 | 8, 1 | 2, 1 | 8, 2 | 4, 2 | 4 | 8, 1 | 4 | 8, 1 | 2 | 8, 1 | 2 | 4, 15}

// diagonals are the polylines of U+1FBD0–U+1FBDF between the corners (0
// and 2 on each axis), the middles of the edges and the centre (1).
var diagonals = [16][]struct{ x, y int }{
	{{2, 1}, {0, 2}}, {{2, 0}, {0, 1}}, {{0, 0}, {2, 1}}, {{0, 1}, {2, 2}},
	{{0, 0}, {1, 2}}, {{1, 0}, {2, 2}}, {{2, 0}, {1, 2}}, {{1, 0}, {0, 2}},
	{{0, 0}, {1, 1}, {2, 0}}, {{2, 0}, {1, 1}, {2, 2}}, {{0, 2}, {1, 1}, {2, 2}}, {{0, 0}, {1, 1}, {0, 2}},
	{{0, 0}, {1, 2}, {2, 0}}, {{2, 0}, {0, 1}, {2, 2}}, {{0, 2}, {1, 0}, {2, 2}}, {{0, 0}, {2, 1}, {0, 2}},
}

// at returns a point of the 3×3 grid of corners, edge middles and centre.
// The middles are the centres of light lines, so diagonals meet the
// straight lines of neighbouring cells.
func (c *canvas) at(x, y int) pt {
	xs := [3]float64{0, c.cx, float64(c.w)}
	ys := [3]float64{0, c.cy, float64(c.h)}
	return pt{xs[x], ys[y]}
}

// legacy draws the symbols for legacy computing, U+1FB00–U+1FBEF.
func (c *canvas) legacy(r rune) {
	w, h := float64(c.w), float64(c.h)
	switch {
	case r <= 0x1fb3b: // sextants, skipping the left and right halves
		i := uint(r - 0x1fb00)
		c.grid(2, 3, i+1+i/20)
	case r <= 0x1fb67:
		c.mosaic(int(r - 0x1fb3c))
	case r <= 0x1fb6f: // triangles from an edge, three quarters then one
		c.edgeTriangle(int(r-0x1fb68)%4, r < 0x1fb6c)
	case r <= 0x1fb75: // vertical one eighth blocks 2–7
		k := int(r - 0x1fb6f)
		c.span(k, k+1, 0, 8, 8)
	case r <= 0x1fb7b: // horizontal one eighth blocks 2–7
		k := int(r - 0x1fb75)
		c.span(0, 8, k, k+1, 8)
	case r == 0x1fb7c: // left and lower one eighth
		c.span(0, 1, 0, 8, 8)
		c.span(0, 8, 7, 8, 8)
	case r == 0x1fb7d: // left and upper
		c.span(0, 1, 0, 8, 8)
		c.span(0, 8, 0, 1, 8)
	case r == 0x1fb7e: // right and upper
		c.span(7, 8, 0, 8, 8)
		c.span(0, 8, 0, 1, 8)
	case r == 0x1fb7f: // right and lower
		c.span(7, 8, 0, 8, 8)
		c.span(0, 8, 7, 8, 8)
	case r == 0x1fb80: // upper and lower one eighth
		c.span(0, 8, 0, 1, 8)
		c.span(0, 8, 7, 8, 8)
	case r == 0x1fb81: // horizontal one eighth blocks 1, 3, 5 and 8
		for _, k := range [...]int{0, 2, 4, 7} {
			c.span(0, 8, k, k+1, 8)
		}
	case r <= 0x1fb86: // upper 2, 3, 5, 6 and 7 eighths
		c.span(0, 8, 0, [...]int{2, 3, 5, 6, 7}[r-0x1fb82], 8)
	case r <= 0x1fb8b: // right 2, 3, 5, 6 and 7 eighths
		c.span(8-[...]int{2, 3, 5, 6, 7}[r-0x1fb87], 8, 0, 8, 8)
	case r <= 0x1fb94:
		c.halfShade(r)
	case r == 0x1fb95, r == 0x1fb96: // checker board from the top left, inverse
		mask := uint(0xa5a5)
		if r == 0x1fb96 {
			mask = 0x5a5a
		}
		c.grid(4, 4, mask)
	case r == 0x1fb97: // heavy horizontal fill
		c.span(0, 8, 2, 4, 8)
		c.span(0, 8, 6, 8, 8)
	case r == 0x1fb98:
		c.stripes(false)
	case r == 0x1fb99:
		c.stripes(true)
	case r == 0x1fb9a: // upper and lower triangular half
		c.edgeTriangle(edgeTop, false)
		c.edgeTriangle(edgeBottom, false)
	case r == 0x1fb9b: // left and right triangular half
		c.edgeTriangle(edgeLeft, false)
		c.edgeTriangle(edgeRight, false)
	case r <= 0x1fb9f: // triangular medium shades
		c.cornerTriangle([...]int{cornerTopLeft, cornerTopRight, cornerBottomRight, cornerBottomLeft}[r-0x1fb9c], mediumShade)
	case r <= 0x1fbae:
		m := midLines[r-0x1fba0]
		for i, ends := range [4][2]pt{{c.at(0, 1), c.at(1, 0)}, {c.at(2, 1), c.at(1, 0)}, {c.at(0, 1), c.at(1, 2)}, {c.at(2, 1), c.at(1, 2)}} {
			if m&(1<<i) != 0 {
				c.line(ends[0], ends[1])
			}
		}
	case r == 0x1fbce: // left two thirds
		c.span(0, 2, 0, 3, 3)
	case r == 0x1fbcf: // left one third
		c.span(0, 1, 0, 3, 3)
	case r <= 0x1fbdf:
		var pts []pt
		for _, p := range diagonals[r-0x1fbd0] {
			pts = append(pts, c.at(p.x, p.y))
		}
		c.polyline(pts...)
	case r <= 0x1fbe3: // justified half white circles: top, right, bottom, left
		half := float64(c.t) / 2
		rad := w/2 - half
		switch r {
		case 0x1fbe0:
			c.arc(pt{w / 2, half}, rad, 0, 180)
		case 0x1fbe1:
			c.arc(pt{w, h / 2}, rad, 90, 270)
		case 0x1fbe2:
			c.arc(pt{w / 2, h - half}, rad, 180, 360)
		default:
			c.arc(pt{0, h / 2}, rad, 270, 450)
		}
	case r == 0x1fbe4: // upper centre one quarter
		c.span(2, 6, 0, 4, 8)
	case r == 0x1fbe5: // lower centre one quarter
		c.span(2, 6, 4, 8, 8)
	case r == 0x1fbe6: // middle left one quarter
		c.span(0, 4, 2, 6, 8)
	case r == 0x1fbe7: // middle right one quarter
		c.span(4, 8, 2, 6, 8)
	case r <= 0x1fbeb: // justified half black circles: top, right, bottom, left
		c.disc([...]pt{{w / 2, 0}, {w, h / 2}, {w / 2, h}, {0, h / 2}}[r-0x1fbe8], w/2)
	default: // justified quarter black circles in the corner they name
		c.disc([...]pt{{w, 0}, {0, h}, {w, h}, {0, 0}}[r-0x1fbec], w/2)
	}
}

// halfShade draws U+1FB8C–U+1FB94: a medium shade over half the cell,
// over all of it, or over half with the other half a block.
func (c *canvas) halfShade(r rune) {
	mx, my := part(c.w, 1, 2), part(c.h, 1, 2)
	switch r {
	case 0x1fb8c: // left half
		c.shade(0, 0, mx, c.h, mediumShade)
	case 0x1fb8d: // right half
		c.shade(mx, 0, c.w, c.h, mediumShade)
	case 0x1fb8e: // upper half
		c.shade(0, 0, c.w, my, mediumShade)
	case 0x1fb8f: // lower half
		c.shade(0, my, c.w, c.h, mediumShade)
	case 0x1fb90: // inverse medium shade
		c.shade(0, 0, c.w, c.h, mediumShade)
	case 0x1fb91: // upper half block, lower half inverse medium shade
		c.rect(0, 0, c.w, my)
		c.shade(0, my, c.w, c.h, mediumShade)
	case 0x1fb92: // upper half inverse medium shade, lower half block
		c.shade(0, 0, c.w, my, mediumShade)
		c.rect(0, my, c.w, c.h)
	case 0x1fb93: // left half block, right half inverse medium shade (unassigned; kitty draws it)
		c.rect(0, 0, mx, c.h)
		c.shade(mx, 0, c.w, c.h, mediumShade)
	default: // left half inverse medium shade, right half block
		c.shade(0, 0, mx, c.h, mediumShade)
		c.rect(mx, 0, c.w, c.h)
	}
}

// stripes fills four diagonal stripes per cell, parallel to the diagonal
// from the top left (or with rising, from the bottom left). A stripe
// leaves the cell where the next cell's stripe goes on.
func (c *canvas) stripes(rising bool) {
	const n = 4
	w, h := float64(c.w), float64(c.h)
	sy := -1.0
	if rising {
		sy = 1
	}
	grad := n * math.Hypot(1/w, 1/h) // stripes per pixel across them
	c.paint(1, func(x, y float64) float64 {
		u := n * (x/w + sy*y/h)
		f := u - math.Floor(u)
		if f < 0.5 {
			return -min(f, 0.5-f) / grad
		}
		return min(f-0.5, 1-f) / grad
	})
}

// supplementBlocks draws U+1CE16–U+1CE19 and U+1CE51–U+1CEAF of the
// legacy computing supplement.
func (c *canvas) supplementBlocks(r rune) {
	sx0, sx1 := c.vband(c.t)
	switch {
	case r <= 0x1ce19: // vertical and a half line at a quarter or three quarters
		c.lines(arms{u: light, d: light})
		at := part(c.h, 1, 4)
		if r&1 != 0 {
			at = part(c.h, 3, 4)
		}
		if r <= 0x1ce17 {
			c.hlineAt(sx0, c.w, at)
		} else {
			c.hlineAt(0, sx1, at)
		}
	case r <= 0x1ce8f:
		c.separated(2, 3, uint(r-0x1ce51+1))
	case r <= 0x1ce9f: // one sixteenth blocks, row by row
		c.grid(4, 4, 1<<(r-0x1ce90))
	default: // one quarter strips cut to a half or three quarters
		q := quarters[r-0x1cea0]
		c.span(q[0], q[1], q[2], q[3], 4)
	}
}

// quarters are U+1CEA0–U+1CEAF as x0, x1, y0, y1 in quarters of the cell.
var quarters = [16][4]int{
	{2, 4, 3, 4}, {1, 4, 3, 4}, {0, 3, 3, 4}, {0, 2, 3, 4}, // lower strip
	{0, 1, 2, 4}, {0, 1, 1, 4}, {0, 1, 0, 3}, {0, 1, 0, 2}, // left strip
	{0, 2, 0, 1}, {0, 3, 0, 1}, {1, 4, 0, 1}, {2, 4, 0, 1}, // upper strip
	{3, 4, 0, 2}, {3, 4, 0, 3}, {3, 4, 1, 4}, {3, 4, 2, 4}, // right strip
}
