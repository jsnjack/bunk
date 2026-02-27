// search.go – incremental in-pane text search (Ctrl+F).
//
// Usage:
//
//	Ctrl+F          → enter search mode on the active pane
//	Type characters → query grows; matches highlight live (amber = all, orange = current)
//	Enter / Ctrl+N  → jump to next match
//	Ctrl+P          → jump to previous match
//	Backspace       → delete last character from query
//	Escape          → exit search mode, clear all highlights
//
// The search is case-insensitive and covers both the scrollback ring and the
// live terminal grid (the full virtual row space).
//
// Thread safety: App.search* fields are protected by app.mu.  Pane.searchHL is
// protected by pane.mu.  updateSearch acquires each lock independently (never
// both at once) to avoid deadlocks, accepting a one-frame visual inconsistency
// if the render loop runs between the two commits.
package main

import (
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/hinshun/vt10x"
)

// searchMatch identifies one occurrence of the search query inside a pane's
// virtual grid (scrollback ring + live terminal rows).
type searchMatch struct {
	vRow, col, length int
}

// enterSearch activates search mode for the currently active pane.
func (app *App) enterSearch() {
	app.mu.Lock()
	p := app.active
	if p == nil {
		app.mu.Unlock()
		return
	}
	L.Debug("search: entering search mode", "pane", p.id)
	app.searchMode = true
	app.searchQuery = ""
	app.searchPane = p
	app.searchMatches = nil
	app.searchIdx = 0
	app.mu.Unlock()

	p.mu.Lock()
	p.searchHL = nil
	p.mu.Unlock()

	app.triggerRedraw()
}

// exitSearch deactivates search mode and removes all highlights.
func (app *App) exitSearch() {
	app.mu.Lock()
	p := app.searchPane
	L.Debug("search: exiting search mode")
	app.searchMode = false
	app.searchQuery = ""
	app.searchPane = nil
	app.searchMatches = nil
	app.searchIdx = 0
	app.mu.Unlock()

	if p != nil {
		p.mu.Lock()
		p.searchHL = nil
		p.mu.Unlock()
	}
	app.triggerRedraw()
}

// updateSearch scans the active search pane's virtual grid for the current
// query and rebuilds p.searchHL (match highlights) and app.searchMatches.
// Called from the event loop after any query or index change.
func (app *App) updateSearch() {
	// Snapshot search state under app.mu.
	app.mu.Lock()
	p := app.searchPane
	query := app.searchQuery
	idx := app.searchIdx
	app.mu.Unlock()

	if p == nil {
		return
	}
	if query == "" {
		p.mu.Lock()
		p.searchHL = nil
		p.mu.Unlock()
		app.mu.Lock()
		app.searchMatches = nil
		app.searchIdx = 0
		app.mu.Unlock()
		app.triggerRedraw()
		return
	}

	lq := strings.ToLower(query)

	// Scan the virtual grid under p.mu.  We hold p.mu for the entire scan to
	// get a consistent snapshot; it is the same lock held by renderPane, so
	// the scan blocks at most one render frame.
	p.mu.Lock()
	cols, rows := p.term.Size()
	sbCount := p.sb.count
	var matches []searchMatch

	for vRow := 0; vRow < sbCount+rows; vRow++ {
		var cells []vt10x.Glyph
		if vRow < sbCount {
			cells = p.sb.get(vRow)
		} else if tr := vRow - sbCount; tr >= 0 && tr < rows {
			cells = captureRow(p.term, tr, cols)
		}
		if cells == nil {
			continue
		}
		// Build lowercase line string.
		var lb strings.Builder
		for _, g := range cells {
			ch := g.Char
			if ch == 0 {
				ch = ' '
			}
			lb.WriteRune(ch)
		}
		line := strings.ToLower(lb.String())

		// Find all non-overlapping occurrences.
		offset := 0
		for {
			i := strings.Index(line[offset:], lq)
			if i < 0 {
				break
			}
			matches = append(matches, searchMatch{
				vRow:   vRow,
				col:    offset + i,
				length: len(lq),
			})
			offset += i + 1
		}
	}

	// Clamp index.
	if idx >= len(matches) {
		idx = 0
	}

	// Build highlight map: 1 = regular match, 2 = current match.
	var hl map[int64]int8
	if len(matches) > 0 {
		hl = make(map[int64]int8, len(matches)*len(lq))
		for i, m := range matches {
			val := int8(1)
			if i == idx {
				val = 2
			}
			for c := m.col; c < m.col+m.length && c < cols; c++ {
				key := int64(m.vRow)<<16 | int64(c)
				// Don't downgrade a current-match cell to regular.
				if hl[key] != 2 {
					hl[key] = val
				}
			}
		}
	}
	p.searchHL = hl
	p.mu.Unlock()

	// Commit match list and (possibly clamped) index under app.mu.
	app.mu.Lock()
	app.searchMatches = matches
	app.searchIdx = idx
	app.mu.Unlock()

	L.Debug("search: updateSearch done", "query", query, "matches", len(matches), "idx", idx)
	app.triggerRedraw()
}

// searchNavigate moves to the next (delta=+1) or previous (delta=-1) match,
// scrolls the pane so the match is visible, and rebuilds highlights.
func (app *App) searchNavigate(delta int) {
	app.mu.Lock()
	matches := app.searchMatches
	if len(matches) == 0 {
		app.mu.Unlock()
		return
	}
	app.searchIdx = (app.searchIdx + delta + len(matches)) % len(matches)
	m := matches[app.searchIdx]
	p := app.searchPane
	app.mu.Unlock()

	L.Debug("search: navigate", "delta", delta, "idx", app.searchIdx, "total", len(matches), "vrow", m.vRow)

	if p == nil {
		return
	}

	// Scroll so the match is centred vertically in the pane.
	p.mu.Lock()
	sbCount := p.sb.count
	rows := p.h
	targetOff := sbCount - m.vRow + rows/2
	if targetOff < 0 {
		targetOff = 0
	}
	if targetOff > sbCount {
		targetOff = sbCount
	}
	p.sbOff = targetOff
	p.mu.Unlock()

	// Rebuild highlights with the updated index.
	app.updateSearch()
}

// handleSearchKey processes a key event while search mode is active.
// It returns true to continue the event loop (search mode never triggers shutdown).
func (app *App) handleSearchKey(ev *tcell.EventKey) bool {
	kb := &app.keys
	switch {
	case kb.SearchExit.Matches(ev):
		app.exitSearch()

	case ev.Key() == tcell.KeyEnter, kb.SearchNext.Matches(ev):
		app.searchNavigate(+1)

	case kb.SearchPrev.Matches(ev):
		app.searchNavigate(-1)

	case ev.Key() == tcell.KeyBackspace || ev.Key() == tcell.KeyBackspace2:
		app.mu.Lock()
		q := app.searchQuery
		app.mu.Unlock()
		if len(q) > 0 {
			runes := []rune(q)
			app.mu.Lock()
			app.searchQuery = string(runes[:len(runes)-1])
			app.mu.Unlock()
			app.updateSearch()
		}

	case ev.Key() == tcell.KeyRune:
		app.mu.Lock()
		app.searchQuery += string(ev.Rune())
		app.mu.Unlock()
		app.updateSearch()
	}
	return true
}
