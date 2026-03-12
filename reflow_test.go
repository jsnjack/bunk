package main

import (
	"bytes"
	"testing"

	"github.com/hinshun/vt10x"
)

// ---------------------------------------------------------------------------
// stripAltScreen
//
// Bug: btop background leak after resize+exit
//
// During reflow, the raw PTY buffer is replayed through vt10x. Alt-screen
// content (btop, vim, etc.) must be stripped out so it doesn't contaminate
// the normal-screen buffer with stale colours and content.
// ---------------------------------------------------------------------------

func TestStripAltScreen_BtopBackgroundLeak_NoSequences(t *testing.T) {
	input := []byte("hello world, no alt screen here")
	got := stripAltScreen(input)
	if !bytes.Equal(got, input) {
		t.Errorf("stripAltScreen changed input that has no sequences")
	}
}

func TestStripAltScreen_BtopBackgroundLeak_OnePair(t *testing.T) {
	before := []byte("before")
	entry := []byte("\x1b[?1049h")
	middle := []byte("alt screen content")
	exit := []byte("\x1b[?1049l")
	after := []byte("after")

	input := concat(before, entry, middle, exit, after)
	got := stripAltScreen(input)
	want := concat(before, after)
	if !bytes.Equal(got, want) {
		t.Errorf("stripAltScreen one pair:\n got %q\nwant %q", got, want)
	}
}

func TestStripAltScreen_BtopBackgroundLeak_EntryWithoutExit(t *testing.T) {
	before := []byte("before")
	entry := []byte("\x1b[?1049h")
	trailing := []byte("stuff after entry with no exit")

	input := concat(before, entry, trailing)
	got := stripAltScreen(input)
	// Entry without exit: everything from entry onward is discarded.
	if !bytes.Equal(got, before) {
		t.Errorf("stripAltScreen entry without exit:\n got %q\nwant %q", got, before)
	}
}

func TestStripAltScreen_BtopBackgroundLeak_MultiplePairs(t *testing.T) {
	seg1 := []byte("seg1")
	entry1 := []byte("\x1b[?1049h")
	alt1 := []byte("alt1")
	exit1 := []byte("\x1b[?1049l")
	seg2 := []byte("seg2")
	entry2 := []byte("\x1b[?47h")
	alt2 := []byte("alt2")
	exit2 := []byte("\x1b[?47l")
	seg3 := []byte("seg3")

	input := concat(seg1, entry1, alt1, exit1, seg2, entry2, alt2, exit2, seg3)
	got := stripAltScreen(input)
	want := concat(seg1, seg2, seg3)
	if !bytes.Equal(got, want) {
		t.Errorf("stripAltScreen multiple pairs:\n got %q\nwant %q", got, want)
	}
}

func TestStripAltScreen_BtopBackgroundLeak_AlternateVariants(t *testing.T) {
	// Use the ?1047h/l variant.
	before := []byte("A")
	entry := []byte("\x1b[?1047h")
	middle := []byte("X")
	exit := []byte("\x1b[?1047l")
	after := []byte("B")

	input := concat(before, entry, middle, exit, after)
	got := stripAltScreen(input)
	want := concat(before, after)
	if !bytes.Equal(got, want) {
		t.Errorf("stripAltScreen ?1047 variant:\n got %q\nwant %q", got, want)
	}
}

// concat joins multiple byte slices.
func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// ---------------------------------------------------------------------------
// rowContentEnd
// ---------------------------------------------------------------------------

func TestRowContentEnd(t *testing.T) {
	tests := []struct {
		name string
		row  []vt10x.Glyph
		want int
	}{
		{
			name: "empty row",
			row:  nil,
			want: 0,
		},
		{
			name: "all blank NUL",
			row:  makeGlyphRow(0, 0, 0, 0),
			want: 0,
		},
		{
			name: "all blank spaces",
			row:  makeGlyphRow(' ', ' ', ' '),
			want: 0,
		},
		{
			name: "content then blanks",
			row:  makeGlyphRow('H', 'i', ' ', ' '),
			want: 2,
		},
		{
			name: "content at very end",
			row:  makeGlyphRow(' ', ' ', 'Z'),
			want: 3,
		},
		{
			name: "all content",
			row:  makeGlyphRow('A', 'B', 'C'),
			want: 3,
		},
		{
			name: "NUL between content",
			row:  makeGlyphRow('A', 0, 'B'),
			want: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rowContentEnd(tt.row)
			if got != tt.want {
				t.Errorf("rowContentEnd = %d, want %d", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// rowVisualHeight
// ---------------------------------------------------------------------------

func TestRowVisualHeight(t *testing.T) {
	tests := []struct {
		name string
		end  int // number of content glyphs in the row
		cols int
		want int
	}{
		{"end=0", 0, 10, 1},
		{"fits in one row", 5, 10, 1},
		{"exactly one row", 10, 10, 1},
		{"one char overflow", 11, 10, 2},
		{"exactly two rows", 20, 10, 2},
		{"two rows plus one", 21, 10, 3},
		{"single column", 5, 1, 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build a row with tt.end content characters followed by blanks.
			row := make([]vt10x.Glyph, tt.end+5) // extra blanks at end
			for i := 0; i < tt.end; i++ {
				row[i] = vt10x.Glyph{Char: 'X'}
			}
			// Remaining glyphs default to Char==0 (blank).
			got := rowVisualHeight(row, tt.cols)
			if got != tt.want {
				t.Errorf("rowVisualHeight(end=%d, cols=%d) = %d, want %d",
					tt.end, tt.cols, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// isWordChar
// ---------------------------------------------------------------------------

func TestIsWordChar(t *testing.T) {
	tests := []struct {
		name string
		r    rune
		want bool
	}{
		{"lowercase letter", 'a', true},
		{"uppercase letter", 'Z', true},
		{"digit 0", '0', true},
		{"digit 9", '9', true},
		{"underscore", '_', true},
		{"hyphen", '-', true},
		{"dot", '.', true},
		{"slash", '/', true},
		{"tilde", '~', true},
		{"at sign", '@', true},
		{"plus", '+', true},
		{"colon", ':', true},
		{"percent", '%', true},
		{"equals", '=', true},
		{"space", ' ', false},
		{"NUL", 0, false},
		{"exclamation", '!', false},
		{"hash", '#', false},
		{"ampersand", '&', false},
		{"open paren", '(', false},
		{"pipe", '|', false},
		{"tab", '\t', false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isWordChar(tt.r)
			if got != tt.want {
				t.Errorf("isWordChar(%q) = %v, want %v", tt.r, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// emitColorCode
// ---------------------------------------------------------------------------

func TestEmitColorCode(t *testing.T) {
	tests := []struct {
		name string
		c    vt10x.Color
		isFG bool
		want string
	}{
		// Default colours should produce no output.
		{"DefaultFG as FG", vt10x.DefaultFG, true, ""},
		{"DefaultBG as BG", vt10x.DefaultBG, false, ""},

		// Standard ANSI colours 0-7.
		{"ANSI 0 FG (black)", 0, true, ";30"},
		{"ANSI 0 BG (black)", 0, false, ";40"},
		{"ANSI 7 FG (white)", 7, true, ";37"},
		{"ANSI 7 BG (white)", 7, false, ";47"},

		// Bright colours 8-15.
		{"bright 8 FG", 8, true, ";90"},
		{"bright 8 BG", 8, false, ";100"},
		{"bright 15 FG", 15, true, ";97"},
		{"bright 15 BG", 15, false, ";107"},

		// 256-colour palette (16-255).
		{"256-color 128 FG", 128, true, ";38;5;128"},
		{"256-color 128 BG", 128, false, ";48;5;128"},
		{"256-color 16 FG", 16, true, ";38;5;16"},
		{"256-color 255 FG", 255, true, ";38;5;255"},

		// Truecolor: encoded as r<<16 | g<<8 | b, but only for values >= 256.
		// 256 is the first value that falls into the truecolor branch.
		{"truecolor r=255,g=128,b=0 FG", vt10x.Color(255<<16 | 128<<8 | 0), true, ";38;2;255;128;0"},
		{"truecolor r=255,g=128,b=0 BG", vt10x.Color(255<<16 | 128<<8 | 0), false, ";48;2;255;128;0"},
		{"truecolor r=0,g=0,b=0 FG", vt10x.Color(256), true, ";38;2;0;1;0"},
		{"truecolor r=1,g=2,b=3 FG", vt10x.Color(1<<16 | 2<<8 | 3), true, ";38;2;1;2;3"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			emitColorCode(&buf, tt.c, tt.isFG)
			got := buf.String()
			if got != tt.want {
				t.Errorf("emitColorCode(%d, isFG=%v) = %q, want %q",
					tt.c, tt.isFG, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// emitSGR (integration-level: verify full sequence structure)
//
// Bug: btop background leak — SGR reset before vt10x resize
//
// We write \x1b[0m before resize to clear cursor attributes. emitSGR must
// generate correct reset sequences so the reflow replay produces clean output.
// ---------------------------------------------------------------------------

func TestEmitSGR_BtopBackgroundLeak_DefaultAttrs(t *testing.T) {
	g := vt10x.Glyph{FG: vt10x.DefaultFG, BG: vt10x.DefaultBG, Mode: 0}
	var buf bytes.Buffer
	emitSGR(&buf, g)
	got := buf.String()
	// With all defaults, should produce a simple reset: \x1b[0m
	want := "\x1b[0m"
	if got != want {
		t.Errorf("emitSGR(default) = %q, want %q", got, want)
	}
}

func TestEmitSGR_BtopBackgroundLeak_BoldAndColor(t *testing.T) {
	g := vt10x.Glyph{
		FG:   1, // ANSI red
		BG:   vt10x.DefaultBG,
		Mode: vtAttrBold,
	}
	var buf bytes.Buffer
	emitSGR(&buf, g)
	got := buf.String()
	want := "\x1b[0;1;31m"
	if got != want {
		t.Errorf("emitSGR(bold+red) = %q, want %q", got, want)
	}
}
