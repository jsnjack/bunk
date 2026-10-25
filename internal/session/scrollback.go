// scrollback.go - per-pane scrollback buffer.
//
// Architecture
// ─────────────
// Scrollback capture uses a native vt10x scroll callback (WithScrollCallback).
// vt10x calls Session.OnScrollRow() synchronously inside scrollUp() for each row
// that leaves the top of the primary screen, before that row's storage is
// cleared.  onScrollRow pushes the row into sbRing.
//
// Alternate screen (vim, htop, less in fullscreen) sets vt10x.ModeAltScreen.
// onScrollRow checks this flag and ignores callbacks while it is set — alt-
// screen apps use absolute positioning and their scrolls must not appear in
// primary scrollback.
//
// Scrollback ring buffer
// ──────────────────────
// sbRing is a fixed-capacity circular buffer of captured lines, stored as
// compact vt10x.Rows without trailing blanks.  Each entry owns its backing
// storage (allocated or reused by push).  When the ring is full the oldest
// line is evicted and its slot is reused.
//
// User navigation (Shift+PgUp / Shift+PgDn):
//
//	sbOff == 0     → live view (normal)
//	sbOff == N     → display starting N lines above the live view
//	Any non-scroll key → snap back to live view automatically
package session

import "bunk/internal/vt10x"

// Ring is a fixed-capacity circular buffer of captured rows.
// maxLines must be set before the first push (typically from the config).
type Ring struct {
	MaxLines int         // ring capacity (from config scrollback setting)
	lines    []vt10x.Row // allocated on first push, length = maxLines
	head     int         // index of the oldest entry
	Count    int         // number of valid entries (0 … maxLines)
	viewBuf  []vt10x.Glyph
}

// nextSlot returns the slot for a new line, evicting the oldest line when
// the ring is full.
func (s *Ring) nextSlot() int {
	if s.lines == nil {
		s.lines = make([]vt10x.Row, s.MaxLines)
	}
	if s.Count < s.MaxLines {
		s.Count++
		return (s.head + s.Count - 1) % s.MaxLines
	}
	// Ring is full: overwrite oldest slot (head), advance head.
	slot := s.head
	s.head = (s.head + 1) % s.MaxLines
	return slot
}

// PushRow appends a copy of row without its trailing blanks.  Readers treat
// cells past the end of a stored row as default blanks, so dropping them
// changes nothing on screen, in copies or in search.
func (s *Ring) PushRow(row vt10x.Row) {
	if s.MaxLines <= 0 {
		return
	}
	slot := s.nextSlot()
	s.lines[slot].CopyFrom(row, row.TrimmedLen())
}

// Push appends a row of glyphs without its trailing blanks.  The ring never
// retains a reference to row.
func (s *Ring) Push(row []vt10x.Glyph) {
	if s.MaxLines <= 0 {
		return
	}
	slot := s.nextSlot()
	r := &s.lines[slot]
	r.SetGlyphs(row)
	r.Truncate(r.TrimmedLen())
}

// Row returns the stored Row at logical index i without decoding it.  The
// result must not be modified.
func (s *Ring) Row(i int) vt10x.Row {
	return s.lines[(s.head+i)%s.MaxLines]
}

// Clear empties the ring in place.  Backing row storage is kept so
// subsequent pushes can reuse it without reallocating.
func (s *Ring) Clear() {
	s.head = 0
	s.Count = 0
}

// Get returns a new slice holding the line at logical index i, where 0 is
// the OLDEST surviving line and count-1 is the most recently pushed line.
// Returns nil on bounds violation.
func (s *Ring) Get(i int) []vt10x.Glyph {
	if i < 0 || i >= s.Count {
		return nil
	}
	return s.Row(i).AppendGlyphs(nil)
}

// View is get without allocating: the result is reused by the next call to
// View.  Render, search and reflow read one row at a time this way.
func (s *Ring) View(i int) []vt10x.Glyph {
	if i < 0 || i >= s.Count {
		return nil
	}
	s.viewBuf = s.Row(i).AppendGlyphs(s.viewBuf[:0])
	return s.viewBuf
}

// ---------------------------------------------------------------------------
// Scrollback capture helpers (called from ReadLoop, under the session lock)
// ---------------------------------------------------------------------------

// CaptureRow allocates a fresh []vt10x.Glyph slice and copies the current
// vt10x row r into it.  Must be called with the session locked.
func CaptureRow(term vt10x.Terminal, r, cols int) []vt10x.Glyph {
	row := make([]vt10x.Glyph, cols)
	for c := 0; c < cols; c++ {
		row[c] = term.RawCell(c, r)
	}
	return row
}
