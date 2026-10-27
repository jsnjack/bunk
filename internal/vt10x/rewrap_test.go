package vt10x

import (
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"
)

// rewrapLine reflows row as one logical line at width cols and returns the
// emitted rows as glyphs.
func rewrapLine(row Row, cols int) [][]Glyph {
	var out [][]Glyph
	w := NewRewrap(cols, func(r Row) {
		out = append(out, r.AppendGlyphs(nil)[:r.TrimmedLen()])
	})
	w.Append(row, 0, row.ReflowEnd())
	w.EndLine()
	return out
}

// randomRow returns a short row of cells chosen to hit every case Rewrap
// treats differently: wide pairs, broken pairs, padding, wrap bits and
// side-table data.
func randomRow(rng *rand.Rand) Row {
	img := &ImageCell{}
	var glyphs []Glyph
	for n := rng.IntN(8); n > 0; n-- {
		g := Glyph{Char: 'a' + rune(rng.IntN(3)), Width: 1, FG: DefaultFG, BG: DefaultBG, UL: DefaultUL}
		switch rng.IntN(10) {
		case 0:
			g.Mode = attrWrap
		case 1:
			g.Link = uint16(rng.IntN(2) + 1)
		case 2:
			g.Combining = "\u0301"
		case 3:
			g.Image = img
		case 4:
			g.Char, g.Width = ' ', -2
		case 5:
			g.Char, g.Width = 0, 0
			g.BG = 3
		}
		glyphs = append(glyphs, g)
		if rng.IntN(4) == 0 {
			g.Char, g.Width = '界', 2
			cont := g
			cont.Char, cont.Combining, cont.Width = 0, "", -1
			if rng.IntN(5) == 0 {
				cont.FG = 7 // not the continuation Rewrap builds
			}
			glyphs = append(glyphs, g)
			if rng.IntN(6) != 0 {
				glyphs = append(glyphs, cont)
			}
		}
	}
	var r Row
	r.SetGlyphs(glyphs)
	return r
}

// TestReflowFitMeansUnchanged checks the promise reflow relies on to move
// rows without copying them: a row with fit n comes out of Rewrap as its
// first n cells at any width of n or more.
func TestReflowFitMeansUnchanged(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 8))
	fits := 0
	for i := 0; i < 20000; i++ {
		row := randomRow(rng)
		fit := row.ReflowFit()
		if fit < 0 {
			continue
		}
		fits++
		want := row.AppendGlyphs(nil)[:fit]
		for cols := max(fit, 1); cols < fit+3; cols++ {
			got := rewrapLine(row, cols)
			if len(got) != 1 || !slices.Equal(got[0], want) {
				t.Fatalf("row %+v with fit %d at width %d became %+v", want, fit, cols, got)
			}
		}
	}
	if fits < 1000 {
		t.Fatalf("only %d of the random rows fit; the generator no longer covers the fast path", fits)
	}
}

// TestRewrapPlacesCells checks wrapping, padding, continuation rebuilding
// and the one-column replacement on a small line.
func TestRewrapPlacesCells(t *testing.T) {
	term := New(WithSize(10, 1))
	term.Write([]byte("ab界\x1b]8;;x\x1b\\c\x1b]8;;\x1b\\界")) //nolint:errcheck
	row := term.(*terminal).lines[0]
	text := func(rows [][]Glyph) []string {
		var s []string
		for _, r := range rows {
			line := ""
			for _, g := range r {
				switch g.Width {
				case -1:
					line += "+"
				case -2:
					line += "_"
				default:
					line += string(g.Char)
				}
				if g.Mode&attrWrap != 0 {
					line += "/"
				}
			}
			s = append(s, line)
		}
		return s
	}
	for cols, want := range map[int][]string{
		1:  {"a/", "b/", "\uFFFD/", "c/", "\uFFFD"},
		3:  {"ab_/", "界+c/", "界+"},
		4:  {"ab界+/", "c界+"},
		10: {"ab界+c界+"},
	} {
		got := rewrapLine(*row, cols)
		if !reflect.DeepEqual(text(got), want) {
			t.Errorf("width %d: %q, want %q", cols, text(got), want)
		}
		for _, r := range got {
			for _, g := range r {
				if g.Char == 'c' && g.Link == 0 {
					t.Errorf("width %d: hyperlink lost", cols)
				}
			}
		}
	}
}
