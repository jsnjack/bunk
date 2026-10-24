package input

import "testing"

// Most encodings are covered through bunk's tcell adapter (keys_test.go,
// mouse_test.go). These cases cover events a GUI front end sends directly.
func TestEncodeKeyDirectEvents(t *testing.T) {
	for _, tc := range []struct {
		name string
		ev   Event
		m    Modes
		want string
	}{
		{"backspace sends DEL", Event{Key: KeyBackspace}, Modes{}, "\x7f"},
		{"alt backspace", Event{Key: KeyBackspace, Mod: ModAlt}, Modes{}, "\x1b\x7f"},
		{"kitty backspace", Event{Key: KeyBackspace}, Modes{KittyFlags: 1}, "\x1b[127u"},
		{"ctrl letter", Event{Key: KeyRune, Rune: 'c', Mod: ModCtrl}, Modes{}, "\x03"},
		{"ctrl shift letter", Event{Key: KeyRune, Rune: 'C', Mod: ModCtrl | ModShift}, Modes{}, "\x03"},
		{"ctrl alt letter", Event{Key: KeyRune, Rune: 'x', Mod: ModCtrl | ModAlt}, Modes{}, "\x1b\x18"},
		{"kitty ctrl letter", Event{Key: KeyRune, Rune: 'c', Mod: ModCtrl}, Modes{KittyFlags: 1}, "\x1b[99;5u"},
		{"ctrl digit stays a digit", Event{Key: KeyRune, Rune: '1', Mod: ModCtrl}, Modes{}, "1"},
		{"unicode rune", Event{Key: KeyRune, Rune: 'é'}, Modes{}, "é"},
		{"app cursor", Event{Key: KeyUp}, Modes{AppCursor: true}, "\x1bOA"},
		{"app cursor ignores modified keys", Event{Key: KeyUp, Mod: ModCtrl}, Modes{AppCursor: true}, "\x1b[1;5A"},
		{"keypad digit", Event{Key: KeyRune, Rune: '7', Mod: ModKeypad}, Modes{AppKeypad: true}, "\x1bOw"},
		{"keypad enter kitty", Event{Key: KeyEnter, Mod: ModKeypad}, Modes{KittyFlags: 1}, "\x1b[57414;1u"},
		{"f13 is shift f1", Event{Key: KeyF(13)}, Modes{}, "\x1b[1;2P"},
		{"f12 with ctrl", Event{Key: KeyF(12), Mod: ModCtrl}, Modes{}, "\x1b[24;5~"},
		{"clear sends nothing", Event{Key: KeyClear}, Modes{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(EncodeKey(tc.ev, tc.m)); got != tc.want {
				t.Fatalf("EncodeKey(%+v, %+v) = %q, want %q", tc.ev, tc.m, got, tc.want)
			}
		})
	}
}

func TestEncodeMouseDirectEvents(t *testing.T) {
	for _, tc := range []struct {
		name      string
		btn, prev Button
		mod       Mod
		sgr       bool
		want      string
	}{
		{"middle press", ButtonMiddle, ButtonNone, 0, true, "\x1b[<1;3;4M"},
		{"right drag", ButtonRight, ButtonRight, ModAlt, true, "\x1b[<42;3;4M"},
		{"left release x10", ButtonNone, ButtonLeft, 0, false, "\x1b[M##$"},
		{"unknown button", ButtonOther, ButtonNone, 0, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(EncodeMouse(tc.btn, tc.prev, tc.mod, 3, 4, tc.sgr)); got != tc.want {
				t.Fatalf("EncodeMouse = %q, want %q", got, tc.want)
			}
		})
	}
}
