package main

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"bunk/internal/vt10x"
)

// TestScrollbackHandoffWithEviction fills a small ring past capacity, with
// short rows (copied without trailing blanks) and full rows (handed over).
// Stored rows and the spare rows returned to vt10x must never share storage:
// scrollback must keep its text while the screen keeps changing.
func TestScrollbackHandoffWithEviction(t *testing.T) {
	for _, suffix := range []string{"", "!"} {
		t.Run(fmt.Sprintf("suffix %q", suffix), func(t *testing.T) {
			testScrollbackEviction(t, suffix)
		})
	}
}

func testScrollbackEviction(t *testing.T, suffix string) {
	const cols, rows, keep = 6, 2, 3
	p := &Pane{cmd: &exec.Cmd{}, w: cols + 1, h: rows, scrollbackLines: keep, sb: sbRing{maxLines: keep}}
	p.term = vt10x.New(vt10x.WithSize(cols, rows), vt10x.WithScrollCallback(p.onScrollRow))

	text := func(row []vt10x.Glyph) string {
		var b strings.Builder
		for _, g := range row {
			b.WriteRune(g.Char)
		}
		return strings.TrimRight(b.String(), " \x00")
	}
	for i := 0; i < 10; i++ {
		p.captureAndWrite([]byte(fmt.Sprintf("\r\nline%d%s", i, suffix)))
		// After line i is written, lines up to i-2 have left the 2-row screen
		// (the first row of output is the empty line before line0).
		for j := 0; j < p.sb.count; j++ {
			want := fmt.Sprintf("line%d%s", i-1-p.sb.count+j, suffix)
			if i-1-p.sb.count+j < 0 {
				want = ""
			}
			if got := text(p.sb.get(j)); got != want {
				t.Fatalf("after line%d: scrollback[%d] = %q, want %q", i, j, got, want)
			}
		}
	}
	if p.sb.count != keep {
		t.Fatalf("scrollback holds %d rows, want %d", p.sb.count, keep)
	}
	for y, want := range []string{"line8" + suffix, "line9" + suffix} {
		if got := text(captureRow(p.term, y, cols)); got != want {
			t.Fatalf("screen row %d = %q, want %q", y, got, want)
		}
	}
}

// TestScrollbackDropsOnlyPlainBlanks checks which trailing cells a stored
// row may drop: only those that look and copy like a missing cell.
func TestScrollbackDropsOnlyPlainBlanks(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		want         int
	}{
		{"default blanks", "ab", 2},
		{"coloured background", "ab\x1b[44m  \x1b[0m", 4},
		{"erased with colour", "ab\x1b[44m\x1b[K\x1b[0m", 8},
		{"underlined spaces", "ab\x1b[4m  \x1b[0m", 4},
		{"linked spaces", "ab\x1b]8;;https://example.com\x1b\\  \x1b]8;;\x1b\\", 4},
		{"coloured text only", "\x1b[31mab  \x1b[0m", 2},
		{"soft wrap", "abcdefghij", 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := regressionPane(8, 1)
			p.captureAndWrite([]byte(tc.output + "\r\nnext"))
			if p.sb.count == 0 {
				t.Fatal("nothing reached scrollback")
			}
			if got := len(p.sb.get(0)); got != tc.want {
				t.Fatalf("stored row has %d cells, want %d", got, tc.want)
			}
		})
	}
}
