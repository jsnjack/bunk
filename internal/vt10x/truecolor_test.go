package vt10x

import "testing"

// TestTruecolorNeverEqualsPaletteIndex: an RGB colour with zero red and
// green and blue below 256 used to be stored as the number of a palette
// entry, so 38;2;0;0;255 painted palette 255 (grey). The gradients in
// scripts/terminal_features/02_colors.sh show it.
func TestTruecolorNeverEqualsPaletteIndex(t *testing.T) {
	term := New(WithSize(10, 2))
	if _, err := term.Write([]byte("\x1b[38;2;0;0;255;48;2;0;0;1;4;58;2;0;0;3mX\x1b]10;rgb:0000/0000/0909\x07")); err != nil {
		t.Fatal(err)
	}
	g := term.RawCell(0, 0)
	for _, c := range []struct {
		name      string
		got, want Color
	}{
		{"foreground", g.FG, RGB(0, 0, 255)},
		{"background", g.BG, RGB(0, 0, 1)},
		{"underline", g.UL, RGB(0, 0, 3)},
	} {
		if c.got != c.want || c.got < 256 || c.got >= DefaultFG {
			t.Errorf("%s = %#x, want truecolour %#x in [256, DefaultFG)", c.name, c.got, c.want)
		}
	}
	if fg, ok := term.ColorOverride(DefaultFG); !ok || fg != RGB(0, 0, 9) {
		t.Errorf("OSC 10 override = %#x, want %#x", fg, RGB(0, 0, 9))
	}
	if got, want := term.StatusString("m"), "\x1bP1$r0;4:1;38;2;0;0;255;48;2;0;0;1;58;2;0;0;3m\x1b\\"; got != want {
		t.Errorf("DECRQSS SGR = %q, want %q", got, want)
	}
}
