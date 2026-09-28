package main

import "bunk/internal/vt10x"

// reflowCells copies glyphs directly: replaying old cursor commands at a new
// width changes their targets and can recreate content that was already erased.
func (p *Pane) reflowCells(cols, rows int) {
	oldCols, oldRows := p.term.Size()
	cursor := p.term.Cursor()
	cursorRow := p.sb.count + cursor.Y
	cursorCol := cursor.X
	if cursor.State&vt10x.CursorWrapNext != 0 {
		cursorCol++
	}
	anchorRow := -1
	if p.sbOff > 0 {
		anchorRow = p.sb.count - p.sbOff + oldRows/2
	}
	mappedAnchor, mappedCursorRow, mappedCursorCol := 0, 0, 0
	pending, cursorMapped := false, false
	result := sbRing{maxLines: p.scrollbackLines + rows}
	row := make([]vt10x.Glyph, cols)
	x, y := 0, 0
	flush := func(wrapped bool) {
		if wrapped {
			row[cols-1].Mode |= vt10x.AttrWrap
		}
		if !cursorMapped || y < mappedCursorRow+rows {
			result.push(row)
		}
		clear(row)
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
	contentRows := findContentRows(p.term, oldCols, oldRows)
	total := p.sb.count + contentRows
	for r := 0; r < total; r++ {
		source := p.sb.get(r)
		if r >= p.sb.count {
			source = captureRow(p.term, r-p.sb.count, oldCols)
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
	dropped := storedRows - result.count
	p.sb = sbRing{maxLines: p.scrollbackLines}
	for r := dropped; r < firstVisible; r++ {
		p.sb.push(result.get(r - dropped))
	}
	visible := make([][]vt10x.Glyph, rows)
	for r := 0; r < rows; r++ {
		visible[r] = result.get(firstVisible + r - dropped)
	}
	if p.sbOff > 0 {
		p.sbOff = min(p.sb.count, max(0, firstVisible-mappedAnchor+rows/2))
	}
	cursor.X = mappedCursorCol
	cursor.Y = max(0, min(mappedCursorRow-firstVisible, rows-1))
	cursor.State &^= vt10x.CursorWrapNext
	if pending {
		cursor.State |= vt10x.CursorWrapNext
	}
	p.term.ReplaceScreen(cols, rows, visible, cursor, p.term)
	p.selActive = false
	p.forceFullRepaint = true
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
