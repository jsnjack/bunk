package main

import (
	"github.com/diamondburned/gotk4/pkg/gdk/v4"

	"bunk/internal/input"
)

// keyNames maps GDK keyvals of non-text keys to input keys. Keypad keys
// carry input.ModKeypad so application keypad mode and the Kitty protocol
// can tell them apart.
var keyNames = map[uint]struct {
	key    input.Key
	keypad bool
}{
	gdk.KEY_Return: {input.KeyEnter, false}, gdk.KEY_KP_Enter: {input.KeyEnter, true},
	gdk.KEY_Tab: {input.KeyTab, false}, gdk.KEY_KP_Tab: {input.KeyTab, true},
	gdk.KEY_ISO_Left_Tab: {input.KeyBacktab, false},
	gdk.KEY_BackSpace:    {input.KeyBackspace, false}, // sends DEL
	gdk.KEY_Escape:       {input.KeyEscape, false},
	gdk.KEY_Up:           {input.KeyUp, false}, gdk.KEY_KP_Up: {input.KeyUp, true},
	gdk.KEY_Down: {input.KeyDown, false}, gdk.KEY_KP_Down: {input.KeyDown, true},
	gdk.KEY_Left: {input.KeyLeft, false}, gdk.KEY_KP_Left: {input.KeyLeft, true},
	gdk.KEY_Right: {input.KeyRight, false}, gdk.KEY_KP_Right: {input.KeyRight, true},
	gdk.KEY_Home: {input.KeyHome, false}, gdk.KEY_KP_Home: {input.KeyHome, true},
	gdk.KEY_End: {input.KeyEnd, false}, gdk.KEY_KP_End: {input.KeyEnd, true},
	gdk.KEY_Page_Up: {input.KeyPgUp, false}, gdk.KEY_KP_Page_Up: {input.KeyPgUp, true},
	gdk.KEY_Page_Down: {input.KeyPgDn, false}, gdk.KEY_KP_Page_Down: {input.KeyPgDn, true},
	gdk.KEY_Insert: {input.KeyInsert, false}, gdk.KEY_KP_Insert: {input.KeyInsert, true},
	gdk.KEY_Delete: {input.KeyDelete, false}, gdk.KEY_KP_Delete: {input.KeyDelete, true},
	gdk.KEY_Clear: {input.KeyClear, false}, gdk.KEY_KP_Begin: {input.KeyClear, true},
}

// keyEvent maps a GDK key press to an input event. Text keys use
// gdk_keyval_to_unicode, so the rune already reflects Shift and the layout.
// IME composition is not handled yet (milestone 2).
func keyEvent(keyval uint, state gdk.ModifierType) (input.Event, bool) {
	var mod input.Mod
	if state&gdk.ShiftMask != 0 {
		mod |= input.ModShift
	}
	if state&gdk.ControlMask != 0 {
		mod |= input.ModCtrl
	}
	if state&gdk.AltMask != 0 {
		mod |= input.ModAlt
	}
	if state&(gdk.SuperMask|gdk.MetaMask) != 0 {
		mod |= input.ModMeta
	}
	if k, ok := keyNames[keyval]; ok {
		if k.keypad {
			mod |= input.ModKeypad
		}
		return input.Event{Key: k.key, Mod: mod}, true
	}
	if keyval >= gdk.KEY_F1 && keyval <= gdk.KEY_F24 {
		return input.Event{Key: input.KeyF(int(keyval-gdk.KEY_F1) + 1), Mod: mod}, true
	}
	if keyval >= gdk.KEY_KP_Space && keyval <= gdk.KEY_KP_Equal {
		mod |= input.ModKeypad // digits and operators on the numeric keypad
	}
	r := rune(gdk.KeyvalToUnicode(keyval))
	if r == 0 {
		return input.Event{}, false // modifier keys, dead keys, media keys
	}
	return input.Event{Key: input.KeyRune, Rune: r, Mod: mod}, true
}

// ctrlByte returns the legacy control byte for Ctrl plus a non-letter key
// (Ctrl+Space, Ctrl+[ ...), which input.EncodeKey sends as the plain rune.
func ctrlByte(ev input.Event) (byte, bool) {
	if ev.Key != input.KeyRune || ev.Mod&input.ModCtrl == 0 || ev.Mod&input.ModAlt != 0 {
		return 0, false
	}
	switch ev.Rune {
	case ' ', '@', '2':
		return 0x00, true
	case '[', '3':
		return 0x1b, true
	case '\\', '4':
		return 0x1c, true
	case ']', '5':
		return 0x1d, true
	case '^', '6', '~':
		return 0x1e, true
	case '_', '7', '/', '-':
		return 0x1f, true
	case '8', '?':
		return 0x7f, true
	}
	return 0, false
}
