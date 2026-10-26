package vt10x

import (
	"fmt"
	"strings"
	"testing"
)

// wideRow writes red 語 then green 本, each with its own link, from column
// 0, so the cells read 語 lead, 語 cont, 本 lead, 本 cont, then text.
const wideRow = "\x1b]8;;https://a.example\x1b\\\x1b[31m語" +
	"\x1b]8;;https://b.example\x1b\\\x1b[32m本" +
	"\x1b]8;;\x1b\\\x1b[mxy"

// TestShiftNeverPairsForeignContinuation covers ICH and DCH whose edges cut
// a wide character: the cut character is erased, as in xterm, instead of
// leaving its lead next to another character's continuation.
func TestShiftNeverPairsForeignContinuation(t *testing.T) {
	for _, tc := range []struct {
		name, seq, want string
	}{
		// Deleting 語's second half and 本's first half erases both; their
		// other halves stay as blanks.
		{"delete across two wide characters", "\x1b[2G\x1b[2P", "  xy"},
		// Deleting 語's lead erases 語; 本 moves left whole.
		{"delete a lead", "\x1b[1G\x1b[1P", " 本xy"},
		// Deleting from 語's second half erases 語; 本 moves left whole.
		{"delete a continuation", "\x1b[2G\x1b[1P", " 本xy"},
		// Inserting inside 語 erases it; 本 moves right whole and y falls
		// off the end.
		{"insert inside a wide character", "\x1b[2G\x1b[1@", "   本x"},
		// Inserting before 本 keeps both.
		{"insert between wide characters", "\x1b[3G\x1b[1@", "語 本x"},
		// Inserting pushes 本 off the end: its lead is not left orphaned.
		{"insert pushing a wide character off the end", "\x1b[1G\x1b[3@", "   語"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term := newFuzzTerm(6, 2)
			term.write([]byte(wideRow+tc.seq), false)
			if err := checkInvariants(term.State, sideTableBound(6)); err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimRight(shownText(term.State, 0), " "); got != tc.want {
				t.Errorf("row = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestShiftEveryPosition applies ICH and DCH of every count at every column
// of rows mixing wide and narrow characters with distinct colours and
// links; the grid must stay valid each time, with no lead paired with
// another character's continuation.
func TestShiftEveryPosition(t *testing.T) {
	rows := []string{
		wideRow,
		"\x1b[31m語\x1b[32m本\x1b[33m字\x1b[34m漢",
		"a\x1b[35m界\x1b[36mb\x1b[31m語c",
		"\x1b]8;;https://c.example\x1b\\界\x1b]8;;https://d.example\x1b\\界\x1b]8;;\x1b\\",
	}
	for cols := 5; cols <= 9; cols++ {
		for ri, row := range rows {
			for x := 1; x <= cols; x++ {
				for n := 1; n <= cols+1; n++ {
					for _, op := range []byte{'@', 'P'} {
						seq := fmt.Sprintf("\x1b[%dG\x1b[%d%c", x, n, op)
						term := newFuzzTerm(cols, 2)
						term.write([]byte(row+seq), false)
						if err := checkInvariants(term.State, sideTableBound(cols)); err != nil {
							t.Fatalf("cols %d row %d %q: %v", cols, ri, seq, err)
						}
					}
				}
			}
		}
	}
}

// shownText returns row y as shown: each wide character once.
func shownText(s *State, y int) string {
	var b strings.Builder
	for x := 0; x < s.cols; x++ {
		g := s.lines[y].Glyph(x)
		if g.Width < 0 {
			continue
		}
		b.WriteRune(g.Char)
	}
	return b.String()
}
