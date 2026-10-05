package main

import (
	"bunk/internal/vt10x"
	"fmt"
	"strings"
	"testing"
)

// rowContentEnd returns the index one past the last non-blank cell in row.
// Only cells with an actual visible character (non-NUL, non-space) are
// considered content.  Trailing spaces are ignored regardless of their
// background colour — shells commonly use \x1b[K (erase-to-EOL) to fill
// the rest of a prompt line with a coloured background; we must not replay
// those filled cells or they tint the entire pane.
func rowContentEnd(row []vt10x.Glyph) int {
	end := len(row)
	for end > 0 {
		g := row[end-1]
		if g.Width < 0 || (g.Char != 0 && g.Char != ' ') {
			break
		}
		end--
	}
	return end
}

// rowChars extracts the visible rune sequence from a Glyph row (up to the
// last non-blank character).  Interior spaces are included — they are
// meaningful content that distinguishes e.g. "Mar 23" from "Mar23".
// Trailing blank cells (NUL or space) are excluded via rowContentEnd.
func rowChars(row []vt10x.Glyph) []rune {
	end := rowContentEnd(row)
	if end == 0 {
		return nil
	}
	chars := make([]rune, 0, end)
	for i := 0; i < end; i++ {
		if c := row[i].Char; c != 0 { // include spaces, exclude only unset cells
			chars = append(chars, c)
			chars = append(chars, []rune(row[i].Combining)...)
		}
	}
	return chars
}

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

func TestResizePreservesCursorOnBlankLine(t *testing.T) {
	// Simulate a terminal with log output ending in \r\n.
	term := vt10x.New(vt10x.WithSize(40, 10))
	p := &Pane{
		term:            term,
		scrollbackLines: 100,
		sb:              sbRing{maxLines: 100},
	}

	// Write three log lines each ending with \r\n.
	data := []byte("11:55:41 | log line 1\r\n11:55:42 | log line 2\r\n11:55:43 | log line 3\r\n")
	p.captureAndWrite(data)

	// Cursor should be on row 3 (the blank row after the last \r\n).
	cur := p.term.Cursor()
	if cur.Y != 3 {
		t.Fatalf("pre-resize cursor Y = %d, want 3", cur.Y)
	}

	// Resize (height only change).
	p.resizeAndReflow(40, 8)

	// After resize, the cursor should NOT be on a content row.
	// Verify the cursor row is blank (no visible characters).
	cur = p.term.Cursor()
	cols, _ := p.term.Size()
	row := captureRow(p.term, cur.Y, cols)
	if rowContentEnd(row) != 0 {
		t.Errorf("after resize cursor is on row %d which has content; should be on blank row", cur.Y)
	}
}

func TestResizePreservesCursorOnPrompt(t *testing.T) {
	// Simulate a shell prompt — cursor is mid-line on a content row.
	term := vt10x.New(vt10x.WithSize(40, 10))
	p := &Pane{
		term:            term,
		scrollbackLines: 100,
		sb:              sbRing{maxLines: 100},
	}

	p.captureAndWrite([]byte("user@host:~$ "))

	// Cursor should be on row 0, column 13.
	cur := p.term.Cursor()
	if cur.Y != 0 || cur.X != 13 {
		t.Fatalf("pre-resize cursor (X=%d,Y=%d), want (13,0)", cur.X, cur.Y)
	}

	// Resize.
	p.resizeAndReflow(40, 8)

	// The trailing prompt space must count toward the cursor position.
	cur = p.term.Cursor()
	if cur.Y != 0 || cur.X != 13 {
		t.Errorf("after height resize cursor (X=%d,Y=%d), want (13,0)", cur.X, cur.Y)
	}
}

func TestResizeColumnChangePreservesCursorAfterTrailingSpace(t *testing.T) {
	term := vt10x.New(vt10x.WithSize(40, 5))
	p := &Pane{
		term:            term,
		scrollbackLines: 100,
		sb:              sbRing{maxLines: 100},
	}

	p.captureAndWrite([]byte("user@host ~\r\n$ "))

	cur := p.term.Cursor()
	if cur.Y != 1 || cur.X != 2 {
		t.Fatalf("pre-resize cursor (X=%d,Y=%d), want (2,1)", cur.X, cur.Y)
	}

	// Column change forces the full reflow path.
	p.resizeAndReflow(80, 5)

	cur = p.term.Cursor()
	if cur.Y != 1 || cur.X != 2 {
		t.Errorf("after column resize cursor (X=%d,Y=%d), want (2,1)", cur.X, cur.Y)
	}
}

func paneLogicalText(p *Pane) string {
	cols, rows := p.term.Size()
	var text strings.Builder
	for r := 0; r < p.sb.count+findContentRows(p.term, cols, rows); r++ {
		row := p.sb.get(r)
		if r >= p.sb.count {
			row = captureRow(p.term, r-p.sb.count, cols)
		}
		wrapped := row[len(row)-1].Mode&vt10x.AttrWrap != 0
		end := len(row)
		if !wrapped {
			end = rowContentEnd(row)
		}
		for _, g := range row[:end] {
			if g.Width < 0 {
				continue
			}
			ch := g.Char
			if ch == 0 {
				ch = ' '
			}
			text.WriteRune(ch)
			text.WriteString(g.Combining)
		}
		if !wrapped {
			text.WriteByte('\n')
		}
	}
	return strings.TrimRight(text.String(), "\n")
}

func TestCellReflowLogicalLines(t *testing.T) {
	for _, tc := range []struct{ name, text string }{
		{"soft_wraps", "abcdefghijklmnopqrstuvwxyz"},
		{"hard_breaks", "abc\r\ndefghijk\r\n\r\nlmnop"},
		{"wide_wrap_padding", "abcd界ef界ghi"},
		{"graphemes", "abcd👩‍👩‍👧‍👦é🇳🇱❤️tail"},
		{"spaces_at_wrap", "abc     def   ghi"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := regressionPane(5, 3)
			p.captureAndWrite([]byte(tc.text))
			want := paneLogicalText(p)
			for _, cols := range []int{3, 20, 4, 5, 30} {
				p.resize(0, 0, cols+1, 3)
				if got := paneLogicalText(p); got != want {
					t.Fatalf("width %d: got %q, want %q", cols, got, want)
				}
				for r := 0; r < p.sb.count; r++ {
					if len(p.sb.get(r)) != cols {
						t.Fatal("stale-width scrollback")
					}
				}
			}
		})
	}
}

func TestCellReflowCursor(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
		initial, resized  int
	}{
		{"new_pending_wrap", "abcdef", "abcdef!", 10, 3},
		{"existing_pending_wrap", "abcdefghij", "abcdefghij!", 10, 5},
		{"pending_wrap_widened", "abcde", "abcde!", 5, 10},
		{"cursor_within_text", "abcdef\r\x1b[3C", "abc!ef", 10, 3},
		{"wide_pending_wrap", "abc界", "abc界!", 5, 3},
		{"trailing_prompt_space", "$ ", "$ !", 10, 5},
		{"blank_line", "abc\r\n", "abc\n!", 10, 3},
		{"cursor_addressed_blank", "\x1b[3;7H", "\n\n      !", 10, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := regressionPane(tc.initial, 10)
			p.captureAndWrite([]byte(tc.input))
			p.resize(0, 0, tc.resized+1, 10)
			p.captureAndWrite([]byte("!"))
			if got := paneLogicalText(p); got != tc.want {
				t.Fatalf("got %q, want %q; cursor=%+v", got, tc.want, p.term.Cursor())
			}
		})
	}
}

func TestCellReflowPreservesGlyphs(t *testing.T) {
	p := regressionPane(12, 4)
	p.captureAndWrite([]byte("\x1b[2;3;4:3;9;53;38;2;1;2;3;48;2;4;5;6;58;2;7;8;9m\x1b]8;;https://example.org\a界é\x1b]8;;\a\x1b[0m"))
	wantWide, wantComb := p.term.RawCell(0, 0), p.term.RawCell(2, 0)
	for _, cols := range []int{2, 12} {
		p.resize(0, 0, cols+1, 4)
		gotWide := p.term.RawCell(0, 0)
		x, y := 2, 0
		if cols == 2 {
			x, y = 0, 1
		}
		gotComb := p.term.RawCell(x, y)
		gotWide.Mode &^= vt10x.AttrWrap
		gotComb.Mode &^= vt10x.AttrWrap
		if gotWide != wantWide || gotComb != wantComb {
			t.Fatalf("width %d lost glyph metadata: %+v %+v", cols, gotWide, gotComb)
		}
	}
}

func TestCellReflowHistoryLimit(t *testing.T) {
	for _, limit := range []int{0, 3} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			p := regressionPane(20, 2)
			p.scrollbackLines = limit
			p.sb = sbRing{maxLines: limit}
			p.captureAndWrite([]byte("abcdefghijklmnopqrst"))
			p.resize(0, 0, 3, 2)
			if p.sb.count != limit {
				t.Fatalf("scrollback=%d, want %d", p.sb.count, limit)
			}
			p.captureAndWrite([]byte("!"))
			if !strings.HasSuffix(paneLogicalText(p), "mnopqrst!") && limit == 3 {
				t.Fatalf("tail lost: %q", paneLogicalText(p))
			}
		})
	}
}

func TestCellReflowDoesNotResurrectErasedText(t *testing.T) {
	p := regressionPane(80, 10)
	p.captureAndWrite([]byte("discard me\x1b[H\x1b[2Jkeep me"))
	for _, cols := range []int{20, 100, 40} {
		p.resize(0, 0, cols+1, 10)
		if got := paneLogicalText(p); got != "keep me" {
			t.Fatalf("width %d: %q", cols, got)
		}
	}
}

func TestCellReflowKeepsCursorVisible(t *testing.T) {
	p := regressionPane(20, 4)
	p.captureAndWrite([]byte("first line\r\nsecond line\r\nthird line\r\nfourth line\x1b[H"))
	p.resize(0, 0, 6, 4)
	p.captureAndWrite([]byte("!"))
	if got := p.term.Cell(0, 0).Char; got != '!' {
		t.Fatalf("cursor output = %q", got)
	}
	if got := paneLogicalText(p); !strings.HasPrefix(got, "!irst line\n") {
		t.Fatalf("resize archived cursor's line: %q", got)
	}
}

func TestCellReflowPreservesStyledBlankRows(t *testing.T) {
	p := regressionPane(8, 4)
	p.captureAndWrite([]byte("\x1b[4;1H\x1b[48;2;10;20;30m  \x1b[0m\x1b[H"))
	p.resize(0, 0, 5, 4)
	if got := p.term.Cell(0, 3).BG; got != vt10x.Color(10<<16|20<<8|30) {
		t.Fatalf("blank background lost: %v", got)
	}
}

func TestCellReflowOneColumn(t *testing.T) {
	p := regressionPane(5, 6)
	p.captureAndWrite([]byte("a界b"))
	p.resize(0, 0, 2, 6)
	p.captureAndWrite([]byte("!"))
	if got := paneLogicalText(p); got != "a�b!" {
		t.Fatalf("one column: %q", got)
	}
}

func TestReflowScrollbackPaddingUsesDefaultColours(t *testing.T) {
	p := &Pane{scrollbackLines: 100, sb: sbRing{maxLines: 100}}
	p.term = vt10x.New(vt10x.WithSize(20, 4), vt10x.WithScrollCallback(p.onScrollRow))
	for i := range 8 {
		p.captureAndWrite(fmt.Appendf(nil, "line %d\r\n", i))
	}
	for _, cols := range []int{30, 12} {
		p.resizeAndReflow(cols, 4)
		if p.sb.count == 0 {
			t.Fatal("reflow left no scrollback")
		}
		for r := range p.sb.count {
			for c, g := range p.sb.get(r) {
				if g.FG != vt10x.DefaultFG || g.BG != vt10x.DefaultBG || g.UL != vt10x.DefaultUL {
					t.Fatalf("cols %d: sb[%d][%d] = %+v, want default colours", cols, r, c, g)
				}
			}
		}
	}
}
