package main

// Colour emoji in the window: a glyph the font draws in colour reaches the
// pane's pixels in its own colours from the atlas's colour page, not in
// the cell's foreground.

import "testing"

// TestGUIColorEmoji prints 🔵 in red text, as a review's "### 🔵 Needs a
// closer look" heading came out, and checks that its two cells hold blue
// pixels and no red ones, while the text after it stays red.
func TestGUIColorEmoji(t *testing.T) {
	t.Parallel()
	g := runGUI(t, scenario{
		args:  []string{"--size", "60x16"},
		files: map[string]string{"emoji.bin": "\x1b[H\x1b[2J\x1b[38;2;255;0;0m\x1b[2;3H\U0001F535 HHHH\x1b[0m\x1b[12;1H"},
		script: []string{
			"wait focused.line=$",
			"type cat emoji.bin\\r",
			"wait focused.text~HHHH focused.line=$ focused.settled=true idle 100ms",
			"snap shown", "dump-pane 0 shown.png",
			"quit",
		},
	})
	p := g.snap("shown").focused(t)
	img := loadPNG(t, g.file("shown.png"))
	cw, ch := p.Cell[0], p.Cell[1]
	count := func(x0, cells int, want func(rgb) bool) int {
		n := 0
		for y := ch; y < 2*ch; y++ {
			for x := x0 * cw; x < (x0+cells)*cw; x++ {
				if want(at(img, x, y)) {
					n++
				}
			}
		}
		return n
	}
	blue := func(c rgb) bool { return c.b > 150 && c.b > c.r+80 }
	red := func(c rgb) bool { return c.r > 150 && c.r > c.b+80 && c.r > c.g+80 }
	if n, area := count(2, 2, blue), 2*cw*ch; n < area/4 {
		t.Errorf("🔵 covers %d blue pixels of its %d", n, area)
	}
	if n := count(2, 2, red); n > 0 {
		t.Errorf("🔵 has %d red pixels: drawn in the foreground colour", n)
	}
	if n := count(5, 4, red); n == 0 {
		t.Error("HHHH after 🔵 has no red pixels")
	}
}
