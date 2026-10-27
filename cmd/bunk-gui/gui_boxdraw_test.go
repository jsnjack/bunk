package main

// Box drawing in the window: the characters boxdraw draws reach the
// pane's pixels exactly as its masks, so blocks fill their cells and
// lines run on from cell to cell without seams.

import (
	"fmt"
	"strings"
	"testing"

	"bunk/internal/boxdraw"
)

// boxArt is printed from the third column of the second row: Claude
// Code's logo, frames in light, heavy, double and rounded lines, blocks,
// shades, braille, wedges and a powerline cap.
var boxArt = []string{
	" ▐▛███▜▌",
	"▝▜█████▛▘",
	"  ▘▘ ▝▝",
	"╔═╦═╗ ┌─┬─┐ ┏━┳━┓ ╭─┬─╮",
	"║ ║ ║ │ │ │ ┃ ┃ ┃ │ │ │",
	"╠═╬═╣ ├─┼─┤ ┣━╋━┫ ├─┼─┤",
	"╚═╩═╝ └─┴─┘ ┗━┻━┛ ╰─┴─╯",
	"▀▄▌▐░▒▓█ ⣿⡇⢸ 🬼🭇🬽🭈 ",
}

// TestGUIBoxDrawing prints boxArt in one colour and compares every pixel
// of its cells in the pane's dump with boxdraw's mask blended from the
// theme's background to that colour.
func TestGUIBoxDrawing(t *testing.T) {
	t.Parallel()
	fg := rgb{255, 96, 0}
	var b strings.Builder
	fmt.Fprintf(&b, "\x1b[H\x1b[2J\x1b[38;2;%d;%d;%dm", fg.r, fg.g, fg.b)
	for i, l := range boxArt {
		fmt.Fprintf(&b, "\x1b[%d;3H%s", 2+i, l)
	}
	b.WriteString("\x1b[0m\x1b[12;1H")
	g := runGUI(t, scenario{
		args:  []string{"--size", "60x16"},
		files: map[string]string{"art.bin": b.String()},
		script: []string{
			"wait focused.line=$",
			"type cat art.bin\\r",
			"wait focused.text~╚═╩═╝ focused.line=$ focused.settled=true idle 100ms",
			"snap shown", "dump-pane 0 shown.png",
			"quit",
		},
	})
	p := g.snap("shown").focused(t)
	img := loadPNG(t, g.file("shown.png"))
	cw, ch := p.Cell[0], p.Cell[1]
	bg := hex(testTheme.Background.Hex())
	mask := make([]byte, cw*ch)
	bad := 0
	for row, line := range boxArt {
		for col, r := range []rune(line) {
			if !boxdraw.Draw(r, mask, cw, cw, ch, boxdraw.LineWidth(cw)) {
				continue
			}
			ox, oy := (2+col)*cw, (1+row)*ch
			for y := range ch {
				for x := range cw {
					a := int(mask[y*cw+x])
					want := rgb{bg.r + (fg.r-bg.r)*a/255, bg.g + (fg.g-bg.g)*a/255, bg.b + (fg.b-bg.b)*a/255}
					if got := at(img, ox+x, oy+y); got.dist(want) > 2 {
						if bad < 5 {
							t.Errorf("%c at column %d, row %d: pixel %d,%d is %v, want %v (coverage %d)", r, 2+col, 1+row, x, y, got, want, a)
						}
						bad++
					}
				}
			}
		}
	}
	if bad > 0 {
		t.Errorf("%d pixels differ from boxdraw's masks (cells %dx%d)", bad, cw, ch)
	}
	// The logo's middle row is solid from its first full block to its
	// last: no seam between the cells.
	y := 2*ch + ch/2
	for x := 3 * cw; x < 10*cw; x++ {
		if got := at(img, x, y); got != fg {
			t.Fatalf("logo row at x %d is %v, want %v: a seam", x, got, fg)
		}
	}
}
