package vt10x

import "testing"

// Regressions found by FuzzParse and FuzzResize. Each input is also kept in
// testdata/fuzz.

// TestWriteKeepsSplitRune: a chunk that ends inside a multi-byte character
// must leave the whole prefix unconsumed. Write used to drop its lead byte
// and return the rest, so the character was lost.
func TestWriteKeepsSplitRune(t *testing.T) {
	for _, tc := range []struct{ name, first, second string }{
		{"two of three bytes", "a\xe6\x97", "\xa5b"},
		{"one of three bytes", "a\xe6", "\x97\xa5b"},
		{"three of four bytes", "a\xf0\x9f\x91", "\x8db"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term := New(WithSize(10, 1))
			n, err := term.Write([]byte(tc.first))
			if err != nil {
				t.Fatal(err)
			}
			if n != 1 {
				t.Fatalf("Write consumed %d bytes of %q, want 1", n, tc.first)
			}
			if _, err := term.Write([]byte(tc.first[n:] + tc.second)); err != nil {
				t.Fatal(err)
			}
			want := []rune(tc.first + tc.second)[1]
			if got := term.Cell(1, 0).Char; got != want {
				t.Fatalf("cell 1 = %q, want %q", got, want)
			}
		})
	}
}

// TestTabCountBounded: CHT and CBT with a huge count used to step through
// every tab stop once per count, holding the terminal lock for seconds (or
// forever for counts near 2^63).
func TestTabCountBounded(t *testing.T) {
	for _, tc := range []struct {
		name, seq string
		x         int
	}{
		{"forward", "\x1b[9223372036854775807I", 79},
		{"backward", "\x1b[40G\x1b[9223372036854775807Z", 0},
		{"forward two", "\x1b[2I", 16},
		{"backward two", "\x1b[40G\x1b[2Z", 24},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term := New(WithSize(80, 2))
			if _, err := term.Write([]byte(tc.seq)); err != nil {
				t.Fatal(err)
			}
			if x := term.Cursor().X; x != tc.x {
				t.Fatalf("cursor x = %d, want %d", x, tc.x)
			}
		})
	}
}

// TestWrapPaddingOnlyBeforeWrap: a wide character that does not fit leaves
// a width -2 padding cell at the end of the row and wraps. Copy, search and
// reflow skip padding, so it must stay the last cell of a wrapped row. DCH
// and widening the grid used to move it in front of ordinary cells.
func TestWrapPaddingOnlyBeforeWrap(t *testing.T) {
	// Column 7 of an 8-column row becomes padding.
	const padded = "abcdefg界"
	for _, tc := range []struct {
		name, seq    string
		cols, resize int
	}{
		{"fuzz input", "000000ｗｉｄａ\x1b80\x1b[P", 13, 0},
		{"delete character", padded + "\x1b[1;2H\x1b[P", 8, 0},
		{"delete characters", padded + "\x1b[1;2H\x1b[3P", 8, 0},
		{"insert character", padded + "\x1b[1;2H\x1b[@", 8, 0},
		{"widen", padded, 8, 12},
		{"narrow", padded, 8, 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term := newFuzzTerm(tc.cols, 3)
			term.write([]byte(tc.seq), false)
			if tc.resize > 0 {
				term.Resize(tc.resize, 3)
			}
			cols, _ := term.Size()
			for y, row := range append(term.lines, term.altLines...) {
				if err := checkRow(row, cols, len(term.links), sideTableBound(cols)); err != nil {
					t.Fatalf("row %d: %v", y, err)
				}
			}
		})
	}
}
