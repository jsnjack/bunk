package boxdraw

import (
	"fmt"
	"math"
	"testing"
)

// sizes are cell sizes in device pixels: bunk-gui's at 100%, 133% and
// 200% of an 11 pt font, odd and even ones, and cramped ones.
var sizes = [][2]int{{9, 20}, {12, 27}, {13, 28}, {18, 40}, {24, 54}, {7, 15}, {5, 10}}

// all lists every character Draw draws.
func all() []rune {
	var rs []rune
	for r := rune(0); r < 0x20000; r++ {
		if Has(r) {
			rs = append(rs, r)
		}
	}
	return rs
}

// mask is a drawn character.
type mask struct {
	w, h int
	pix  []byte
}

func draw(t testing.TB, r rune, w, h int) mask {
	t.Helper()
	m := mask{w, h, make([]byte, w*h)}
	if !Draw(r, m.pix, w, w, h, LineWidth(w)) {
		t.Fatalf("Draw(%U) = false", r)
	}
	return m
}

func (m mask) at(x, y int) byte { return m.pix[y*m.w+x] }
func (m mask) col(x int) []byte {
	c := make([]byte, m.h)
	for y := range c {
		c[y] = m.at(x, y)
	}
	return c
}
func (m mask) row(y int) []byte { return m.pix[y*m.w : (y+1)*m.w] }

func (m mask) sum() int {
	n := 0
	for _, v := range m.pix {
		n += int(v)
	}
	return n
}

// String draws the mask in text, for failure messages.
func (m mask) String() string {
	s := "\n"
	for y := range m.h {
		for x := range m.w {
			switch v := m.at(x, y); {
			case v == 255:
				s += "#"
			case v >= 128:
				s += "+"
			case v > 0:
				s += "."
			default:
				s += " "
			}
		}
		s += "\n"
	}
	return s
}

func equal(a, b []byte) bool { return string(a) == string(b) }

// TestHas: the set is kitty's, 1,098 characters, and its edges hold.
func TestHas(t *testing.T) {
	if n := len(all()); n != 1098 {
		t.Errorf("Has accepts %d characters, want 1098", n)
	}
	for _, r := range []rune{'A', ' ', 0x24ff, 0x25a0, 0x25c8, 0x25ca, 0x25e6, 0x27ff, 0x2900, 0xe0af, 0xe0c0, 0xedff, 0xee0c, 0xf5cf, 0xf60e,
		0x1cc1a, 0x1cc40, 0x1cdff, 0x1cde6, 0x1ce15, 0x1ce1a, 0x1ce50, 0x1ceb0, 0x1faff, 0x1fbaf, 0x1fbcd, 0x1fbf0} {
		if Has(r) {
			t.Errorf("Has(%U) = true", r)
		}
		if Draw(r, make([]byte, 100), 10, 10, 10, 1) {
			t.Errorf("Draw(%U) = true", r)
		}
	}
}

// TestDrawStaysInCell: every character writes inside its cell only, at
// every size, and draws something except the blank braille pattern.
func TestDrawStaysInCell(t *testing.T) {
	const pad = 3
	for _, sz := range append(sizes, [2]int{1, 1}, [2]int{2, 3}) {
		w, h := sz[0], sz[1]
		stride := w + 2*pad
		buf := make([]byte, stride*(h+2*pad))
		for _, r := range all() {
			for i := range buf {
				buf[i] = 0xaa
			}
			Draw(r, buf[pad*stride+pad:], stride, w, h, LineWidth(w))
			empty := true
			for y := range h + 2*pad {
				for x := range stride {
					v := buf[y*stride+x]
					in := x >= pad && x < pad+w && y >= pad && y < pad+h
					if !in && v != 0xaa {
						t.Fatalf("%dx%d %U wrote %d outside the cell at %d,%d", w, h, r, v, x-pad, y-pad)
					}
					if in && v != 0 {
						empty = false
					}
				}
			}
			if empty != (r == 0x2800) && w >= 5 {
				t.Errorf("%dx%d %U: empty = %v", w, h, r, empty)
			}
		}
	}
}

// TestLineWidth: light lines grow with the cell, and heavy lines keep
// their parity, so light and heavy lines share a centre.
func TestLineWidth(t *testing.T) {
	for _, c := range []struct{ w, light int }{{5, 1}, {9, 1}, {12, 2}, {13, 2}, {18, 3}, {24, 4}} {
		if got := LineWidth(c.w); got != c.light {
			t.Errorf("LineWidth(%d) = %d, want %d", c.w, got, c.light)
		}
		cv := newCanvas(nil, 0, c.w, 27, c.light)
		if cv.heavy < 2*c.light-1 || (cv.heavy-c.light)%2 != 0 {
			t.Errorf("width %d: heavy line %d for light %d", c.w, cv.heavy, c.light)
		}
	}
}

// armsOf returns the arms of a character drawn as lines from the centre
// to the edges, and false for other characters.
func armsOf(r rune) (arms, bool) {
	fromCurves := func(m int) arms {
		var a arms
		if m&(lineH|arcDR|arcUR) != 0 {
			a.r = light
		}
		if m&(lineH|arcDL|arcUL) != 0 {
			a.l = light
		}
		if m&(lineV|arcUR|arcUL) != 0 {
			a.u = light
		}
		if m&(lineV|arcDR|arcDL) != 0 {
			a.d = light
		}
		return a
	}
	switch {
	case r >= 0x2500 && r <= 0x257f && lineArms[r-0x2500] != (arms{}):
		return lineArms[r-0x2500], true
	case r >= 0x256d && r <= 0x2570:
		return fromCurves([...]int{arcDR, arcDL, arcUL, arcUR}[r-0x256d]), true
	case r == 0xf5d0, r == 0xf5d1:
		return fromCurves([...]int{lineH, lineV}[r-0xf5d0]), true
	case r >= 0xf5d6 && r <= 0xf5ed:
		return fromCurves(branchCurves[r-0xf5d6]), true
	case r >= 0xf5ee && r <= 0xf60d:
		return commitLines[(r-0xf5ee)/2], true
	}
	return arms{}, false
}

// TestLinesMeet: where a character has an arm, its edge is exactly the
// edge of the straight line of that weight, so it meets the next cell's
// line without a step; where it has none, the edge is empty.
func TestLinesMeet(t *testing.T) {
	for _, sz := range sizes {
		w, h := sz[0], sz[1]
		hline := map[weight][]byte{none: make([]byte, h)}
		vline := map[weight][]byte{none: make([]byte, w)}
		for wt, rs := range map[weight][2]rune{light: {'─', '│'}, heavy: {'━', '┃'}, double: {'═', '║'}} {
			hm, vm := draw(t, rs[0], w, h), draw(t, rs[1], w, h)
			hline[wt], vline[wt] = hm.col(0), vm.row(0)
			if !equal(hm.col(0), hm.col(w-1)) || !equal(vm.row(0), vm.row(h-1)) {
				t.Fatalf("%dx%d: %c or %c does not tile:%v%v", w, h, rs[0], rs[1], hm, vm)
			}
		}
		for _, r := range all() {
			a, ok := armsOf(r)
			if !ok {
				continue
			}
			m := draw(t, r, w, h)
			for _, e := range []struct {
				name      string
				got, want []byte
			}{
				{"left", m.col(0), hline[a.l]}, {"right", m.col(w - 1), hline[a.r]},
				{"top", m.row(0), vline[a.u]}, {"bottom", m.row(h - 1), vline[a.d]},
			} {
				if !equal(e.got, e.want) {
					t.Errorf("%dx%d %U %c: %s edge %v, want %v%v", w, h, r, r, e.name, e.got, e.want, m)
				}
			}
		}
	}
}

// TestDoubleChannels: double lines keep their channel open where they
// meet other double lines, and single lines cross them.
func TestDoubleChannels(t *testing.T) {
	for _, sz := range sizes[:5] {
		w, h := sz[0], sz[1]
		tl := LineWidth(w)
		cx, cy := (w-3*tl)/2+tl, (h-3*tl)/2+tl // a pixel in both channels
		for _, c := range []struct {
			r    rune
			open bool
		}{{'╬', true}, {'╦', true}, {'╩', true}, {'╠', true}, {'╣', true}, {'╪', false}, {'╫', false}} {
			m := draw(t, c.r, w, h)
			if got := m.at(cx, cy) == 0; got != c.open {
				t.Errorf("%dx%d %c: centre open = %v, want %v%v", w, h, c.r, got, c.open, m)
			}
		}
		// ╔ keeps both channels open into the corner, and its outer
		// corner is covered.
		m := draw(t, '╔', w, h)
		if m.at(cx, h-1) != 0 || m.at(w-1, cy) != 0 || m.at(cx-tl, cy-tl) != 255 {
			t.Errorf("%dx%d ╔ is not a double corner:%v", w, h, m)
		}
	}
}

// TestBlocksPartition: complementary blocks cover the cell exactly once.
func TestBlocksPartition(t *testing.T) {
	sets := [][]rune{
		{'▀', '▄'}, {'▌', '▐'}, {'▘', '▝', '▖', '▗'}, {'▚', '▞'}, {'▛', '▗'}, {'▙', '▝'},
		{'▔', '▇'}, {0x1fb82, '▆'}, {'▏', 0x1fb8b}, {'▎', 0x1fb8a}, {0x1fbe6, 0x1fbe7, 0x1fb82, '▂'},
	}
	for _, sz := range sizes[:6] {
		w, h := sz[0], sz[1]
		for _, set := range sets {
			total := make([]int, w*h)
			for _, r := range set {
				for i, v := range draw(t, r, w, h).pix {
					if v != 0 && v != 255 {
						t.Fatalf("%dx%d %c: partial pixel %d", w, h, r, v)
					}
					total[i] += int(v) / 255
				}
			}
			for i, n := range total {
				if n != 1 {
					t.Errorf("%dx%d %q: pixel %d,%d covered %d times", w, h, string(set), i%w, i/w, n)
					break
				}
			}
		}
	}
}

// gridMask reads a block character as a pattern of a cols×rows grid,
// and false when it is not one: a part partly covered or a pixel neither
// empty nor covered.
func gridMask(m mask, cols, rows int) (uint, bool) {
	var bitsSet uint
	for row := range rows {
		for col := range cols {
			x0, x1 := part(m.w, col, cols), part(m.w, col+1, cols)
			y0, y1 := part(m.h, row, rows), part(m.h, row+1, rows)
			v := m.at(x0, y0)
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					if m.at(x, y) != v || (v != 0 && v != 255) {
						return 0, false
					}
				}
			}
			if v == 255 {
				bitsSet |= 1 << (row*cols + col)
			}
		}
	}
	return bitsSet, true
}

// TestOctantsFillTheGrid: the octant characters are the 2×4 patterns no
// other character draws, and with the others and the space they draw all
// 256. The sextants are all 2×3 patterns but the halves.
func TestOctantsFillTheGrid(t *testing.T) {
	w, h := 12, 27
	seen := map[uint]rune{0: ' '}
	for _, r := range all() {
		// Heavy horizontal fill is a fill pattern, not a block element,
		// and has an octant twin.
		if r >= 0x1cd00 && r <= 0x1cde5 || r == 0x1fb97 {
			continue
		}
		if m, ok := gridMask(draw(t, r, w, h), 2, 4); ok {
			seen[m] = r
		}
	}
	for i, want := range octants {
		r := rune(0x1cd00 + i)
		got, ok := gridMask(draw(t, r, w, h), 2, 4)
		if !ok || got != uint(want) {
			t.Errorf("%U: pattern %#02x (%v), want %#02x", r, got, ok, want)
		}
		if o, dup := seen[got]; dup {
			t.Errorf("%U draws pattern %#02x, as %U does", r, got, o)
		}
		seen[got] = r
	}
	if len(seen) != 256 {
		t.Errorf("%d of 256 octant patterns drawn", len(seen))
	}
	sext := map[uint]bool{}
	for r := rune(0x1fb00); r <= 0x1fb3b; r++ {
		m, ok := gridMask(draw(t, r, w, h), 2, 3)
		if !ok || m == 0 || m == 63 || m == 21 || m == 42 || sext[m] {
			t.Errorf("%U: sextant pattern %#02x (%v)", r, m, ok)
		}
		sext[m] = true
	}
}

// TestBraille: each bit is one dot, in Unicode's order, and the dots are
// evenly spaced within a cell and across cells.
func TestBraille(t *testing.T) {
	order := [8][2]int{{0, 0}, {0, 1}, {0, 2}, {1, 0}, {1, 1}, {1, 2}, {0, 3}, {1, 3}}
	for _, sz := range sizes[:5] {
		w, h := sz[0], sz[1]
		var top [4]int
		for i, at := range order {
			m := draw(t, rune(0x2800+(1<<i)), w, h)
			x0, y0 := -1, -1
			for y := range h {
				for x := range w {
					if m.at(x, y) != 0 && x0 < 0 {
						x0, y0 = x, y
					}
				}
			}
			if (x0 < w/2) != (at[0] == 0) || y0*4/h != at[1] {
				t.Errorf("%dx%d dot %d at %d,%d, want column %d row %d%v", w, h, i+1, x0, y0, at[0], at[1], m)
			}
			top[at[1]] = y0
		}
		gaps := []int{top[1] - top[0], top[2] - top[1], top[3] - top[2], top[0] + h - top[3]}
		if slicesMax(gaps)-slicesMin(gaps) > 1 {
			t.Errorf("%dx%d: dot rows %v are uneven", w, h, gaps)
		}
		full := draw(t, 0x28ff, w, h)
		one := draw(t, 0x2801, w, h)
		if full.sum() != 8*one.sum() {
			t.Errorf("%dx%d: ⣿ covers %d, eight dots %d", w, h, full.sum(), 8*one.sum())
		}
	}
}

func slicesMax(s []int) int { return max(s[0], s[1], s[2], s[3]) }
func slicesMin(s []int) int { return min(s[0], s[1], s[2], s[3]) }

// TestMirrors: shapes and their mirror images are drawn alike.
func TestMirrors(t *testing.T) {
	w, h := 12, 27 // a light line of 2 is centred, so lines mirror exactly too
	flipX := func(m mask) mask {
		f := mask{m.w, m.h, make([]byte, len(m.pix))}
		for y := range m.h {
			for x := range m.w {
				f.pix[y*m.w+x] = m.at(m.w-1-x, y)
			}
		}
		return f
	}
	for _, p := range [][2]rune{
		{'◢', '◣'}, {'◤', '◥'}, {'╱', '╲'}, {'╭', '╮'}, {'╰', '╯'}, {'◖', '◗'}, {'◜', '◝'},
		{0xe0b0, 0xe0b2}, {0xe0b1, 0xe0b3}, {0xe0b4, 0xe0b6}, {0xe0b5, 0xe0b7}, {0xe0d6, 0xe0d7},
		{0xee00, 0xee02}, {0xee03, 0xee05}, {0x1fb3c, 0x1fb47}, {0x1fbe1, 0x1fbe3}, {0x1fbe9, 0x1fbeb},
	} {
		a, b := draw(t, p[0], w, h), flipX(draw(t, p[1], w, h))
		for i := range a.pix {
			if d := int(a.pix[i]) - int(b.pix[i]); d < -1 || d > 1 {
				t.Errorf("%c (%U) is not the mirror image of %c (%U):%v%v", p[0], p[0], p[1], p[1], a, b)
				break
			}
		}
	}
}

// TestCirclePartsJoin: the twelfths of U+1CC30–U+1CC3F printed in a 4×4
// block, and the quarters in a 2×2 one, draw one closed ellipse.
func TestCirclePartsJoin(t *testing.T) {
	for _, sz := range sizes[:5] {
		w, h := sz[0], sz[1]
		for _, c := range []struct {
			n      int
			layout [][]rune
		}{
			{4, [][]rune{{0x1cc30, 0x1cc31, 0x1cc32, 0x1cc33}, {0x1cc34, 0, 0, 0x1cc37}, {0x1cc38, 0, 0, 0x1cc3b}, {0x1cc3c, 0x1cc3d, 0x1cc3e, 0x1cc3f}}},
			{2, [][]rune{{0x1cc35, 0x1cc36}, {0x1cc39, 0x1cc3a}}},
		} {
			W, H := c.n*w, c.n*h
			img := make([]byte, W*H)
			for row, l := range c.layout {
				for col, r := range l {
					if r != 0 {
						Draw(r, img[row*h*W+col*w:], W, w, h, LineWidth(w))
					}
				}
			}
			half := float64(LineWidth(w)) / 2
			rx, ry := float64(W)/2-half, float64(H)/2-half
			for i := range 720 {
				a := float64(i) * math.Pi / 360
				x := int(float64(W)/2 + rx*math.Cos(a))
				y := int(float64(H)/2 + ry*math.Sin(a))
				covered := byte(0) // the stroke is antialiased: look around the point
				for dy := -1; dy <= 1; dy++ {
					for dx := -1; dx <= 1; dx++ {
						if xx, yy := x+dx, y+dy; xx >= 0 && xx < W && yy >= 0 && yy < H {
							covered = max(covered, img[yy*W+xx])
						}
					}
				}
				if covered < 128 {
					t.Errorf("%dx%d, %d×%d circle: gap at %d,%d (angle %.1f°)", w, h, c.n, c.n, x, y, float64(i)/2)
					break
				}
			}
		}
	}
}

// TestDashesEven: dashed lines keep the same gap inside a cell and across
// cells.
func TestDashesEven(t *testing.T) {
	for _, sz := range sizes[:5] {
		w, h := sz[0], sz[1]
		for _, r := range []rune{'┄', '┈', '╌', '┆', '┊', '╎'} {
			m := draw(t, r, w, h)
			// The middle of the line through two cells in a row.
			var line []byte
			if r == '┆' || r == '┊' || r == '╎' {
				line = append(m.col(w/2), m.col(w/2)...)
			} else {
				line = append(append([]byte{}, m.row(h/2)...), m.row(h/2)...)
			}
			var gaps []int
			run := 0
			for i, v := range line {
				if v == 0 {
					run++
				}
				if (v != 0 || i == len(line)-1) && run > 0 {
					if i-run > 0 && v != 0 { // a gap between two dashes
						gaps = append(gaps, run)
					}
					run = 0
				}
			}
			for _, g := range gaps {
				if g != gaps[0] {
					t.Errorf("%dx%d %c: gaps %v are uneven", w, h, r, gaps)
					break
				}
			}
		}
	}
}

func BenchmarkDrawAll(b *testing.B) {
	rs := all()
	buf := make([]byte, 12*27)
	b.ReportAllocs()
	for b.Loop() {
		for _, r := range rs {
			Draw(r, buf, 12, 12, 27, 2)
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(len(rs)), "ns/glyph")
}

func ExampleDraw() {
	w, h := 6, 10
	m := make([]byte, w*h)
	Draw('┼', m, w, w, h, LineWidth(w))
	for y := range h {
		for x := range w {
			fmt.Print(map[bool]string{true: "#", false: "."}[m[y*w+x] != 0])
		}
		fmt.Println()
	}
	// Output:
	// ..#...
	// ..#...
	// ..#...
	// ..#...
	// ######
	// ..#...
	// ..#...
	// ..#...
	// ..#...
	// ..#...
}
