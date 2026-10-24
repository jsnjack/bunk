// Package input encodes keyboard, mouse, focus and paste events as the bytes
// a terminal application reads from its PTY. Events use a toolkit-neutral
// model, so the tcell front end and a GUI front end share one encoder.
package input

import "fmt"

// Mod is a set of modifier keys.
type Mod uint8

const (
	ModShift Mod = 1 << iota
	ModCtrl
	ModAlt
	ModMeta
	ModKeypad // the key is on the numeric keypad
)

// Key identifies a key. KeyRune means the event's Rune holds the character.
type Key uint8

const (
	KeyNone Key = iota
	KeyRune
	KeyEnter
	KeyTab
	KeyBacktab
	KeyBackspace // the Backspace key; sends DEL (0x7f)
	KeyBS        // a host-reported BS (0x08), which may be Backspace or Ctrl+H
	KeyEscape
	KeyUp
	KeyDown
	KeyRight
	KeyLeft
	KeyHome
	KeyEnd
	KeyPgUp
	KeyPgDn
	KeyInsert
	KeyDelete
	KeyClear
	KeyF1 // KeyF1 + n-1 is function key n, up to KeyF24
)

// KeyF returns function key n (1-24).
func KeyF(n int) Key { return KeyF1 + Key(n-1) }

// Event is a key press. Ctrl+letter is KeyRune with ModCtrl.
type Event struct {
	Key  Key
	Rune rune
	Mod  Mod
}

// Modes are the pane's negotiated input modes.
type Modes struct {
	KittyFlags int  // Kitty keyboard protocol flags; bit 0 asks for CSI u
	AppCursor  bool // DECCKM: cursor keys send SS3
	AppKeypad  bool // DECKPAM: keypad keys send SS3
}

// Sequences for focus reports (DECSET 1004) and bracketed paste (DECSET 2004).
const (
	FocusIn    = "\x1b[I"
	FocusOut   = "\x1b[O"
	PasteStart = "\x1b[200~"
	PasteEnd   = "\x1b[201~"
)

// EncodeKey returns the bytes for ev, or nil when the key sends nothing.
func EncodeKey(ev Event, m Modes) []byte {
	if ev.Mod&ModKeypad != 0 {
		if code, ss3, ok := keypadCode(ev); ok {
			if m.KittyFlags&1 != 0 {
				return fmt.Appendf(nil, "\x1b[%d;%du", code, kittyMod(ev.Mod))
			}
			if m.AppKeypad {
				return []byte{'\x1b', 'O', ss3}
			}
		}
	}
	data := encode(ev, m.KittyFlags)
	if m.AppCursor && ev.Mod&^ModKeypad == 0 && len(data) == 3 && data[0] == 27 && data[1] == '[' {
		switch data[2] {
		case 'A', 'B', 'C', 'D', 'H', 'F':
			data[1] = 'O'
		}
	}
	return data
}

// kittyMod is the Kitty and xterm modifier parameter: 1 + shift + alt*2 +
// ctrl*4.
func kittyMod(mod Mod) int {
	m := 1
	if mod&ModShift != 0 {
		m += 1
	}
	if mod&ModAlt != 0 {
		m += 2
	}
	if mod&ModCtrl != 0 {
		m += 4
	}
	return m
}

// ctrlLetter returns the control byte for Ctrl+r, if r is a letter.
func ctrlLetter(r rune) (byte, bool) {
	switch {
	case r >= 'a' && r <= 'z':
		return byte(r-'a') + 1, true
	case r >= 'A' && r <= 'Z':
		return byte(r-'A') + 1, true
	}
	return 0, false
}

func encode(ev Event, kittyFlags int) []byte {
	mod := ev.Mod

	// Kitty keyboard protocol: when the child app has pushed disambiguate
	// mode (flags bit 0), encode keys as CSI u sequences so the app can
	// tell Alt+key from ESC+key, Ctrl+I from Tab, etc.
	useCSIu := kittyFlags&1 != 0
	km := kittyMod(mod)

	k := ev.Key
	if k == KeyRune {
		r := ev.Rune
		if useCSIu && km > 1 {
			return fmt.Appendf(nil, "\x1b[%d;%du", r, km)
		}
		if b, ok := ctrlLetter(r); ok && mod&ModCtrl != 0 {
			if mod&ModAlt != 0 {
				return []byte{'\x1b', b}
			}
			return []byte{b}
		}
		if mod&ModAlt != 0 {
			return append([]byte{'\x1b'}, string(r)...)
		}
		return []byte(string(r))
	}

	// CSI u encoding for special keys whose legacy form is ambiguous.
	if useCSIu {
		cp := 0
		switch k {
		case KeyEnter:
			cp = 13
		case KeyTab:
			cp = 9
		case KeyBacktab:
			cp = 9
			km = 1 + ((km - 1) | 1) // BackTab always implies Shift
		case KeyBS, KeyBackspace:
			cp = 127
		case KeyEscape:
			cp = 27
		}
		if cp > 0 {
			if km > 1 {
				return fmt.Appendf(nil, "\x1b[%d;%du", cp, km)
			}
			return fmt.Appendf(nil, "\x1b[%du", cp)
		}
		// Fall through to legacy for arrows, F-keys, etc. which already
		// have unambiguous CSI representations.
	}

	// Legacy encoding.
	if mod&ModAlt != 0 && (k == KeyBS || k == KeyBackspace || k == KeyEscape || k == KeyEnter || k == KeyTab) {
		plain := ev
		plain.Mod &^= ModAlt
		return append([]byte{27}, encode(plain, 0)...)
	}

	switch k {
	case KeyBS:
		return []byte{'\x08'}
	case KeyBackspace:
		return []byte{'\x7f'}
	case KeyEnter:
		return []byte{'\r'}
	case KeyTab:
		return []byte{'\t'}
	case KeyBacktab: // Shift+Tab
		return []byte{'\x1b', '[', 'Z'}
	case KeyEscape:
		return []byte{'\x1b'}
	}

	// Cursor and editing keys: bare CSI when unmodified, CSI 1;<mod> or
	// <code>;<mod> when modified.
	if final, ok := cursorFinal[k]; ok {
		if km > 1 {
			return fmt.Appendf(nil, "\x1b[1;%d%c", km, final)
		}
		return []byte{'\x1b', '[', final}
	}
	if code, ok := tildeCode[k]; ok {
		if km > 1 {
			return fmt.Appendf(nil, "\x1b[%d;%d~", code, km)
		}
		return fmt.Appendf(nil, "\x1b[%d~", code)
	}
	if k >= KeyF1 && k <= KeyF(4) {
		final := "PQRS"[k-KeyF1]
		if km > 1 {
			return fmt.Appendf(nil, "\x1b[1;%d%c", km, final)
		}
		return []byte{'\x1b', 'O', final}
	}
	// F13–F24 are the standard xterm Shift+F1–F12 encodings. A host may
	// report Shift+F1 as F1 with Shift or as F13; both send the same bytes.
	if k >= KeyF(13) && k <= KeyF(24) {
		return encode(Event{Key: k - 12, Mod: ModShift}, 0)
	}
	return nil
}

var cursorFinal = map[Key]byte{
	KeyUp: 'A', KeyDown: 'B', KeyRight: 'C', KeyLeft: 'D', KeyHome: 'H', KeyEnd: 'F',
}

var tildeCode = map[Key]int{
	KeyInsert: 2, KeyDelete: 3, KeyPgUp: 5, KeyPgDn: 6,
	KeyF(5): 15, KeyF(6): 17, KeyF(7): 18, KeyF(8): 19,
	KeyF(9): 20, KeyF(10): 21, KeyF(11): 23, KeyF(12): 24,
}

// keypadCode returns the Kitty code point and SS3 final byte for a keypad
// key.
func keypadCode(ev Event) (int, byte, bool) {
	for _, key := range []struct {
		key  Key
		code int
		ss3  byte
	}{
		{KeyLeft, 57417, 't'}, {KeyRight, 57418, 'v'},
		{KeyUp, 57419, 'x'}, {KeyDown, 57420, 'r'},
		{KeyPgUp, 57421, 'y'}, {KeyPgDn, 57422, 's'},
		{KeyHome, 57423, 'w'}, {KeyEnd, 57424, 'q'},
		{KeyInsert, 57425, 'p'}, {KeyDelete, 57426, 'n'}, {KeyClear, 57427, 'u'},
		{KeyEnter, 57414, 'M'},
	} {
		if ev.Key == key.key {
			return key.code, key.ss3, true
		}
	}
	if ev.Key != KeyRune {
		return 0, 0, false
	}
	r := ev.Rune
	if r >= '0' && r <= '9' {
		return 57399 + int(r-'0'), byte('p' + r - '0'), true
	}
	for _, key := range []struct {
		ch   rune
		code int
		ss3  byte
	}{
		{'.', 57409, 'n'}, {'/', 57410, 'o'}, {'*', 57411, 'j'},
		{'-', 57412, 'm'}, {'+', 57413, 'k'}, {'=', 57415, 'X'}, {',', 57416, 'l'},
	} {
		if r == key.ch {
			return key.code, key.ss3, true
		}
	}
	return 0, 0, false
}

// Button is a mouse button or wheel direction.
type Button uint8

const (
	ButtonNone Button = iota // no button: a release or pure motion
	ButtonLeft
	ButtonMiddle
	ButtonRight
	WheelUp
	WheelDown
	WheelLeft
	WheelRight
	ButtonOther // a button or combination that is not reported
)

// EncodeMouse returns the report for a mouse event at 1-based pane
// coordinates x, y, or nil when nothing is reported. A release is btn ==
// ButtonNone after a real button; sgr selects SGR (1006) over X10 encoding.
func EncodeMouse(btn, prevBtn Button, mod Mod, x, y int, sgr bool) []byte {
	release := btn == ButtonNone && prevBtn != ButtonNone

	// Which button is relevant for this event?
	activebtn := btn
	if release {
		activebtn = prevBtn
	}

	var cb int
	switch activebtn {
	case ButtonLeft:
		cb = 0
	case ButtonMiddle:
		cb = 1
	case ButtonRight:
		cb = 2
	case WheelUp:
		cb = 64
	case WheelDown:
		cb = 65
	case WheelLeft:
		cb = 66
	case WheelRight:
		cb = 67
	case ButtonNone:
		cb = 35 // pure motion: base code 3 + 32
	default:
		return nil
	}

	if mod&ModShift != 0 {
		cb |= 4
	}
	if mod&ModAlt != 0 {
		cb |= 8
	}
	if mod&ModCtrl != 0 {
		cb |= 16
	}

	// Set the motion bit (32) for button-held drag events.
	// ANSI protocol: bit 5 indicates "motion" — same button as last event,
	// not a fresh press.  Without this, every drag position looks like a
	// new press and apps restart selection at each intermediate point.
	// Pure motion (ButtonNone) already has it embedded in cb=35 above.
	// Wheel events don't need it — they never produce drag sequences.
	isWheel := activebtn >= WheelUp && activebtn <= WheelRight
	if !release && !isWheel && activebtn != ButtonNone && prevBtn == btn {
		cb |= 32
	}

	if sgr {
		final := 'M'
		if release {
			final = 'm'
		}
		return fmt.Appendf(nil, "\x1b[<%d;%d;%d%c", cb, x, y, final)
	}

	// X10/normal encoding - coordinates capped at 223 (byte limit).
	if x > 223 || y > 223 {
		return nil
	}
	if release {
		cb = 3
	}
	return []byte{'\x1b', '[', 'M', byte(cb + 32), byte(x + 32), byte(y + 32)}
}
