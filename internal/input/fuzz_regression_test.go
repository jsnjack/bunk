package input

import "testing"

// Regressions found by FuzzEncodeKey and FuzzEncodeMouse.

// TestEncodeKeyInvalidRune: a rune that is not a Unicode character used to
// produce a negative Kitty code point ("\x1b[-1;3u") or U+FFFD.
func TestEncodeKeyInvalidRune(t *testing.T) {
	for _, r := range []rune{-1, 0xD800, 0x110000} {
		for _, m := range []Modes{{}, {KittyFlags: 1}} {
			for _, mod := range []Mod{0, ModAlt, ModCtrl | ModShift} {
				if b := EncodeKey(Event{Key: KeyRune, Rune: r, Mod: mod}, m); b != nil {
					t.Errorf("rune %d, mod %d, modes %+v: %q, want nothing", r, mod, m, b)
				}
			}
		}
	}
}

// TestEncodeMouseOutsidePane: coordinates are 1-based. Zero or negative ones
// used to produce negative SGR coordinates or X10 bytes below 0x20.
func TestEncodeMouseOutsidePane(t *testing.T) {
	for _, tc := range []struct{ x, y int }{{0, 1}, {1, 0}, {-40, 2}, {3, -1}} {
		for _, sgr := range []bool{false, true} {
			if b := EncodeMouse(ButtonLeft, ButtonNone, 0, tc.x, tc.y, sgr); b != nil {
				t.Errorf("(%d,%d) sgr=%v: %q, want nothing", tc.x, tc.y, sgr, b)
			}
		}
	}
}
