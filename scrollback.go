// scrollback.go – per-pane scrollback buffer.
//
// Architecture
// ─────────────
// vt10x maintains a fixed-size cell grid (w × h).  When the terminal scrolls,
// row 0 is silently discarded and every other row shifts up by one.  vt10x
// exposes no hook for this event, so we must detect it ourselves.
//
// Detection algorithm (called from readPTY, under Pane.mu):
//  1. Before term.Write(chunk): if the cursor is in the BOTTOM HALF of the
//     screen, take a full snapshot of every row.
//  2. After  term.Write(chunk): compare the new row-0 to every row in the
//     snapshot.  If old row[N] matches new row[0], exactly N rows scrolled
//     off the top.  Push those N rows into the ring buffer.
//
// The comparison uses the first non-blank row as a "fingerprint" for the
// scroll shift.  This is approximate (false positives possible if rows happen
// to have identical content) but correct for virtually all real-world shell
// output (ls, git log, compiler output, etc.).
//
// Alternate screen (vim, htop, less in fullscreen) sets vt10x.ModeAltScreen.
// We skip both snapshot and push while that flag is set – the alternate screen
// doesn't scroll in the traditional sense, and its state is separate from the
// primary screen history.
//
// Scrollback ring buffer
// ──────────────────────
// sbRing is a fixed-capacity circular buffer of captured lines.  Each entry
// is a slice of vt10x.Glyph (one per column).  When the ring is full the
// oldest line is silently evicted.
//
// User navigation (Shift+PgUp / Shift+PgDn):
//
//	sbOff == 0     → live view (normal)
//	sbOff == N     → display starting N lines above the live view
//	Any non-scroll key → snap back to live view automatically
package main

import "github.com/hinshun/vt10x"

// sbRing is a fixed-capacity circular buffer of captured Glyph rows.
// maxLines must be set before the first push (typically from the config).
type sbRing struct {
	maxLines int               // ring capacity (from config scrollback setting)
	lines    [][]vt10x.Glyph   // allocated on first push, length = maxLines
	head     int               // index of the oldest entry
	count    int               // number of valid entries (0 … maxLines)
}

// push appends one captured row to the ring.  When the ring is full, the
// oldest entry is evicted and its backing memory is reused to avoid GC churn.
func (s *sbRing) push(row []vt10x.Glyph) {
	if s.maxLines <= 0 {
		return
	}
	if s.lines == nil {
		s.lines = make([][]vt10x.Glyph, s.maxLines)
	}
	if s.count < s.maxLines {
		s.lines[(s.head+s.count)%s.maxLines] = row
		s.count++
	} else {
		// Ring is full: overwrite oldest slot (head), advance head.
		s.lines[s.head] = row
		s.head = (s.head + 1) % s.maxLines
	}
}

// get returns the line at logical index i, where 0 is the OLDEST surviving
// line and count-1 is the most recently pushed line.  Returns nil on bounds
// violation.
func (s *sbRing) get(i int) []vt10x.Glyph {
	if i < 0 || i >= s.count {
		return nil
	}
	return s.lines[(s.head+i)%s.maxLines]
}

// ---------------------------------------------------------------------------
// Scrollback capture helpers (called from pane.go readPTY, under Pane.mu)
// ---------------------------------------------------------------------------

// captureRow allocates a fresh []vt10x.Glyph slice and copies the current
// vt10x row r into it.  Must be called with Pane.mu held.
func captureRow(term vt10x.Terminal, r, cols int) []vt10x.Glyph {
	row := make([]vt10x.Glyph, cols)
	for c := 0; c < cols; c++ {
		row[c] = term.Cell(c, r)
	}
	return row
}

// captureGrid snapshots all rows of the vt10x grid.  Must be called with
// Pane.mu held.
func captureGrid(term vt10x.Terminal, cols, rows int) [][]vt10x.Glyph {
	grid := make([][]vt10x.Glyph, rows)
	for r := 0; r < rows; r++ {
		grid[r] = captureRow(term, r, cols)
	}
	return grid
}

// isBlankRow returns true if every cell in row is empty (NUL or space).
// Used to avoid using blank rows as scroll fingerprints.
func isBlankRow(row []vt10x.Glyph) bool {
	for _, g := range row {
		if g.Char != 0 && g.Char != ' ' {
			return false
		}
	}
	return true
}

// rowsEqual compares two rows cell-by-cell.
func rowsEqual(a, b []vt10x.Glyph) bool {
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

// detectShift finds the scroll amount by looking for the position in prev
// whose content matches newRow0 (the current row 0 after the write).
// Returns 0 if no shift is detected (content unchanged or all blank).
//
// newRow1 is the row-1 content after the write, used to confirm the candidate
// shift: a real scroll of N lines means prev[N]==newRow0 AND prev[N+1]==newRow1.
//
// A return value of len(prev) means the output burst scrolled more than one
// full terminal height: newRow0 is entirely new content not present in prev.
// The caller should treat this as "all of prev has scrolled off".
func detectShift(prev [][]vt10x.Glyph, newRow0, newRow1 []vt10x.Glyph) int {
	if isBlankRow(newRow0) {
		// Blank row 0 can't serve as a fingerprint.
		return 0
	}
	// KEY INVARIANT: if row 0 did not change, nothing scrolled.
	// Without this check the loop below can falsely match prev[shift]==newRow0
	// whenever two rows in the grid happen to share identical content (very
	// common with blank rows, repeated filenames, or prompt lines that look the
	// same).  This was causing every typed character to push a spurious line
	// into the scrollback buffer whenever row 0 content happened to equal some
	// other row in the snapshot.
	if rowsEqual(prev[0], newRow0) {
		return 0
	}
	// Row 0 changed – find how many rows scrolled off by fingerprinting.
	for shift := 1; shift < len(prev); shift++ {
		if !rowsEqual(prev[shift], newRow0) {
			continue
		}
		// Verify with row 1 to reject coincidental single-row matches.
		if newRow1 != nil && !isBlankRow(newRow1) && shift+1 < len(prev) {
			if !rowsEqual(prev[shift+1], newRow1) {
				continue
			}
		}
		return shift
	}
	// Row 0 changed and doesn't match any previous row: the output burst
	// scrolled more than one full terminal height.  Push all of prev.
	// (We already know prev[0] != newRow0 from the early-return above.)
	//
	// The original guard was isBlankRow(prev[0]) but that misses the common
	// case where a fresh terminal (SSH session just started, or a new pane)
	// has blank rows at the top: a large burst of output can scroll off all
	// the previous non-blank rows without prev[0] ever being non-blank.
	// Use any non-blank row in prev as the guard instead.
	for _, row := range prev {
		if !isBlankRow(row) {
			return len(prev) // sentinel: entire prev has scrolled off
		}
	}
	return 0
}
