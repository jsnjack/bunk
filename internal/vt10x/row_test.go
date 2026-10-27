package vt10x

import (
	"fmt"
	"reflect"
	"testing"
)

// TestRowSideTableStaysBounded rewrites part of a row with a new hyperlink
// many times. Entries of overwritten cells must be dropped, and the cells
// must keep reading their current data.
func TestRowSideTableStaysBounded(t *testing.T) {
	term := New(WithSize(4, 1)).(*terminal)
	for i := 0; i < 500; i++ {
		fmt.Fprintf(term, "\r\x1b]8;;https://example.com/%d\x1b\\ab\x1b]8;;\x1b\\", i) //nolint:errcheck
	}
	if n := len(term.lines[0].extras); n > 2*4+32 {
		t.Fatalf("side table holds %d entries for a 4-cell row", n)
	}
	for x, want := range []string{"https://example.com/499", "https://example.com/499", "", ""} {
		if got := term.Link(term.Cell(x, 0).Link); got != want {
			t.Fatalf("cell %d links to %q, want %q", x, got, want)
		}
	}
}

// TestRowRoundTrip checks that every Glyph field survives encoding.
func TestRowRoundTrip(t *testing.T) {
	img := &ImageCell{Placement: &ImagePlacement{ID: 3}}
	glyphs := []Glyph{
		{},
		{Char: 'a', Width: 1, FG: 3, BG: DefaultBG, UL: DefaultUL},
		{Char: 'e', Combining: "\u0301", Width: 1, Mode: attrBold, FG: DefaultFG, BG: 4, UL: DefaultUL},
		{Char: 0x754c, Width: 2, UL: 9, Link: 2},
		{Width: -1, UL: 9, Link: 2},
		{Char: ' ', Width: -2, Mode: attrWrap},
		{Char: 0x2580, Width: 1, Image: img, UL: DefaultUL},
	}
	var r Row
	r.SetGlyphs(glyphs)
	if got := r.AppendGlyphs(nil); !reflect.DeepEqual(got, glyphs) {
		t.Fatalf("round trip changed glyphs\ngot:  %+v\nwant: %+v", got, glyphs)
	}
	var c Row
	c.CopyFrom(r, 3)
	if got := c.AppendGlyphs(nil); !reflect.DeepEqual(got, glyphs[:3]) {
		t.Fatalf("CopyFrom changed glyphs\ngot:  %+v\nwant: %+v", got, glyphs[:3])
	}
}

// TestRowSideTableOverflow fills a very wide row with distinct hyperlinks.
// Cells past the side table's capacity keep their characters and colours.
func TestRowSideTableOverflow(t *testing.T) {
	glyphs := make([]Glyph, maxExtras+10)
	for i := range glyphs {
		glyphs[i] = Glyph{Char: 'x', Width: 1, UL: DefaultUL, Link: uint16(i + 1)}
	}
	var r Row
	r.SetGlyphs(glyphs)
	if g := r.Glyph(maxExtras - 1); g.Link != maxExtras {
		t.Fatalf("last addressable cell lost its link: %+v", g)
	}
	if g := r.Glyph(maxExtras); g.Char != 'x' || g.Link != 0 {
		t.Fatalf("overflow cell = %+v, want 'x' without a link", g)
	}
}
