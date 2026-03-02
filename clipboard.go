// clipboard.go - write text to the system clipboard.
//
// Strategy (in order):
//  1. OSC 52: the host terminal sets its own clipboard directly.  Works in
//     Kitty, Alacritty, foot, WezTerm, xterm (with allowWindowOps), and many
//     others.  Routed through the render loop's oscCh so it is emitted to
//     os.Stdout just before tcell.Show() - the only safe write window.
//  2. wl-copy  - Wayland clipboard tool (wl-clipboard package).
//  3. xclip    - X11 clipboard tool.
//  4. xsel     - X11 clipboard tool (alternative).
//
// The native tools are attempted in a background goroutine so they never block
// the event loop.  Failures are silently ignored; if none of the tools exist
// and the terminal doesn't support OSC 52, the user simply won't get a copy -
// which is better than crashing.
package main

import (
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"
)

// copyToClipboard copies text to the clipboard via OSC 52 and native tools.
func (app *App) copyToClipboard(text string) {
	if text == "" {
		return
	}

	// OSC 52: \e]52;c;<base64>\a
	// 'c' selects the CLIPBOARD selection (as opposed to primary 'p').
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	osc := fmt.Sprintf("\x1b]52;c;%s\x07", encoded)
	select {
	case app.oscCh <- []byte(osc):
	default:
	}

	// Native clipboard as best-effort fallback.
	go func() {
		if tryClipboardCmd(exec.Command("wl-copy"), text) {
			return
		}
		if tryClipboardCmd(exec.Command("xclip", "-selection", "clipboard"), text) {
			return
		}
		tryClipboardCmd(exec.Command("xsel", "--clipboard", "--input"), text) //nolint:errcheck
	}()
}

// pasteFromClipboard reads the system clipboard and writes the content to the
// active pane's PTY, wrapping it in bracketed-paste markers if the pane has
// opted in via DECSET 2004.  Safe to call from the event loop goroutine.
func (app *App) pasteFromClipboard() {
	app.mu.Lock()
	active := app.active
	app.mu.Unlock()
	if active == nil || active.isDead() {
		return
	}

	text := readClipboard()
	if text == "" {
		return
	}

	active.mu.Lock()
	bracketed := active.wantsBracketedPaste
	active.mu.Unlock()

	if bracketed {
		active.writeInput([]byte("\x1b[200~"))
	}
	active.writeInput([]byte(text))
	if bracketed {
		active.writeInput([]byte("\x1b[201~"))
	}
}

// readClipboard returns the current clipboard contents using native tools.
// Returns empty string if no tool is available or clipboard is empty.
func readClipboard() string {
	// Wayland
	if out, err := exec.Command("wl-paste", "--no-newline").Output(); err == nil {
		return string(out)
	}
	// X11 - xclip
	if out, err := exec.Command("xclip", "-selection", "clipboard", "-o").Output(); err == nil {
		return string(out)
	}
	// X11 - xsel
	if out, err := exec.Command("xsel", "--clipboard", "--output").Output(); err == nil {
		return string(out)
	}
	return ""
}

// tryClipboardCmd runs cmd with text piped to stdin and returns true on success.
func tryClipboardCmd(cmd *exec.Cmd, text string) bool {
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run() == nil
}
