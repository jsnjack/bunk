// clipboard.go - write text to the system clipboard.
//
// Strategy (in order):
//  1. OSC 52: the host terminal sets its own clipboard directly.  Works in
//     Kitty, Alacritty, foot, WezTerm, xterm (with allowWindowOps), and many
//     others.  Routed through the render loop's oscBuf so it is emitted to
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
	"os"
	"os/exec"
	"strings"

	"bunk/internal/vt10x"
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
	app.oscBuf.append([]byte(osc))

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

// pasteFromClipboard reads the system clipboard and writes it to the active
// pane's PTY, wrapping in bracketed-paste markers if the pane has opted in via
// DECSET 2004.  Text is pasted directly; images are saved to a temp file and
// the path is pasted instead.  Returns false only when the clipboard is empty
// or contains unsupported data, so the caller can forward the raw key.
func (app *App) pasteFromClipboard() bool {
	app.mu.Lock()
	active := app.active
	app.mu.Unlock()
	if active == nil || active.isDead() {
		return true // consumed, nothing useful to forward
	}

	text := readClipboard()
	if text == "" {
		// No text — check for image data and save to a temp file.
		// Terminal apps can't receive raw image data; they need a file path.
		if path := saveClipboardImage(); path != "" {
			text = path
		} else {
			return false
		}
	}

	// Normalize line endings to \r — the terminal convention for "Enter".
	// The PTY line discipline (icrnl) converts \r → \n for the shell.
	// Raw \n bypasses icrnl, and \r\n produces double newlines.
	text = strings.ReplaceAll(text, "\r\n", "\r")
	text = strings.ReplaceAll(text, "\n", "\r")

	active.mu.Lock()
	bracketed := active.term.Mode()&vt10x.ModeSetPaste != 0
	active.mu.Unlock()

	if bracketed {
		active.writeInput([]byte("\x1b[200~"))
	}
	active.writeInput([]byte(text))
	if bracketed {
		active.writeInput([]byte("\x1b[201~"))
	}
	return true
}

// readClipboard returns the current text clipboard contents using native tools.
// Returns empty string if no tool is available, clipboard is empty, or
// clipboard contains only non-text data (e.g. screenshot).
func readClipboard() string {
	// Wayland — "text" is a special type that matches any text/* MIME,
	// so image/png data is never returned as raw binary.
	if out, err := exec.Command("wl-paste", "--no-newline", "--type", "text").Output(); err == nil {
		return string(out)
	}
	// X11 - xclip (defaults to UTF8_STRING target, text only)
	if out, err := exec.Command("xclip", "-selection", "clipboard", "-o").Output(); err == nil {
		return string(out)
	}
	// X11 - xsel (text-only by default)
	if out, err := exec.Command("xsel", "--clipboard", "--output").Output(); err == nil {
		return string(out)
	}
	return ""
}

// saveClipboardImage saves image data from the clipboard to a temp file and
// returns the file path.  Returns "" if the clipboard has no image data.
// This allows terminal apps (which can't receive raw image bytes) to handle
// image paste via the file path.
func saveClipboardImage() string {
	// Wayland
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		return saveClipboardImageWl()
	}
	// X11 — xclip can read image targets.
	return saveClipboardImageX11()
}

func saveClipboardImageWl() string {
	// Check available MIME types.
	out, err := exec.Command("wl-paste", "--list-types").Output()
	if err != nil {
		return ""
	}
	// Find the best image type.
	var mime string
	for _, t := range strings.Split(string(out), "\n") {
		t = strings.TrimSpace(t)
		if strings.HasPrefix(t, "image/") {
			mime = t
			break
		}
	}
	if mime == "" {
		return ""
	}

	ext := ".png"
	if strings.HasSuffix(mime, "/jpeg") {
		ext = ".jpg"
	} else if strings.HasSuffix(mime, "/webp") {
		ext = ".webp"
	}

	f, err := os.CreateTemp("", "bunk-paste-*"+ext)
	if err != nil {
		return ""
	}
	cmd := exec.Command("wl-paste", "--no-newline", "--type", mime)
	cmd.Stdout = f
	runErr := cmd.Run()
	f.Close()
	if runErr != nil {
		os.Remove(f.Name())
		return ""
	}
	return f.Name()
}

func saveClipboardImageX11() string {
	// Query available targets.
	out, err := exec.Command("xclip", "-selection", "clipboard", "-o", "-target", "TARGETS").Output()
	if err != nil {
		return ""
	}
	var target string
	for _, t := range strings.Split(string(out), "\n") {
		t = strings.TrimSpace(t)
		if strings.HasPrefix(t, "image/") {
			target = t
			break
		}
	}
	if target == "" {
		return ""
	}

	ext := ".png"
	if strings.HasSuffix(target, "/jpeg") {
		ext = ".jpg"
	}

	f, err := os.CreateTemp("", "bunk-paste-*"+ext)
	if err != nil {
		return ""
	}
	cmd := exec.Command("xclip", "-selection", "clipboard", "-o", "-target", target)
	cmd.Stdout = f
	runErr := cmd.Run()
	f.Close()
	if runErr != nil {
		os.Remove(f.Name())
		return ""
	}
	return f.Name()
}

// tryClipboardCmd runs cmd with text piped to stdin and returns true on success.
func tryClipboardCmd(cmd *exec.Cmd, text string) bool {
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run() == nil
}
