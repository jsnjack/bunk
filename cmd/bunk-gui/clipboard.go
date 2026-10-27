package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
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

// copySelection puts the selection on the clipboard and flashes the
// Copied badge, as bunk's TUI does. With clear (Ctrl+C) the selection ends,
// so the next Ctrl+C reaches the program. It reports whether anything was
// selected.
func (a *App) copySelection(p *Pane, clear bool) bool {
	text := p.selectionText()
	if text == "" {
		return false
	}
	clipboard(false).SetText(text)
	if clear {
		p.setSelection(false, session.SelPos{}, session.SelPos{})
	}
	p.showFlash(badges.Copied)
	return true
}

// selectAll selects the scrollback and the screen down to the last row
// with text, and puts it on the primary selection, as a drag does.
func (a *App) selectAll(p *Pane) {
	p.s.Lock()
	cols, rows := p.s.Term.Size()
	last := -1 // the last screen row with text
	for y := rows - 1; y >= 0 && last < 0; y-- {
		for _, g := range session.CaptureRow(p.s.Term, y, cols) {
			if g.Char != 0 && g.Char != ' ' {
				last = y
				break
			}
		}
	}
	end := p.s.Scrollback.Count + last
	p.s.Unlock()
	if end < 0 {
		return
	}
	p.setSelection(true, session.SelPos{}, session.SelPos{Row: end, Col: cols - 1})
	a.copyPrimary(p)
}

// copyPrimary puts the selection on the primary selection, as finishing a
// drag or a double click does.
func (a *App) copyPrimary(p *Pane) {
	if text := p.selectionText(); text != "" {
		clipboard(true).SetText(text)
	}
}

// paste reads the clipboard (or the primary selection) and sends it to the
// pane: Ctrl+V, Ctrl+Shift+V and middle click. An image goes as the path of
// a PNG file holding it, as bunk's TUI pastes one: a terminal program
// cannot read image data, and agents attach an image whose path is pasted.
// It reports false when the clipboard holds neither text nor an image.
func (a *App) paste(p *Pane, primary bool) bool {
	cb := clipboard(primary)
	text, image := clipOffers(cb.Formats())
	switch {
	case text:
		cb.ReadTextAsync(context.Background(), func(res gio.AsyncResulter) {
			text, err := cb.ReadTextFinish(res)
			if err != nil || text == "" || p.removeOnce {
				return
			}
			p.paste(text)
		})
	case image:
		cb.ReadTextureAsync(context.Background(), func(res gio.AsyncResulter) {
			tex, err := cb.ReadTextureFinish(res)
			if err != nil || p.removeOnce {
				slog.Debug("paste: read image", "err", err)
				return
			}
			path, err := savePastedImage(gdk.BaseTexture(tex))
			if err != nil {
				a.toast("Could not paste the image")
				slog.Warn("paste: save image", "err", err)
				return
			}
			p.paste(path)
		})
	default:
		return false
	}
	return true
}

// clipOffers reports whether formats offer text and an image. It reads the
// MIME types itself: gotk4's binding of
// gdk_content_formats_union_deserialize_gtypes frees formats twice.
func clipOffers(formats *gdk.ContentFormats) (text, image bool) {
	text = formats.ContainGType(coreglib.TypeString)
	image = formats.ContainGType(gdk.GTypeTexture)
	for _, m := range formats.MIMETypes() {
		text = text || strings.HasPrefix(m, "text/plain")
		image = image || strings.HasPrefix(m, "image/")
	}
	return text, image
}

// savePastedImage writes tex to a new PNG file in the temporary directory
// and returns its path.
func savePastedImage(tex *gdk.Texture) (string, error) {
	f, err := os.CreateTemp("", "bunk-paste-*.png")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		return "", err
	}
	if !tex.SaveToPNG(path) {
		if err := os.Remove(path); err != nil {
			slog.Debug("paste: remove", "path", path, "err", err)
		}
		return "", fmt.Errorf("write %s", path)
	}
	return path, nil
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
