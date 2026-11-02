package main

import (
	"encoding/binary"
	"math"
	"slices"
	"testing"
	"unicode"
	"unicode/utf8"

	"bunk/internal/boxdraw"
)

// slotPixels returns the pixels of slot v, cells wide, in the atlas.
func slotPixels(a *Atlas, v uint32, cells int) []byte {
	slot := int(v & slotMask)
	x, y := (slot%a.SlotsPerRow)*a.CellW, (slot/a.SlotsPerRow)*a.CellH
	w := cells * a.CellW
	px := make([]byte, 0, w*a.CellH)
	for row := y; row < y+a.CellH; row++ {
		px = append(px, a.Pixels[row*a.W+x:row*a.W+x+w]...)
	}
	return px
}

// TestAtlasBoxDrawing: characters that boxdraw draws go into the atlas as
// its masks, bold or not and wide or not, and never reach the font; text
// and a block with a combining mark still do.
func TestAtlasBoxDrawing(t *testing.T) {
	fr := &fakeRaster{}
	a := newAtlas(12, 27, fr)
	for _, c := range []struct {
		r          rune
		bold, wide bool
	}{{'█', false, false}, {'─', false, false}, {'─', true, false}, {'▛', false, true}, {0x1fb3c, false, false}, {0x2847, false, false}} {
		cells := 1
		if c.wide {
			cells = 2
		}
		want := make([]byte, cells*a.CellW*a.CellH)
		boxdraw.Draw(c.r, want, cells*a.CellW, cells*a.CellW, a.CellH, boxdraw.LineWidth(a.CellW))
		v := a.Lookup(c.r, "", c.bold, c.wide)
		if got := slotPixels(a, v, cells); string(got) != string(want) {
			t.Errorf("%c (bold %v, wide %v): the atlas slot differs from boxdraw's mask", c.r, c.bold, c.wide)
		}
		if (v&flagWide != 0) != c.wide {
			t.Errorf("%c: wide flag %v, want %v", c.r, v&flagWide != 0, c.wide)
		}
	}
	if fr.calls != 0 || a.Drawn != 6 {
		t.Errorf("font drew %d glyphs and boxdraw %d; want 0 and 6", fr.calls, a.Drawn)
	}
	a.Lookup('A', "", false, false)
	a.Lookup('▌', "́", false, false)
	if fr.calls != 2 || a.Rasterized != 2 || a.Drawn != 6 {
		t.Errorf("font drew %d glyphs (Rasterized %d) and boxdraw %d; want 2, 2 and 6", fr.calls, a.Rasterized, a.Drawn)
	}
}

// TestFitInk: ink within tolerance of the slot keeps its place, wider ink
// moves into the slot, and ink wider than the slot shrinks to fill it.
func TestFitInk(t *testing.T) {
	for _, c := range []struct {
		x0, x1, w   int
		scale, dx   float64
		overflowing bool
	}{
		{1, 8, 9, 1, 0, false},                   // the terminal font's glyph
		{-1, 10, 9, 1, 0, false},                 // a sliver past each edge
		{3, 14, 12, 1, -2, true},                 // ✗: shifted left
		{-3, 8, 12, 1, 3, false},                 // a left bearing: shifted right
		{2, 23, 12, 12.0 / 21, -24.0 / 21, true}, // ⟳ in one cell: shrunk
		{2, 23, 24, 1, 0, false},                 // ⟳ across two cells
	} {
		scale, dx, overflowing := fitInk(c.x0, c.x1, c.w, 1)
		if math.Abs(scale-c.scale) > 1e-9 || math.Abs(dx-c.dx) > 1e-9 || overflowing != c.overflowing {
			t.Errorf("fitInk(%d, %d, %d) = %v, %v, %v; want %v, %v, %v", c.x0, c.x1, c.w, scale, dx, overflowing, c.scale, c.dx, c.overflowing)
		}
		if lo, hi := float64(c.x0)*scale+dx, float64(c.x1)*scale+dx; scale != 1 && (lo < -1e-9 || hi > float64(c.w)+1e-9) {
			t.Errorf("fitInk(%d, %d, %d) places the ink at [%v, %v)", c.x0, c.x1, c.w, lo, hi)
		}
	}
}

// TestAtlasFitsWideInk draws symbols with the real font, in one cell and in
// two, and checks that each leaves no ink outside its slot (but for the
// one-pixel tolerance) and some inside. Fallback fonts draw many of them
// wider than a cell, such as ⟳ from Noto Sans Math. The terminal font's
// own ASCII never counts as overflowing, so text keeps its size.
func TestAtlasFitsWideInk(t *testing.T) {
	var runes []rune
	for _, block := range [][2]rune{{0x2190, 0x27bf}, {0x2b00, 0x2bff}} {
		for r := block[0]; r <= block[1]; r++ {
			runes = append(runes, r)
		}
	}
	for _, scale := range []float64{1, 4.0 / 3, 2} {
		a, err := BuildAtlas(defaultSettings().Font+" 11", scale)
		if err != nil {
			t.Fatal(err)
		}
		for r := rune(0x21); r < 0x7f; r++ {
			for _, bold := range []bool{false, true} {
				if a.Lookup(r, "", bold, false)&flagOverflow != 0 {
					t.Errorf("scale %v: %c (bold %v) counts as overflowing its cell", scale, r, bold)
				}
			}
		}
		p := a.raster.(*pangoRaster)
		tol, overflowing := max(1, p.cellW/8), 0
		for _, r := range runes {
			if !unicode.IsGraphic(r) || unicode.IsSpace(r) || boxdraw.Has(r) {
				continue
			}
			for cells := 1; cells <= 2; cells++ {
				w := cells * p.cellW
				color, overflows := p.draw(string(r), false, cells)
				if color {
					break // an emoji: TestAtlasColorEmoji
				}
				if overflows {
					overflowing++
				}
				dst := make([]byte, w*p.cellH)
				p.copyOut(dst, w, false)
				src, stride := p.surf.Data(), p.surf.Stride()
				for y := 0; y < p.cellH; y++ {
					for x := 0; x < 4*p.cellW; x++ {
						if binary.NativeEndian.Uint32(src[y*stride+4*x:])>>24 != 0 && (x < p.cellW-tol || x >= p.cellW+w+tol) {
							t.Fatalf("scale %v: %c (U+%04X) in %d cells has ink at column %d of the surface; the slot is [%d, %d)",
								scale, r, r, cells, x, p.cellW, p.cellW+w)
						}
					}
				}
				if !slices.ContainsFunc(dst, func(b byte) bool { return b != 0 }) {
					t.Errorf("scale %v: %c (U+%04X) in %d cells drew nothing", scale, r, r, cells)
				}
			}
		}
		t.Logf("scale %v: cell %dx%d, %d of %d symbols overflow one cell", scale, p.cellW, p.cellH, overflowing, len(runes))
	}
}

// TestAtlasColorEmoji draws emoji with the real fonts: each lands on the
// colour page with its own colours (🔵 is blue, whatever the foreground),
// while text and a text-style heart stay on the coverage page.
func TestAtlasColorEmoji(t *testing.T) {
	for _, scale := range []float64{1, 4.0 / 3, 2} {
		a, err := BuildAtlas(defaultSettings().Font+" 11", scale)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range []string{"\U0001F535", "\U0001F600", "\u2764\uFE0F", "\u2705", "\U0001F680"} {
			r, _ := utf8.DecodeRuneInString(e)
			v := a.Lookup(r, e[utf8.RuneLen(r):], false, true)
			if v&flagColor == 0 || v&slotMask == 0 {
				t.Errorf("scale %v: %s: glyph %x, want a colour slot", scale, e, v)
			}
		}
		for _, r := range []rune{'A', '\u2764', '\u2713'} {
			if v := a.Lookup(r, "", false, false); v&flagColor != 0 {
				t.Errorf("scale %v: %c: glyph %x on the colour page", scale, r, v)
			}
		}
		// The blue circle's slot holds opaque blue pixels.
		px := a.slotPixels(&a.Color, a.Lookup(0x1F535, "", false, true))
		blue := 0
		for y := range a.CellH {
			for x := range 2 * a.CellW {
				p := px[4*(y*a.Color.W+x):]
				if p[3] == 255 && int(p[2]) > int(p[0])+80 {
					blue++
				}
			}
		}
		if area := 2 * a.CellW * a.CellH; blue < area/4 {
			t.Errorf("scale %v: 🔵 has %d opaque blue pixels of %d", scale, blue, area)
		}
	}
}
