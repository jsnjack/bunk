package main

import (
	"bunk/internal/input"
	"bunk/internal/session"
	"bunk/internal/vt10x"

	"github.com/gdamore/tcell/v2"
)

// keyToBytes encodes a tcell key event for a pane with the given Kitty
// keyboard flags and no other input modes.
func keyToBytes(ev *tcell.EventKey, kittyFlags int) []byte {
	return keyToBytesMode(ev, kittyFlags, 0)
}

// keyToBytesMode encodes a tcell key event using the pane's input modes.
func keyToBytesMode(ev *tcell.EventKey, flags int, mode vt10x.ModeFlag) []byte {
	k, ok := tcellKeyEvent(ev)
	if !ok {
		return nil
	}
	return input.EncodeKey(k, session.InputModes(flags, mode))
}

// tcellKeys maps tcell's named keys to input keys. tcell reports Ctrl+H,
// Ctrl+I and Ctrl+M as Backspace (BS), Tab and Enter; other Ctrl+letter
// keys become letters with ModCtrl.
var tcellKeys = map[tcell.Key]input.Key{
	tcell.KeyBackspace: input.KeyBS, tcell.KeyBackspace2: input.KeyBackspace,
	tcell.KeyTab: input.KeyTab, tcell.KeyEnter: input.KeyEnter,
	tcell.KeyBacktab: input.KeyBacktab, tcell.KeyEsc: input.KeyEscape,
	tcell.KeyUp: input.KeyUp, tcell.KeyDown: input.KeyDown,
	tcell.KeyRight: input.KeyRight, tcell.KeyLeft: input.KeyLeft,
	tcell.KeyHome: input.KeyHome, tcell.KeyEnd: input.KeyEnd,
	tcell.KeyPgUp: input.KeyPgUp, tcell.KeyPgDn: input.KeyPgDn,
	tcell.KeyInsert: input.KeyInsert, tcell.KeyDelete: input.KeyDelete,
	tcell.KeyClear: input.KeyClear,
}

// tcellKeyEvent converts a tcell key event. It reports false for keys that
// send nothing.
func tcellKeyEvent(ev *tcell.EventKey) (input.Event, bool) {
	out := input.Event{Mod: tcellMods(ev.Modifiers())}
	switch k := ev.Key(); {
	case k == tcell.KeyRune:
		out.Key, out.Rune = input.KeyRune, ev.Rune()
	case tcellKeys[k] != input.KeyNone:
		out.Key = tcellKeys[k]
	case k >= tcell.KeyCtrlA && k <= tcell.KeyCtrlZ:
		out.Key, out.Rune = input.KeyRune, 'a'+rune(k-tcell.KeyCtrlA)
		out.Mod |= input.ModCtrl
	case k >= tcell.KeyF1 && k <= tcell.KeyF24:
		out.Key = input.KeyF(int(k-tcell.KeyF1) + 1)
	default:
		return input.Event{}, false
	}
	return out, true
}

func tcellMods(m tcell.ModMask) input.Mod {
	var out input.Mod
	for _, pair := range [...]struct {
		from tcell.ModMask
		to   input.Mod
	}{
		{tcell.ModShift, input.ModShift}, {tcell.ModCtrl, input.ModCtrl},
		{tcell.ModAlt, input.ModAlt}, {tcell.ModMeta, input.ModMeta},
		{tcell.ModKeypad, input.ModKeypad},
	} {
		if m&pair.from != 0 {
			out |= pair.to
		}
	}
	return out
}

// tcellButton converts a tcell button mask. Note tcell.Button2 is the right
// button and tcell.Button3 the middle one. Combinations are not reported.
func tcellButton(b tcell.ButtonMask) input.Button {
	switch b {
	case tcell.ButtonNone:
		return input.ButtonNone
	case tcell.Button1:
		return input.ButtonLeft
	case tcell.Button2:
		return input.ButtonRight
	case tcell.Button3:
		return input.ButtonMiddle
	case tcell.WheelUp:
		return input.WheelUp
	case tcell.WheelDown:
		return input.WheelDown
	case tcell.WheelLeft:
		return input.WheelLeft
	case tcell.WheelRight:
		return input.WheelRight
	}
	return input.ButtonOther
}

// mouseToBytes encodes a tcell mouse event. x and y are 1-indexed
// pane-local coordinates.
func mouseToBytes(btn, prevBtn tcell.ButtonMask, mod tcell.ModMask, x, y int, sgr bool) []byte {
	return input.EncodeMouse(tcellButton(btn), tcellButton(prevBtn), tcellMods(mod), x, y, sgr)
}
