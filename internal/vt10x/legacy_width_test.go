package vt10x

import "testing"

// TestLegacyWidthsIgnoreZeroWidthCodePoints: with DECRST 2027 every code
// point keeps its own width. VS16 and a keycap mark join the previous cell
// for copy and search but do not widen it; scripts/terminal_features
// 03_cursor_unicode.sh shows the same line.
func TestLegacyWidthsIgnoreZeroWidthCodePoints(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		cells      []string // lead cell text in display order
		widths     []int8
	}{
		{"emoji variation selector", "\u2764\ufe0f|", []string{"\u2764\ufe0f", "|"}, []int8{1, 1}},
		{"keycap", "1\ufe0f\u20e3|", []string{"1\ufe0f\u20e3", "|"}, []int8{1, 1}},
		{"combining accent", "e\u0301|", []string{"e\u0301", "|"}, []int8{1, 1}},
		{"flag", "\U0001F1F3\U0001F1F1|", []string{"\U0001F1F3", "\U0001F1F1", "|"}, []int8{2, 2, 1}},
		{"zwj family", "\U0001F469\u200d\U0001F467|", []string{"\U0001F469\u200d", "\U0001F467", "|"}, []int8{2, 2, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term := New(WithSize(20, 2))
			if _, err := term.Write([]byte("\x1b[?2027l" + tc.text)); err != nil {
				t.Fatal(err)
			}
			x := 0
			for i, want := range tc.cells {
				g := term.Cell(x, 0)
				if g.Text() != want || g.Width != tc.widths[i] {
					t.Fatalf("cell %d = %+q width %d, want %+q width %d", x, g.Text(), g.Width, want, tc.widths[i])
				}
				x += int(g.Width)
			}
			if cur := term.Cursor(); cur.X != x {
				t.Fatalf("cursor at %d, want %d", cur.X, x)
			}
		})
	}
}
