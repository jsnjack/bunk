package session

import "bunk/internal/vt10x"

// reflowCells re-wraps the primary screen's logical lines at a new width.
// It copies stored cells instead of replaying output: replaying old cursor
// commands at a new width changes their targets and can recreate content
// that was already erased.
//
// Cells are copied in their compact form by vt10x.Rewrap. A scrollback row
// that is a whole logical line and fits the new width keeps its storage:
// it moves to the new scrollback without being read again, because the
// ring caches its fit. A resize therefore costs the rows it changes plus a
// little work per unchanged row.
func (s *Session) reflowCells(cols, rows int) {
	oldCols, oldRows := s.Term.Size()
	cursor := s.Term.Cursor()
	old := &s.Scrollback
	cursorRow := old.Count + cursor.Y
	cursorCol := cursor.X
	if cursor.State&vt10x.CursorWrapNext != 0 {
		cursorCol++
	}
	anchorRow := -1
	if s.ScrollOffset > 0 {
		anchorRow = old.Count - s.ScrollOffset + oldRows/2
	}
	mappedAnchor, mappedCursorRow, mappedCursorCol := 0, 0, 0
	pending, cursorMapped := false, false
	out := newReflowOutput(s.ScrollbackLines, rows, old)
	var w *vt10x.Rewrap
	// Rows more than a screen below the cursor are dropped: its line must
	// stay on screen.
	w = vt10x.NewRewrap(cols, func(row vt10x.Row) {
		if !cursorMapped || w.Rows() < mappedCursorRow+rows {
			out.copy(row)
		}
	})
	mapCursor := func() {
		cursorMapped = true
		mappedCursorRow, mappedCursorCol = w.Rows(), w.X()
		pending = mappedCursorCol == cols
		if pending {
			mappedCursorCol--
		}
	}
	contentRows := findContentRows(s.Term, oldCols, oldRows)
	total := old.Count + contentRows
	var live vt10x.Row
	// The rows of the old scrollback are read in order: a row moves to the
	// new scrollback or its storage is reused for new rows. The old ring is
	// dropped at the end.
	for r := 0; r < total; r++ {
		scrollback := r < old.Count
		if scrollback && w.X() == 0 {
			// A whole logical line that fits is stored unchanged.
			if fit := old.lineFit(r, cols); fit >= 0 {
				if r == anchorRow {
					mappedAnchor = w.Rows()
				}
				if !cursorMapped || w.Rows() < mappedCursorRow+rows {
					out.move(r, fit)
				}
				w.SkipLine()
				out.consumed = r + 1
				continue
			}
		}
		var source vt10x.Row
		if scrollback {
			source = old.Row(r)
		} else {
			s.Term.CopyRow(r-old.Count, &live)
			source = live
		}
		n := source.Len()
		wrapped := n > 0 && rowWrapped(source, n-1)
		end := n
		if !wrapped {
			end = source.ReflowEnd()
		}
		if r == cursorRow {
			end = max(end, cursorCol)
			anchorMapped := false
			for c := 0; c < end; c++ {
				if width := reflowWidth(source, c); width < 0 {
					if c == cursorCol && width == -2 {
						mapCursor()
					}
					continue
				}
				sourceWidth := max(1, reflowWidth(source, c))
				y, x, width := w.Put(source, c)
				if r == anchorRow && !anchorMapped {
					mappedAnchor = y
					anchorMapped = true
				}
				if c <= cursorCol && cursorCol < c+sourceWidth {
					cursorMapped = true
					mappedCursorRow, mappedCursorCol, pending = y, x+min(cursorCol-c, width-1), false
				}
			}
			if r == anchorRow && !anchorMapped {
				mappedAnchor = w.Rows()
			}
			if cursorCol >= end {
				mapCursor()
			}
		} else if first := w.Append(source, 0, end); r == anchorRow {
			mappedAnchor = first
			if first < 0 {
				mappedAnchor = w.Rows()
			}
		}
		if !wrapped || r == total-1 {
			w.EndLine()
		}
		if scrollback {
			out.consumed = r + 1
		}
	}
	storedRows := min(w.Rows(), mappedCursorRow+rows)
	firstVisible := max(0, storedRows-rows)
	var visible [][]vt10x.Glyph
	s.Scrollback, visible = out.finish(s.ScrollbackLines, rows, firstVisible, storedRows)
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

// reflowWidth returns the stored width of cell x of row.
func reflowWidth(row vt10x.Row, x int) int {
	_, _, _, _, width, _ := row.CellAt(x)
	return int(width)
}

// reflowOutput collects reflowed rows in order, as many as a scrollback
// plus a screen hold. The newest rows become the screen, the others the new
// scrollback. Copies reuse the storage of old scrollback rows that have
// been read and were not moved.
type reflowOutput struct {
	rows     Ring
	old      *Ring
	consumed int // old rows before this index have been read
	next     int // the next old row whose storage may be reused
}

func newReflowOutput(scrollback, rows int, old *Ring) *reflowOutput {
	return &reflowOutput{rows: Ring{MaxLines: scrollback + rows}, old: old}
}

// copy stores a copy of row without its trailing blanks, as Ring.PushRow
// does. A slot without storage takes that of a consumed old row.
func (o *reflowOutput) copy(row vt10x.Row) {
	if o.rows.MaxLines <= 0 {
		return
	}
	dst := &o.rows.lines[o.rows.nextSlot()]
	for dst.Len() == 0 && o.next < o.consumed {
		*dst = *o.old.slot(o.next)
		o.next++
	}
	dst.CopyFrom(row, row.TrimmedLen())
}

// move stores old row i itself, cut to its first fit cells.
func (o *reflowOutput) move(i, fit int) {
	src := o.old.slot(i)
	row := *src
	*src = vt10x.Row{} // its storage is no longer free to reuse
	row.Truncate(fit)
	o.rows.adopt(row, int32(fit))
}

// finish returns the screen's glyph rows, from stored row firstVisible on,
// and the new scrollback: the rows before firstVisible, at most limit of
// them. storedRows counts the rows stored, including any dropped for
// lack of room.
func (o *reflowOutput) finish(limit, rows, firstVisible, storedRows int) (Ring, [][]vt10x.Glyph) {
	res := &o.rows
	dropped := storedRows - res.Count
	visible := make([][]vt10x.Glyph, rows)
	for r := 0; r < rows; r++ {
		visible[r] = res.Get(firstVisible + r - dropped)
	}
	sb := Ring{MaxLines: limit}
	keep := firstVisible - dropped // rows before the screen
	if limit <= 0 || keep <= 0 {
		return sb, visible
	}
	from := max(0, keep-limit)
	sb.Count = keep - from
	if res.head == 0 && from == 0 {
		// The rows are in place: shorten the arrays. Slots past Count
		// hold rows nothing else refers to, which later pushes reuse.
		sb.lines, sb.fits = res.lines[:limit:limit], res.fits[:limit:limit]
		clear(res.lines[limit:])
		return sb, visible
	}
	sb.lines, sb.fits = make([]vt10x.Row, limit), make([]int32, limit)
	for i := range sb.Count {
		slot := (res.head + from + i) % res.MaxLines
		sb.lines[i], sb.fits[i] = res.lines[slot], res.fits[slot]
	}
	return sb, visible
}

func reflowCellHasContent(g vt10x.Glyph) bool {
	if g.Width < 0 || g.Image != nil || g.Combining != "" {
		return true
	}
	return g.Char != 0 && (g.Char != ' ' || g.BG != vt10x.DefaultBG || g.Mode&^vt10x.AttrWrap != 0 || g.Link != 0)
}
