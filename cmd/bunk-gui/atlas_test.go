package main

import (
	"testing"

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
