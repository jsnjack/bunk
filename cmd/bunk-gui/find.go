package main

// Find in pane (actions search, search_next, search_prev, search_exit, as
// in bunk's TUI): a small bar on the focused pane, an incremental
// case-insensitive search through session.FindAll over scrollback and
// screen, every match highlighted and the current one stronger.

import (
	"fmt"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"bunk/internal/session"
)

// Match colours (RGBA8 as Instance takes them): amber for every match,
// orange for the current one, as the TUI does.
var (
	findFG, findBG       = rgba(0x131416), rgba(0xc9a227)
	findCurFG, findCurBG = rgba(0x131416), rgba(0xff8c2a)
)

type finder struct {
	p       *Pane
	bar     *gtk.Box
	height  int // logical pixels from the pane's top to the bar's bottom
	entry   *gtk.Entry
	count   *gtk.Label
	query   string
	matches []session.Match
	idx     int // current match; -1 when none
	gen     int // bumped by every query change
	busy    bool
	again   bool // the query changed while a scan ran
}

// openFind shows the find bar on the focused pane, or focuses it again.
func (a *App) openFind() {
	p := a.activePane()
	if p == nil {
		return
	}
	if f := a.find; f != nil {
		if f.p == p {
			f.entry.GrabFocus()
			return
		}
		a.closeFind()
	}
	f := &finder{p: p, idx: -1}
	f.bar = gtk.NewBox(gtk.OrientationHorizontal, 6)
	f.bar.AddCSSClass("findbar")
	f.bar.SetHAlign(gtk.AlignEnd)
	f.bar.SetVAlign(gtk.AlignStart)
	f.entry = gtk.NewEntry()
	f.entry.AddCSSClass("findentry")
	f.entry.SetPlaceholderText("Find")
	f.entry.SetWidthChars(22)
	f.count = label("", "findcount")
	cl := iconButton("window-close-symbolic", 14, a.withKeys("Close", "search_exit"), "tabclose")
	cl.SetVAlign(gtk.AlignCenter)
	cl.ConnectClicked(a.closeFind)
	f.bar.Append(f.entry)
	f.bar.Append(f.count)
	f.bar.Append(cl)
	f.entry.ConnectChanged(func() {
		if a.find == f {
			a.setFindQuery(f.entry.Text())
		}
	})
	p.frame.AddOverlay(f.bar)
	a.find = f
	f.entry.GrabFocus()
	// The badges move below the bar while it is open.
	_, f.height, _, _ = f.bar.Measure(gtk.OrientationVertical, -1)
	p.syncBadges()
}

// closeFind removes the bar and the highlights. Like the TUI it leaves the
// scrollback view where the last match put it.
func (a *App) closeFind() {
	f := a.find
	if f == nil {
		return
	}
	a.find = nil
	f.p.frame.RemoveOverlay(f.bar)
	f.p.syncBadges()
	f.p.tv.setFind(nil, -1)
	f.p.queueRender()
	if a.activePane() == f.p && !f.p.removeOnce {
		f.p.widget.GrabFocus()
	}
}

func (a *App) setFindQuery(q string) {
	f := a.find
	f.query = q
	f.gen++
	if q == "" {
		f.matches, f.idx = nil, -1
		a.showFind()
		return
	}
	a.findScan()
}

// findScan runs FindAll on a goroutine, off the GTK thread; FindAll holds
// the session lock while it reads the grid. Queries typed meanwhile
// coalesce into one more scan.
func (a *App) findScan() {
	f := a.find
	if f.busy {
		f.again = true
		return
	}
	f.busy = true
	q, gen, s := f.query, f.gen, f.p.s
	go func() {
		m := s.FindAll(q)
		coreglib.IdleAdd(func() {
			f.busy = false
			if a.find != f {
				return
			}
			if f.again || gen != f.gen {
				f.again = false
				a.findScan()
				return
			}
			f.matches = m
			f.idx = f.nearest()
			if f.idx >= 0 && !f.visible(f.matches[f.idx]) {
				f.scrollTo(f.matches[f.idx])
			}
			a.showFind()
		})
	}()
}

// nearest picks the last match at or above the bottom of the view (the
// one closest to what the user is looking at), else the first.
func (f *finder) nearest() int {
	if len(f.matches) == 0 {
		return -1
	}
	s := f.p.s
	s.Lock()
	_, rows := s.Term.Size()
	bottom := s.Scrollback.Count - s.ScrollOffset + rows - 1
	s.Unlock()
	cur := 0
	for i, m := range f.matches {
		if m.VRow <= bottom {
			cur = i
		}
	}
	return cur
}

func (f *finder) visible(m session.Match) bool {
	s := f.p.s
	s.Lock()
	defer s.Unlock()
	_, rows := s.Term.Size()
	top := s.Scrollback.Count - s.ScrollOffset
	return m.VRow >= top && m.VRow < top+rows
}

// scrollTo centres a match vertically, as the TUI does.
func (f *finder) scrollTo(m session.Match) {
	s := f.p.s
	s.Lock()
	_, rows := s.Term.Size()
	count := s.Scrollback.Count
	s.ScrollOffset = max(0, min(count, count-m.VRow+rows/2))
	s.Unlock()
}

// findStep moves to the next (+1) or previous (-1) match, wrapping around.
func (a *App) findStep(d int) {
	f := a.find
	if n := len(f.matches); n > 0 {
		f.idx = ((f.idx+d)%n + n) % n
		f.scrollTo(f.matches[f.idx])
		a.showFind()
	}
}

// showFind updates the count and the pane's highlights.
func (a *App) showFind() {
	f := a.find
	if f.query == "" {
		f.count.SetText("")
	} else {
		f.count.SetText(fmt.Sprintf("%d/%d", f.idx+1, len(f.matches)))
	}
	f.p.tv.setFind(f.matches, f.idx)
	f.p.queueRender()
}

// findFocused reports the keyboard in the find bar.
func (a *App) findFocused() bool {
	if a.find == nil {
		return false
	}
	fw := a.win.Focus()
	return fw != nil && gtk.BaseWidget(fw).IsAncestor(a.find.entry)
}

// findAction runs search_next, search_prev and search_exit while the bar
// is open; otherwise their keys (Enter, Esc) go to the terminal.
func (a *App) findAction(name string) bool {
	if a.find == nil {
		return false
	}
	switch name {
	case "search_next":
		a.findStep(+1)
	case "search_prev":
		a.findStep(-1)
	case "search_exit":
		a.closeFind()
	}
	return true
}
