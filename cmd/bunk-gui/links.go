package main

// Links: OSC 8 hyperlinks stored on cells, and http, https and file URLs
// found in plain text (also across soft-wrapped rows).
//
//   - While Ctrl is held, the link under the pointer is underlined and the
//     pointer becomes a hand. Ctrl+click opens it.
//   - When the application reports the mouse, plain and Ctrl clicks go to
//     it; Ctrl+Shift+click opens the link.
//   - Links open through gtk.UriLauncher (the portal) behind openURL, which
//     tests replace. The launcher never runs on the test compositor.
//
// Hit tests read the rows the pane last drew (termView.lines); only an
// OSC 8 URL needs the session lock. New output clears the underline. A
// link sweep since those rows were copied may have given their link IDs
// to other URLs; such a link does not resolve until the next frame.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"unicode"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"bunk/internal/session"
	"bunk/internal/vt10x"
)

// linkSpan is a run of cells of a visible row: columns x0 to x1.
type linkSpan struct{ y, x0, x1 int }

// linkHit is a link and the visible cells that show it.
type linkHit struct {
	url   string
	spans []linkSpan
}

// linkSchemes are the schemes a link may open.
var linkSchemes = map[string]bool{"http": true, "https": true, "file": true, "mailto": true, "ftp": true}

// textSchemes start URLs recognised in plain text.
var textSchemes = []string{"https://", "http://", "file://"}

// openURL opens a link the user clicked. Tests replace it. It records the
// link in --report and launches it only off the test compositor.
var openURL = func(a *App, uri string) {
	a.rep.Opened = append(a.rep.Opened, uri)
	fmt.Printf("open: %s\n", uri)
	if err := launchRefused(); err != nil {
		slog.Debug("link not launched", "url", uri, "err", err)
		return
	}
	l := gtk.NewURILauncher(uri)
	l.Launch(context.Background(), &a.win.Window, func(res gio.AsyncResulter) {
		if err := l.LaunchFinish(res); err != nil {
			a.toast("Could not open " + uri)
			slog.Debug("open link", "url", uri, "err", err)
		}
	})
}

// launchRefused keeps the real launcher off the test compositor, as
// headlessOnly keeps input injection on it.
func launchRefused() error {
	if where := testCompositor(); where != "" {
		return errors.New("nothing is launched on " + where)
	}
	return nil
}

// testCompositor names the headless test compositor bunk-gui runs on, or
// returns "" on a desktop. Links, new windows and desktop notifications
// never leave it.
func testCompositor() string {
	if os.Getenv("BUNK_HEADLESS") != "" {
		return "the headless test compositor (BUNK_HEADLESS set)"
	}
	if strings.HasPrefix(os.Getenv("WAYLAND_DISPLAY"), "bunk-headless-") {
		return "a scripts/headless-gui.sh display"
	}
	return ""
}

// linkState is the link under the pointer while Ctrl is held.
type linkState struct {
	url   string
	spans []linkSpan
}

// setHover underlines hit's cells instead of the previous link's.
func (v *termView) setHover(hit linkHit) {
	for _, s := range v.links.spans {
		v.markDirty(s.y)
	}
	v.links.url = hit.url
	v.links.spans = append(v.links.spans[:0], hit.spans...)
	for _, s := range v.links.spans {
		v.markDirty(s.y)
	}
}

func (v *termView) clearHover() {
	if v.links.url != "" {
		v.setHover(linkHit{})
	}
}

// underlineHover underlines the hovered link's cells of row y.
func (v *termView) underlineHover(y int, inst []Instance) {
	for _, s := range v.links.spans {
		if s.y != y {
			continue
		}
		for x := max(0, s.x0); x <= min(s.x1, len(inst)-1); x++ {
			inst[x].Glyph |= flagUnderline
		}
	}
}

// rowWrapped reports whether visible row y continues on the next row.
func (v *termView) rowWrapped(y int) bool {
	line := &v.lines[y]
	if line.Len() == 0 {
		return false
	}
	_, _, _, mode, _, _ := line.CellAt(line.Len() - 1)
	return mode&vt10x.AttrWrap != 0
}

// linkAt finds the link shown at cell (col, row): an OSC 8 link, resolved
// by linkURL (which takes the session lock), or a URL in the text of the
// logical line around the cell.
func (v *termView) linkAt(col, row int, linkURL func(uint16) string) (linkHit, bool) {
	if row < 0 || row >= v.rows || row >= len(v.lines) {
		return linkHit{}, false
	}
	line := &v.lines[row]
	if col < 0 || col >= line.Len() {
		return linkHit{}, false
	}
	if _, _, _, _, w, _ := line.CellAt(col); w == -1 && col > 0 {
		col-- // the continuation of a wide character
	}
	if id := line.Link(col); id != 0 {
		if u := linkURL(id); u != "" {
			if !allowedLink(u) {
				return linkHit{}, false
			}
			return linkHit{url: u, spans: v.linkSpans(id)}, true
		}
	}
	return v.textLinkAt(col, row)
}

// linkSpans returns the visible cells carrying OSC 8 link id.
func (v *termView) linkSpans(id uint16) []linkSpan {
	var spans []linkSpan
	for y := 0; y < min(v.rows, len(v.lines)); y++ {
		line := &v.lines[y]
		start := -1
		for x := 0; x <= line.Len(); x++ {
			in := x < line.Len() && line.Link(x) == id
			if in && start < 0 {
				start = x
			}
			if !in && start >= 0 {
				spans = append(spans, linkSpan{y, start, x - 1})
				start = -1
			}
		}
	}
	return spans
}

// allowedLink reports whether an OSC 8 URL has a scheme a link may open.
func allowedLink(u string) bool {
	p, err := url.Parse(u)
	return err == nil && linkSchemes[strings.ToLower(p.Scheme)]
}

type textCell struct{ y, x, w int }

// textLinkAt finds a URL in the logical line through (col, row): the row
// and the soft-wrapped rows before and after it.
func (v *termView) textLinkAt(col, row int) (linkHit, bool) {
	rows := min(v.rows, len(v.lines))
	start, end := row, row
	for start > 0 && v.rowWrapped(start-1) {
		start--
	}
	for end < rows-1 && v.rowWrapped(end) {
		end++
	}
	var text []rune
	var cells []textCell
	at := -1
	for y := start; y <= end; y++ {
		line := &v.lines[y]
		for x := 0; x < line.Len(); x++ {
			ch, _, _, _, w, _ := line.CellAt(x)
			if w < 0 {
				continue
			}
			if ch == 0 {
				ch = ' '
			}
			if y == row && x <= col && col < x+max(1, int(w)) {
				at = len(text)
			}
			text = append(text, ch)
			cells = append(cells, textCell{y, x, max(1, int(w))})
		}
	}
	if at < 0 {
		return linkHit{}, false
	}
	s, e, ok := findURL(text, at)
	if !ok {
		return linkHit{}, false
	}
	hit := linkHit{url: string(text[s:e])}
	for i := s; i < e; i++ {
		c := cells[i]
		if n := len(hit.spans); n > 0 && hit.spans[n-1].y == c.y {
			hit.spans[n-1].x1 = c.x + c.w - 1
		} else {
			hit.spans = append(hit.spans, linkSpan{c.y, c.x, c.x + c.w - 1})
		}
	}
	return hit, true
}

// findURL returns the bounds of the http, https or file URL in text that
// covers index at.
func findURL(text []rune, at int) (start, end int, ok bool) {
	for i := 0; i < len(text); i++ {
		scheme := ""
		for _, p := range textSchemes {
			if hasRunePrefix(text[i:], p) {
				scheme = p
				break
			}
		}
		if scheme == "" || (i > 0 && (unicode.IsLetter(text[i-1]) || unicode.IsDigit(text[i-1]))) {
			continue
		}
		j := i + len(scheme)
		for j < len(text) && urlRune(text[j]) {
			j++
		}
		j = trimURL(text[i:j]) + i
		if j <= i+len(scheme) {
			continue
		}
		if i <= at && at < j {
			return i, j, true
		}
		i = j - 1
	}
	return 0, 0, false
}

func hasRunePrefix(text []rune, p string) bool {
	if len(text) < len(p) {
		return false
	}
	for i, r := range p {
		if unicode.ToLower(text[i]) != r {
			return false
		}
	}
	return true
}

// urlRune reports whether r may appear in a URL in terminal text.
func urlRune(r rune) bool {
	if r <= ' ' || r == 0x7f || unicode.IsSpace(r) || unicode.IsControl(r) {
		return false
	}
	return !strings.ContainsRune("<>\"'`{}|\\^", r)
}

// trimURL drops trailing punctuation that usually ends a sentence, and a
// closing bracket the URL did not open. It returns the new length.
func trimURL(u []rune) int {
	n := len(u)
	for n > 0 {
		switch r := u[n-1]; r {
		case '.', ',', ';', ':', '!', '?':
			n--
			continue
		case ')', ']':
			open := '('
			if r == ']' {
				open = '['
			}
			opens, closes := 0, 0
			for _, c := range u[:n] {
				if c == open {
					opens++
				} else if c == r {
					closes++
				}
			}
			if closes > opens {
				n--
				continue
			}
		}
		break
	}
	return n
}

// linkURL returns the URL of OSC 8 link id of the copied rows, or "" when
// a link sweep ran since they were copied.
func (v *termView) linkURL(s *session.Session, id uint16) string {
	s.Lock()
	defer s.Unlock()
	if s.Term.LinkGen() != v.linkGen {
		return ""
	}
	return s.Term.Link(id)
}

// linkAt is termView.linkAt with the session's hyperlink table.
func (p *Pane) linkAt(col, row int) (linkHit, bool) {
	return p.tv.linkAt(col, row, func(id uint16) string { return p.tv.linkURL(p.s, id) })
}

// updateHover underlines the link under the pointer while Ctrl is held and
// shows the hand pointer over it.
func (p *Pane) updateHover(x, y float64, state gdk.ModifierType) {
	var hit linkHit
	if state&gdk.ControlMask != 0 && !p.selecting && p.pointerIn {
		col, row := p.cellAt(x, y)
		hit, _ = p.linkAt(col, row)
	}
	p.setHover(hit)
}

func (p *Pane) setHover(hit linkHit) {
	if hit.url == p.tv.links.url && sameSpans(hit.spans, p.tv.links.spans) {
		return
	}
	p.tv.setHover(hit)
	cursor := "text"
	if hit.url != "" {
		cursor = "pointer"
	}
	if cursor != p.cursorName {
		p.widget.SetCursorFromName(cursor)
		p.cursorName = cursor
	}
	p.queueRender()
}

func sameSpans(a, b []linkSpan) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ctrlKey follows Ctrl presses and releases while the pointer rests.
func (p *Pane) ctrlKey(down bool) {
	var state gdk.ModifierType
	if down {
		state = gdk.ControlMask
	}
	p.updateHover(p.lastX, p.lastY, state)
}

// clickLink opens the link at (x, y) for a Ctrl+click (Ctrl+Shift+click
// while the application reports the mouse) and reports whether it did.
func (p *Pane) clickLink(button uint, x, y float64, state gdk.ModifierType, mouseApp bool) bool {
	if button != 1 || state&gdk.ControlMask == 0 || (mouseApp && state&gdk.ShiftMask == 0) {
		return false
	}
	col, row := p.cellAt(x, y)
	hit, ok := p.linkAt(col, row)
	if !ok {
		return false
	}
	openURL(p.app, hit.url)
	return true
}
