package input

import (
	"fmt"
	"regexp"
	"testing"
	"unicode/utf8"
)

// csiKey matches the CSI forms EncodeKey produces: legacy cursor, tilde and
// function keys, and Kitty CSI u.
var csiKey = regexp.MustCompile(`^\x1b\[[0-9]*(;[0-9]+)?[ABCDFHPQRSZ~u]$`)

// checkKey returns an error unless b is one key's encoding: one character,
// one character after ESC (Alt), a CSI key sequence or an SS3 key.
func checkKey(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	if r, n := utf8.DecodeRune(b); n == len(b) && r != utf8.RuneError {
		return nil
	}
	if b[0] != 0x1b {
		return fmt.Errorf("%q is not one character or escape sequence", b)
	}
	rest := b[1:]
	if r, n := utf8.DecodeRune(rest); n == len(rest) && r != utf8.RuneError {
		return nil // Alt+character, including Alt+[ and Alt+O
	}
	if len(b) == 3 && b[1] == 'O' && (b[2] >= 'A' && b[2] <= 'Z' || b[2] >= 'a' && b[2] <= 'z') {
		return nil
	}
	if csiKey.Match(b) {
		return nil
	}
	return fmt.Errorf("malformed key sequence %q", b)
}

func FuzzEncodeKey(f *testing.F) {
	for _, seed := range []struct {
		key        Key
		r          rune
		mod        Mod
		flags      int
		cur, keypd bool
	}{
		{KeyRune, 'a', 0, 0, false, false}, {KeyRune, 'c', ModCtrl | ModAlt, 0, false, false},
		{KeyRune, 'c', ModCtrl, 1, false, false}, {KeyRune, '7', ModKeypad, 0, false, true},
		{KeyRune, '[', ModAlt, 0, false, false}, {KeyRune, 'é', ModShift | ModAlt, 1, false, false},
		{KeyEnter, 0, ModKeypad, 1, false, true}, {KeyBacktab, 0, ModCtrl, 1, false, false},
		{KeyUp, 0, 0, 0, true, false}, {KeyHome, 0, ModCtrl | ModShift, 0, true, false},
		{KeyF(13), 0, ModCtrl, 0, false, false}, {KeyF(24), 0, 0, 0, false, false},
		{KeyBackspace, 0, ModAlt, 0, false, false}, {KeyClear, 0, ModKeypad, 0, false, true},
		{KeyF(24) + 1, 0, 0, 0, false, false}, {KeyRune, -1, ModAlt, 1, false, false},
	} {
		f.Add(uint8(seed.key), int32(seed.r), uint8(seed.mod), seed.flags, seed.cur, seed.keypd)
	}
	f.Fuzz(func(t *testing.T, key uint8, r int32, mod uint8, flags int, appCursor, appKeypad bool) {
		ev := Event{Key: Key(key), Rune: r, Mod: Mod(mod)}
		m := Modes{KittyFlags: flags, AppCursor: appCursor, AppKeypad: appKeypad}
		b := EncodeKey(ev, m)
		if err := checkKey(b); err != nil {
			t.Fatalf("EncodeKey(%+v, %+v): %v", ev, m, err)
		}
		if ev.Key == KeyRune && utf8.ValidRune(r) && ev.Mod == 0 && string(b) != string(r) {
			t.Fatalf("EncodeKey(%+v, %+v) = %q, want the character itself", ev, m, b)
		}
		if ev.Key >= KeyRune && ev.Key <= KeyF(24) && ev.Key != KeyClear && (ev.Key != KeyRune || utf8.ValidRune(r)) && len(b) == 0 {
			t.Fatalf("EncodeKey(%+v, %+v) sent nothing", ev, m)
		}
	})
}

// sgrMouse matches an SGR (DECSET 1006) mouse report.
var sgrMouse = regexp.MustCompile(`^\x1b\[<([0-9]+);([1-9][0-9]*);([1-9][0-9]*)[Mm]$`)

func FuzzEncodeMouse(f *testing.F) {
	for _, seed := range []struct {
		btn, prev Button
		mod       Mod
		x, y      int
		sgr       bool
	}{
		{ButtonLeft, ButtonNone, 0, 1, 1, true}, {ButtonNone, ButtonLeft, ModShift, 3, 4, false},
		{ButtonRight, ButtonRight, ModAlt | ModCtrl, 223, 223, false}, {WheelUp, WheelUp, 0, 500, 300, true},
		{ButtonNone, ButtonNone, 0, 224, 1, false}, {ButtonOther, ButtonNone, 0, 1, 1, true},
		{ButtonLeft, ButtonNone, 0, 0, 0, false}, {ButtonMiddle, ButtonNone, 0, -40, 2, true},
	} {
		f.Add(uint8(seed.btn), uint8(seed.prev), uint8(seed.mod), seed.x, seed.y, seed.sgr)
	}
	f.Fuzz(func(t *testing.T, btn, prev, mod uint8, x, y int, sgr bool) {
		b := EncodeMouse(Button(btn), Button(prev), Mod(mod), x, y, sgr)
		if len(b) == 0 {
			return
		}
		if x < 1 || y < 1 {
			t.Fatalf("EncodeMouse at (%d,%d) = %q; 1-based coordinates below 1 must send nothing", x, y, b)
		}
		if sgr {
			m := sgrMouse.FindSubmatch(b)
			if m == nil || string(m[2]) != fmt.Sprint(x) || string(m[3]) != fmt.Sprint(y) {
				t.Fatalf("EncodeMouse(%d, %d, %d, %d, %d) = %q, want an SGR report at (%d,%d)", btn, prev, mod, x, y, b, x, y)
			}
			return
		}
		if len(b) != 6 || string(b[:3]) != "\x1b[M" || b[3] < 32 || int(b[4]) != x+32 || int(b[5]) != y+32 {
			t.Fatalf("EncodeMouse(%d, %d, %d, %d, %d) = %q, want an X10 report at (%d,%d)", btn, prev, mod, x, y, b, x, y)
		}
	})
}
