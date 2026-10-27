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
	// fits caches, for each slot, the result of lineFit: Row.ReflowFit
	// of an unwrapped row, fitNone or fitUnknown. Reflow moves rows that
	// fit the new width without reading their cells again.
	fits    []int32
	head    int // index of the oldest entry
	Count   int // number of valid entries (0 … maxLines)
	viewBuf []vt10x.Glyph
}

// Values of Ring.fits besides a fit: not computed yet, or no fit.
const (
	fitUnknown = -2
	fitNone    = -1
)

// nextSlot returns the slot for a new line, evicting the oldest line when
// the ring is full.
func (s *Ring) nextSlot() int {
	if s.lines == nil {
		s.lines = make([]vt10x.Row, s.MaxLines)
		s.fits = make([]int32, s.MaxLines)
	}
	// head < MaxLines and Count <= MaxLines, so subtracting once replaces
	// the slower modulo.
	var slot int
	if s.Count < s.MaxLines {
		s.Count++
		slot = s.head + s.Count - 1
	} else {
		// Ring is full: overwrite oldest slot (head), advance head.
		slot = s.head
		s.head++
		if s.head == s.MaxLines {
			s.head = 0
		}
	}
	if slot >= s.MaxLines {
		slot -= s.MaxLines
	}
	s.fits[slot] = fitUnknown
	return slot
}

// lineFit returns n when the row at logical index i is unwrapped and
// reflow, reading it as a whole line, would copy exactly its first n cells
// into a row of cols >= n columns. Otherwise it returns -1. A known answer
// is cached, so later calls do not read the row's cells.
func (s *Ring) lineFit(i, cols int) int {
	slot := (s.head + i) % s.MaxLines
	fit := s.fits[slot]
	if fit == fitUnknown {
		row := s.lines[slot]
		switch n := row.Len(); {
		case n > 0 && rowWrapped(row, n-1):
			fit = fitNone
		case row.ReflowEnd() > cols:
			return -1 // a wider row may hold it; check again then
		default:
			fit = int32(row.ReflowFit())
		}
		s.fits[slot] = fit
	}
	if int(fit) > cols {
		return -1
	}
	return int(fit)
}

// rowWrapped reports whether cell x of row carries the soft-wrap bit.
func rowWrapped(row vt10x.Row, x int) bool {
	_, _, _, mode, _, _ := row.CellAt(x)
	return mode&vt10x.AttrWrap != 0
}

// adopt appends row without copying it: the ring takes over its storage.
// fit is the row's lineFit, or fitUnknown.
func (s *Ring) adopt(row vt10x.Row, fit int32) {
	if s.MaxLines <= 0 {
		return
	}
	slot := s.nextSlot()
	s.lines[slot] = row
	s.fits[slot] = fit
}

// slot returns the stored row at logical index i for reflow to take over.
func (s *Ring) slot(i int) *vt10x.Row {
	return &s.lines[(s.head+i)%s.MaxLines]
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
	row := s.Row(i)
	if row.Len() == 0 {
		return nil
	}
	return row.AppendGlyphs(make([]vt10x.Glyph, 0, row.Len()))
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
