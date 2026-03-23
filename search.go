// search.go - incremental in-pane text search (Ctrl+F).
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
// both at once) to avoid deadlocks.
//
// Async scan: updateSearch snapshots the virtual rows to [][]rune under p.mu
// (fast memcpy), releases the lock, then scans in a background goroutine.
// A searchGen counter guards against stale goroutines: if the user types
// another character before the goroutine finishes, the stale result is
// discarded.
package main

import (
	"strings"
	"unicode"

	"bunk/internal/vt10x"
	"github.com/gdamore/tcell/v2"
)

// searchMatch identifies one occurrence of the search query inside a pane's
// virtual grid (scrollback ring + live terminal rows).
type searchMatch struct {
	vRow, col, length int
}

// searchSpan is a highlighted column range within one virtual row [col, end).
type searchSpan struct{ col, end int }

// searchHighlight holds span-based match positions for renderPane.
// regular contains all matches; current contains only the selected match.
// Both map virtual row → spans for that row.
type searchHighlight struct {
	regular map[int][]searchSpan
	current map[int][]searchSpan
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
	app.searchGen++
	app.mu.Unlock()

	p.mu.Lock()
	p.searchHL = nil
	p.searchHLGen++
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
	app.searchGen++ // invalidate any in-flight scan goroutine
	app.mu.Unlock()

	if p != nil {
		p.mu.Lock()
		p.searchHL = nil
		p.searchHLGen++
		p.mu.Unlock()
	}
	app.triggerRedraw()
}

// updateSearch snapshots the active search pane's virtual grid, then scans
// for the current query in a background goroutine.  Results are published only
// if the searchGen counter still matches when the goroutine finishes — stale
// results from superseded queries are silently discarded.
func (app *App) updateSearch() {
	// Snapshot search state under app.mu.
	app.mu.Lock()
	p := app.searchPane
	query := app.searchQuery
	idx := app.searchIdx
	app.searchGen++
	gen := app.searchGen
	app.mu.Unlock()

	if p == nil {
		return
	}
	if query == "" {
		p.mu.Lock()
		p.searchHL = nil
		p.searchHLGen++
		p.mu.Unlock()
		app.mu.Lock()
		app.searchMatches = nil
		app.searchIdx = 0
		app.mu.Unlock()
		app.triggerRedraw()
		return
	}

	lq := strings.ToLower(query)
	lqRunes := []rune(lq)
	lqLen := len(lqRunes)

	// Snapshot the virtual grid to [][]rune under p.mu.
	// This is a pure memcpy — no pattern matching yet — so lock hold time is
	// O(sbCount × cols) iterations of simple ToLower calls (typically < 1ms).
	p.mu.Lock()
	cols, rows := p.term.Size()
	sbCount := p.sb.count
	totalRows := sbCount + rows
	snapshot := make([][]rune, totalRows)
	for vRow := 0; vRow < totalRows; vRow++ {
		var cells []vt10x.Glyph
		if vRow < sbCount {
			cells = p.sb.get(vRow)
		} else {
			cells = captureRow(p.term, vRow-sbCount, cols)
		}
		row := make([]rune, cols)
		for i := 0; i < cols; i++ {
			var ch rune
			if i < len(cells) {
				ch = cells[i].Char
			}
			if ch == 0 {
				ch = ' '
			}
			row[i] = unicode.ToLower(ch)
		}
		snapshot[vRow] = row
	}
	p.mu.Unlock()

	go func() {
		// Scan snapshot — no lock held.
		var matches []searchMatch
		for vRow, row := range snapshot {
			for offset := 0; offset+lqLen <= len(row); offset++ {
				match := true
				for k := 0; k < lqLen; k++ {
					if row[offset+k] != lqRunes[k] {
						match = false
						break
					}
				}
				if match {
					matches = append(matches, searchMatch{
						vRow:   vRow,
						col:    offset,
						length: lqLen,
					})
					offset += lqLen - 1 // skip to end of match (loop adds 1)
				}
			}
		}

		// Clamp index.
		clampedIdx := idx
		if clampedIdx >= len(matches) {
			clampedIdx = 0
		}

		// Build span-based highlight.
		var hl *searchHighlight
		if len(matches) > 0 {
			hl = &searchHighlight{
				regular: make(map[int][]searchSpan),
				current: make(map[int][]searchSpan),
			}
			for i, m := range matches {
				span := searchSpan{col: m.col, end: m.col + m.length}
				if i == clampedIdx {
					hl.current[m.vRow] = append(hl.current[m.vRow], span)
				} else {
					hl.regular[m.vRow] = append(hl.regular[m.vRow], span)
				}
			}
		}

		// Publish — check generation before committing either result.
		app.mu.Lock()
		if app.searchGen != gen {
			app.mu.Unlock()
			return // superseded by a newer query
		}
		app.searchMatches = matches
		app.searchIdx = clampedIdx
		app.mu.Unlock()

		p.mu.Lock()
		p.searchHL = hl
		p.searchHLGen++
		p.mu.Unlock()

		L.Debug("search: scan done", "query", query, "matches", len(matches), "idx", clampedIdx, "gen", gen)
		app.triggerRedraw()
	}()
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

// spanContains reports whether any span in spans covers col.
func spanContains(spans []searchSpan, col int) bool {
	for _, sp := range spans {
		if col >= sp.col && col < sp.end {
			return true
		}
	}
	return false
}
