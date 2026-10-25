package session

import "bunk/internal/vt10x"

// reflowCells copies glyphs directly: replaying old cursor commands at a new
// width changes their targets and can recreate content that was already erased.
func (s *Session) reflowCells(cols, rows int) {
	oldCols, oldRows := s.Term.Size()
	cursor := s.Term.Cursor()
	cursorRow := s.Scrollback.Count + cursor.Y
	cursorCol := cursor.X
	if cursor.State&vt10x.CursorWrapNext != 0 {
		cursorCol++
	}
	anchorRow := -1
	if s.ScrollOffset > 0 {
		anchorRow = s.Scrollback.Count - s.ScrollOffset + oldRows/2
	}
	mappedAnchor, mappedCursorRow, mappedCursorCol := 0, 0, 0
	pending, cursorMapped := false, false
	result := Ring{MaxLines: s.ScrollbackLines + rows}
	// Zero glyphs carry palette colour 0, not the default colours.
	blank := vt10x.Glyph{FG: vt10x.DefaultFG, BG: vt10x.DefaultBG, UL: vt10x.DefaultUL}
	row := make([]vt10x.Glyph, cols)
	fillRow(row, blank)
	x, y := 0, 0
	flush := func(wrapped bool) {
		if wrapped {
			row[cols-1].Mode |= vt10x.AttrWrap
		}
		if !cursorMapped || y < mappedCursorRow+rows {
			result.Push(row)
		}
		fillRow(row, blank)
		x = 0
		y++
	}
	mapCursor := func() {
		cursorMapped = true
		mappedCursorRow, mappedCursorCol = y, x
		pending = x == cols
		if pending {
			mappedCursorCol--
		}
	}
	contentRows := findContentRows(s.Term, oldCols, oldRows)
	total := s.Scrollback.Count + contentRows
	for r := 0; r < total; r++ {
		source := s.Scrollback.View(r)
		if r >= s.Scrollback.Count {
			source = CaptureRow(s.Term, r-s.Scrollback.Count, oldCols)
		}
		wrapped := len(source) > 0 && source[len(source)-1].Mode&vt10x.AttrWrap != 0
		end := reflowContentEnd(source)
		if wrapped {
			end = len(source)
		}
		if r == cursorRow {
			end = max(end, cursorCol)
		}
		anchorMapped := false
		for c := 0; c < end; c++ {
			g := source[c]
			if g.Width < 0 {
				if r == cursorRow && c == cursorCol && g.Width == -2 {
					mapCursor()
				}
				continue
			}
			sourceWidth := max(1, int(g.Width))
			width := sourceWidth
			if width > cols {
				g.Char, g.Combining, g.Width = '\uFFFD', "", 1
				width = 1
			}
			if x+width > cols {
				if x < cols {
					row[x] = vt10x.Glyph{Char: ' ', Width: -2, FG: vt10x.DefaultFG, BG: vt10x.DefaultBG, UL: vt10x.DefaultUL}
				}
				flush(true)
			}
			if r == anchorRow && !anchorMapped {
				mappedAnchor = y
				anchorMapped = true
			}
			if r == cursorRow && c <= cursorCol && cursorCol < c+sourceWidth {
				mapCursor()
				mappedCursorCol += min(cursorCol-c, width-1)
			}
			g.Mode &^= vt10x.AttrWrap
			row[x] = g
			if width == 2 {
				continuation := g
				continuation.Char, continuation.Combining, continuation.Width = 0, "", -1
				row[x+1] = continuation
			}
			x += width
		}
		if r == anchorRow && !anchorMapped {
			mappedAnchor = y
		}
		if r == cursorRow && cursorCol >= end {
			mapCursor()
		}
		if !wrapped || r == total-1 {
			flush(false)
		}
	}
	// Rows below the cursor may be clipped, but its line must remain live.
	storedRows := min(y, mappedCursorRow+rows)
	firstVisible := max(0, storedRows-rows)
	dropped := storedRows - result.Count
	s.Scrollback = Ring{MaxLines: s.ScrollbackLines}
	for r := dropped; r < firstVisible; r++ {
		s.Scrollback.PushRow(result.Row(r - dropped))
	}
	visible := make([][]vt10x.Glyph, rows)
	for r := 0; r < rows; r++ {
		visible[r] = result.Get(firstVisible + r - dropped)
	}
	if s.ScrollOffset > 0 {
		s.ScrollOffset = min(s.Scrollback.Count, max(0, firstVisible-mappedAnchor+rows/2))
	}
	cursor.X = mappedCursorCol
	cursor.Y = max(0, min(mappedCursorRow-firstVisible, rows-1))
	cursor.State &^= vt10x.CursorWrapNext
	if pending {
		cursor.State |= vt10x.CursorWrapNext
	}
	s.Term.ReplaceScreen(cols, rows, visible, cursor, s.Term)
	s.SelActive = false
}

func reflowContentEnd(row []vt10x.Glyph) int {
	end := len(row)
	for end > 0 {
		g := row[end-1]
		if reflowCellHasContent(g) {
			break
		}
		end--
	}
	return end
}

func reflowCellHasContent(g vt10x.Glyph) bool {
	if g.Width < 0 || g.Image != nil || g.Combining != "" {
		return true
	}
	return g.Char != 0 && (g.Char != ' ' || g.BG != vt10x.DefaultBG || g.Mode&^vt10x.AttrWrap != 0 || g.Link != 0)
}

func fillRow(row []vt10x.Glyph, g vt10x.Glyph) {
	for i := range row {
		row[i] = g
	}
}
