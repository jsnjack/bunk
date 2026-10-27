package session

import (
	"bunk/internal/vt10x"
	"strings"
	"unicode"
)

// oracleFindAll is FindAll as it was before it read compact cells (commit
// a6b6ac3), copied without changes except for its name and this comment.
// TestFindAllMatchesOracle and FuzzFindAndSelect compare FindAll against it.
// Do not optimise or fix it: a fix belongs in both.
func (s *Session) oracleFindAll(query string) []Match {
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
