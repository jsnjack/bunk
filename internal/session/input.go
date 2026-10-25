// input.go - input encoding modes taken from the emulator state.
package session

import (
	"bunk/internal/input"
	"bunk/internal/vt10x"
)

// InputModes collects the modes that change how keys are encoded.
func InputModes(kittyFlags int, mode vt10x.ModeFlag) input.Modes {
	return input.Modes{
		KittyFlags: kittyFlags,
		AppCursor:  mode&vt10x.ModeAppCursor != 0,
		AppKeypad:  mode&vt10x.ModeAppKeypad != 0,
	}
}
