package main

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"bunk/internal/vt10x"
)

// TestScrollbackHandoffWithEviction fills a small ring past capacity. Rows
// handed over by vt10x and the spare rows returned to it must never share
// storage: scrollback must keep its text while the screen keeps changing.
func TestScrollbackHandoffWithEviction(t *testing.T) {
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
		p.captureAndWrite([]byte(fmt.Sprintf("\r\nline%d", i)))
		// After line i is written, lines up to i-2 have left the 2-row screen
		// (the first row of output is the empty line before line0).
		for j := 0; j < p.sb.count; j++ {
			want := fmt.Sprintf("line%d", i-1-p.sb.count+j)
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
	for y, want := range []string{"line8", "line9"} {
		if got := text(captureRow(p.term, y, cols)); got != want {
			t.Fatalf("screen row %d = %q, want %q", y, got, want)
		}
	}
}
