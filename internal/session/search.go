// search.go - case-insensitive text search over scrollback and the live grid.
package session

import (
	"bunk/internal/vt10x"
	"strings"
	"unicode"
)

// Match identifies one occurrence of the search query inside a pane's
// virtual grid (scrollback ring + live terminal rows).
type Match struct {
	VRow, Col, Length int
}

// Span is a highlighted column range within one virtual row [col, end).
type Span struct{ Col, End int }

// Highlight holds span-based match positions for front ends.
// regular contains all matches; current contains only the selected match.
// Both map virtual row → spans for that row.
type Highlight struct {
	Regular map[int][]Span
	Current map[int][]Span
}

// BuildHighlight maps match positions to per-row spans for front ends.
// idx is the currently selected match; it is highlighted via current spans.
func BuildHighlight(matches []Match, idx int) *Highlight {
	if len(matches) == 0 {
		return nil
	}
	hl := &Highlight{
		Regular: make(map[int][]Span),
		Current: make(map[int][]Span),
	}
	for i, m := range matches {
		span := Span{Col: m.Col, End: m.Col + m.Length}
		if i == idx {
			hl.Current[m.VRow] = append(hl.Current[m.VRow], span)
		} else {
			hl.Regular[m.VRow] = append(hl.Regular[m.VRow], span)
		}
	}
	return hl
}

// SpanContains reports whether any span in spans covers col.
func SpanContains(spans []Span, col int) bool {
	for _, sp := range spans {
		if col >= sp.Col && col < sp.End {
			return true
		}
	}
	return false
}

// FindAll returns every case-insensitive occurrence of query in the
// scrollback and live grid, in virtual-row order. The session is locked
// while the grid is copied, not while it is matched. An empty query matches
// nothing.
func (s *Session) FindAll(query string) []Match {
	if query == "" {
		return nil
	}
	lq := strings.ToLower(query)
	lqRunes := []rune(lq)
	lqLen := len(lqRunes)

	// Snapshot the virtual grid to [][]rune under the session lock.
	// This is a pure memcpy — no pattern matching yet — so lock hold time is
	// O(sbCount × cols) iterations of simple ToLower calls.
	s.Lock()
	cols, rows := s.Term.Size()
	sbCount := s.Scrollback.Count
	totalRows := sbCount + rows
	snapshot := make([][]rune, totalRows)
	columns := make([][]int, totalRows)
	ends := make([][]int, totalRows)
	for vRow := 0; vRow < totalRows; vRow++ {
		var cells []vt10x.Glyph
		if vRow < sbCount {
			cells = s.Scrollback.View(vRow)
		} else {
			cells = CaptureRow(s.Term, vRow-sbCount, cols)
		}
		row := make([]rune, 0, cols)
		positions := make([]int, 0, cols+1)
		endColumns := make([]int, 0, cols)
		for i := 0; i < cols; i++ {
			var ch rune
			if i < len(cells) {
				if cells[i].Width < 0 {
					continue
				}
				ch = cells[i].Char
			}
			if ch == 0 {
				ch = ' '
			}
			row = append(row, unicode.ToLower(ch))
			positions = append(positions, i)
			width := 1
			if i < len(cells) {
				width = max(1, int(cells[i].Width))
			}
			endColumns = append(endColumns, i+width)
			if i < len(cells) {
				for _, c := range cells[i].Combining {
					row = append(row, unicode.ToLower(c))
					positions = append(positions, i)
					endColumns = append(endColumns, i+width)
				}
			}
		}
		snapshot[vRow] = row
		columns[vRow] = append(positions, cols)
		ends[vRow] = endColumns
	}
	s.Unlock()

	var matches []Match
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
				matches = append(matches, Match{
					VRow:   vRow,
					Col:    columns[vRow][offset],
					Length: ends[vRow][offset+lqLen-1] - columns[vRow][offset],
				})
				offset += lqLen - 1 // skip to end of match (loop adds 1)
			}
		}
	}
	return matches
}
