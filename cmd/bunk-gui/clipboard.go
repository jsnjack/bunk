package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"strings"
	"unicode/utf8"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"

	"bunk/internal/badges"
	"bunk/internal/input"
	"bunk/internal/session"
	"bunk/internal/vt10x"
)

func clipboard(primary bool) *gdk.Clipboard {
	d := gdk.DisplayGetDefault()
	if primary {
		return d.PrimaryClipboard()
	}
	return d.Clipboard()
}

func (p *Pane) selectionText() string {
	p.s.Lock()
	defer p.s.Unlock()
	return p.s.SelText()
}

// copySelection puts the selection on the clipboard (Ctrl+Shift+C) and
// flashes the Copied badge, as bunk's TUI does.
func (a *App) copySelection(p *Pane) {
	if text := p.selectionText(); text != "" {
		clipboard(false).SetText(text)
		p.showFlash(badges.Copied)
	}
}

// copyPrimary puts the selection on the primary selection, as finishing a
// drag or a double click does.
func (a *App) copyPrimary(p *Pane) {
	if text := p.selectionText(); text != "" {
		clipboard(true).SetText(text)
	}
}

// paste reads the clipboard (or the primary selection) and sends it to the
// pane: Ctrl+Shift+V and middle click.
func (a *App) paste(p *Pane, primary bool) {
	cb := clipboard(primary)
	cb.ReadTextAsync(context.Background(), func(res gio.AsyncResulter) {
		text, err := cb.ReadTextFinish(res)
		if err != nil || text == "" || p.removeOnce {
			return
		}
		p.paste(text)
	})
}

// paste writes text to the child as one paste.
func (p *Pane) paste(text string) {
	p.s.Lock()
	bracketed := p.s.Term.Mode()&vt10x.ModeSetPaste != 0
	p.s.Unlock()
	p.snapToLive()
	p.write(pasteData(text, bracketed))
}

// pasteData encodes pasted text: line feeds become carriage returns (what
// Enter sends), and with bracketed paste (DECSET 2004) the text is wrapped
// in the paste markers, after removing any end marker it contains so pasted
// text cannot end the paste early.
func pasteData(text string, bracketed bool) []byte {
	text = strings.ReplaceAll(text, "\r\n", "\r")
	text = strings.ReplaceAll(text, "\n", "\r")
	if bracketed {
		text = input.PasteStart + strings.ReplaceAll(text, input.PasteEnd, "") + input.PasteEnd
	}
	return []byte(text)
}

// onOSC is Hooks.OSC, on the PTY reader: an OSC 52 clipboard write from the
// application sets the clipboard. Queries (reads) are ignored.
func (p *Pane) onOSC(seq []byte) {
	text, ok := osc52ClipboardText(seq)
	if !ok {
		return
	}
	coreglib.IdleAdd(func() { p.app.setClipboardFromApp(text) })
}

// setClipboardFromApp sets the clipboard for an OSC 52 write. Wayland only
// lets a focused window own the selection, so a write that arrives while
// the window is inactive waits until it is focused again.
func (a *App) setClipboardFromApp(text string) {
	a.st.osc52++
	if a.win == nil || !a.win.IsActive() {
		a.pendingClip = text
		return
	}
	clipboard(false).SetText(text)
}

// onActive applies an OSC 52 write that arrived while the window was
// inactive.
func (a *App) onActive() {
	if a.win.IsActive() && a.pendingClip != "" {
		clipboard(false).SetText(a.pendingClip)
		a.pendingClip = ""
	}
}

// osc52ClipboardText decodes an OSC 52 clipboard write. It follows bunk's
// clipboard.go: queries, clear requests, binary data and selections other
// than the clipboard are ignored.
func osc52ClipboardText(seq []byte) (string, bool) {
	if len(seq) < 3 || seq[0] != 0x1b || seq[1] != ']' {
		return "", false
	}
	body := session.StripOSCTerminator(seq[2:])
	semi := bytes.IndexByte(body, ';')
	if semi < 0 || string(body[:semi]) != "52" {
		return "", false
	}
	payload := body[semi+1:]
	if !session.IsSafeOSC52(payload) {
		return "", false
	}
	semi = bytes.IndexByte(payload, ';')
	selection, data := payload[:semi], payload[semi+1:]
	if len(data) == 0 || (len(data) == 1 && data[0] == '?') {
		return "", false
	}
	if len(selection) != 0 && bytes.IndexByte(selection, 'c') < 0 {
		return "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(string(data))
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(string(data))
	}
	if err != nil || len(decoded) == 0 || !utf8.Valid(decoded) || bytes.IndexByte(decoded, 0) >= 0 {
		return "", false
	}
	return string(decoded), true
}
