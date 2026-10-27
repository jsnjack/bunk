package vt10x

// cell is the stored form of a Glyph. It holds no pointers, so the garbage
// collector does not scan the grid or scrollback, and it is 16 bytes instead
// of 56. Data most cells lack lives in the row's side table.
type cell struct {
	Char   rune
	FG, BG Color
	Mode   int16
	meta   uint16 // bits 0-2: Width (two's complement); bits 3-15: side-table index
}

const (
	widthBits = 3
	widthMask = 1<<widthBits - 1
	maxExtras = 1<<(16-widthBits) - 1 // largest 1-based side-table index
)

func (c cell) width() int8 {
	w := int8(c.meta & widthMask)
	if w > widthMask/2 {
		w -= widthMask + 1
	}
	return w
}

func (c *cell) setWidth(w int8) { c.meta = c.meta&^widthMask | uint16(w)&widthMask }

// extra returns the 1-based side-table index, or 0 when the cell has none.
func (c cell) extra() int { return int(c.meta >> widthBits) }

// cellExtra holds the parts of a Glyph that most cells leave at their
// defaults.
type cellExtra struct {
	Combining string
	UL        Color
	Link      uint16
	Image     *ImageCell
}

// Row is one row of stored cells. A scroll callback receives the row leaving
// the screen; Row values must not be modified, and a callback that keeps one
// must copy it with CopyFrom.
type Row struct {
	cells  []cell
	extras []cellExtra
	// Every cell at an index >= used equals tail. Writes raise used;
	// filling to the end of the row lowers it. Clearing and trimming then
	// cost the length of the text, not the width of the row.
	used int
	tail cell
}

func newRow(n int) *Row { return &Row{cells: make([]cell, n)} }

// Len returns the number of cells in the row.
func (r Row) Len() int { return len(r.cells) }

// Glyph returns the cell at x as a Glyph.
func (r Row) Glyph(x int) Glyph {
	c := r.cells[x]
	g := Glyph{Char: c.Char, FG: c.FG, BG: c.BG, Mode: c.Mode, Width: c.width(), UL: DefaultUL}
	if i := c.extra(); i != 0 {
		e := &r.extras[i-1]
		g.Combining, g.UL, g.Link, g.Image = e.Combining, e.UL, e.Link, e.Image
	}
	return g
}

// CellAt returns the stored parts of the cell at x without building a Glyph:
// character, colours, attribute bits and width. extra reports side-table
// data (combining marks, underline colour, hyperlink, image); read it with
// Glyph or Combining. Renderers use it for cells that have none.
func (r Row) CellAt(x int) (ch rune, fg, bg Color, mode int16, width int8, extra bool) {
	c := &r.cells[x]
	return c.Char, c.FG, c.BG, c.Mode, c.width(), c.meta>>widthBits != 0
}

// Combining returns the combining suffix of the cell at x.
func (r Row) Combining(x int) string {
	if i := r.cells[x].extra(); i != 0 {
		return r.extras[i-1].Combining
	}
	return ""
}

// Image returns the image data of the cell at x, or nil.
func (r Row) Image(x int) *ImageCell {
	if i := r.cells[x].extra(); i != 0 {
		return r.extras[i-1].Image
	}
	return nil
}

// Link returns the hyperlink ID of the cell at x (see State.Link), or 0.
func (r Row) Link(x int) uint16 {
	if i := r.cells[x].extra(); i != 0 {
		return r.extras[i-1].Link
	}
	return 0
}

// HasImages reports whether the row's side table holds image data. It may
// report entries of cells since overwritten, never miss a current one.
func (r Row) HasImages() bool {
	for i := range r.extras {
		if r.extras[i].Image != nil {
			return true
		}
	}
	return false
}

// MarkImages calls mark with the image store ID of each placement a cell of
// the row refers to. Rows without image data in their side table cost the
// table's length; others are read up to the last written cell.
func (r Row) MarkImages(mark func(id uint64)) {
	if !r.HasImages() {
		return
	}
	for x := range r.cells[:min(r.used, len(r.cells))] {
		if i := r.cells[x].extra(); i != 0 {
			if img := r.extras[i-1].Image; img != nil && img.Placement != nil && img.Placement.Bitmap != 0 {
				mark(img.Placement.Bitmap)
			}
		}
	}
}

// MarkLinks calls mark with the hyperlink ID of each cell of the row that
// has one (see State.SweepLinks). Neighbouring cells of one link are
// reported once. Rows without links in their side table cost the table's
// length; others are read up to the last written cell.
func (r Row) MarkLinks(mark func(id uint16)) {
	if !r.hasLinks() {
		return
	}
	var last uint16
	for x := range r.cells[:min(r.used, len(r.cells))] {
		if i := r.cells[x].extra(); i != 0 {
			if id := r.extras[i-1].Link; id != 0 && id != last {
				mark(id)
				last = id
			}
		}
	}
}

// hasLinks reports whether the row's side table holds a hyperlink. It may
// report entries of cells since overwritten, never miss a current one.
func (r Row) hasLinks() bool {
	for i := range r.extras {
		if r.extras[i].Link != 0 {
			return true
		}
	}
	return false
}

// AppendGlyphs appends every cell of the row to dst as Glyphs.
func (r Row) AppendGlyphs(dst []Glyph) []Glyph {
	for x := range r.cells {
		dst = append(dst, r.Glyph(x))
	}
	return dst
}

// TrimmedLen returns the row's length without trailing cells that draw,
// copy, search and reflow like missing cells: spaces with the default
// background and no attributes or side-table data.
func (r Row) TrimmedLen() int {
	n := len(r.cells)
	if r.used < n && r.tail.plainBlank() {
		n = r.used
	}
	for n > 0 && r.cells[n-1].plainBlank() {
		n--
	}
	return n
}

// plainBlank reports whether c draws, copies, searches and reflows like a
// missing cell.
func (c cell) plainBlank() bool {
	return (c.Char == ' ' || c.Char == 0) && c.Mode == 0 && c.BG == DefaultBG &&
		c.meta>>widthBits == 0 && c.width() >= 0 && c.width() <= 1
}

// touch records a write to cell x.
func (r *Row) touch(x int) {
	if x >= r.used {
		r.used = x + 1
	}
}

// setWrap marks cell x as the end of a soft-wrapped row.
func (r *Row) setWrap(x int) {
	r.cells[x].Mode |= attrWrap
	r.touch(x)
}

// touchAll records writes that may have changed any cell.
func (r *Row) touchAll() { r.used = len(r.cells) }

// CopyFrom makes r a copy of the first n cells of src, reusing r's storage.
func (r *Row) CopyFrom(src Row, n int) {
	r.cells = append(r.cells[:0], src.cells[:n]...)
	r.used = n
	old := len(r.extras)
	r.extras = append(r.extras[:0], src.extras...)
	clearExtraTail(r.extras, old)
}

// copyCells copies src's leading cells into r without changing r's length.
func (r *Row) copyCells(src Row) {
	copy(r.cells, src.cells)
	r.touchAll()
	old := len(r.extras)
	r.extras = append(r.extras[:0], src.extras...)
	clearExtraTail(r.extras, old)
}

// Truncate shortens the row to its first n cells.
func (r *Row) Truncate(n int) {
	r.cells = r.cells[:n]
	r.used = min(r.used, n)
}

// SetGlyphs makes r hold glyphs, reusing r's storage.
func (r *Row) SetGlyphs(glyphs []Glyph) {
	if cap(r.cells) < len(glyphs) {
		r.cells = make([]cell, len(glyphs))
	}
	r.cells = r.cells[:len(glyphs)]
	r.clearExtras()
	r.used = 0
	for x, g := range glyphs {
		r.set(x, g)
	}
}

// encode converts g to a cell, adding its rare data to the side table.
func (r *Row) encode(g Glyph) cell {
	c := cell{Char: g.Char, FG: g.FG, BG: g.BG, Mode: g.Mode}
	c.setWidth(g.Width)
	if g.Combining != "" || g.UL != DefaultUL || g.Link != 0 || g.Image != nil {
		i := r.extraIndex(cellExtra{Combining: g.Combining, UL: g.UL, Link: g.Link, Image: g.Image})
		c.meta |= uint16(i) << widthBits
	}
	return c
}

// extraIndex returns the 1-based side-table index of e, adding it when no
// cell uses an equal entry yet. Entries of overwritten cells stay until the
// table outgrows the row, when unused ones are dropped. If every entry is
// still in use, e is dropped and the cell keeps its character and colours.
func (r *Row) extraIndex(e cellExtra) int {
	for i := range r.extras {
		if r.extras[i] == e {
			return i + 1
		}
	}
	if len(r.extras) >= 2*len(r.cells)+32 || len(r.extras) == maxExtras {
		r.compactExtras()
		if len(r.extras) == maxExtras {
			return 0
		}
	}
	r.extras = append(r.extras, e)
	return len(r.extras)
}

// compactExtras drops side-table entries that no cell refers to.
func (r *Row) compactExtras() {
	remap := make([]uint16, len(r.extras)+1)
	old := len(r.extras)
	kept := r.extras[:0]
	for x := range r.cells {
		i := r.cells[x].extra()
		if i == 0 {
			continue
		}
		if remap[i] == 0 {
			kept = append(kept, r.extras[i-1])
			remap[i] = uint16(len(kept))
		}
		r.cells[x].meta = r.cells[x].meta&widthMask | remap[i]<<widthBits
	}
	r.extras = kept
	clearExtraTail(r.extras, old)
}

// clearExtraTail zeroes entries between len(extras) and old, so dropped
// strings and images can be collected.
func clearExtraTail(extras []cellExtra, old int) {
	if old > len(extras) {
		clear(extras[len(extras):old])
	}
}

// clearExtras empties the side table. Only valid when no cell refers to it.
func (r *Row) clearExtras() {
	clear(r.extras)
	r.extras = r.extras[:0]
}

// set stores g at x.
func (r *Row) set(x int, g Glyph) {
	r.cells[x] = r.encode(g)
	r.touch(x)
}

// fill stores c in cells [x0, x1), dropping side-table entries when it
// overwrites the whole row.
func (r *Row) fill(x0, x1 int, c cell) {
	if x0 >= x1 {
		return
	}
	if x0 == 0 && x1 == len(r.cells) && c.meta>>widthBits == 0 {
		r.clearExtras() // every cell is overwritten
	}
	r.fillCell(x0, x1, c)
}

// fillCell stores c in cells [x0, x1). c must not refer to the side table
// unless the entry belongs to this row.
func (r *Row) fillCell(x0, x1 int, c cell) {
	if x0 >= x1 {
		return
	}
	if x1 == len(r.cells) && c.meta>>widthBits == 0 {
		// The cells from x0 on all become c, the new tail. When c already
		// is the tail, only the cells below used can differ from it.
		if c == r.tail {
			fillCells(r.cells[x0:max(x0, r.used)], c)
			r.used = min(r.used, x0)
		} else {
			fillCells(r.cells[x0:x1], c)
			r.tail, r.used = c, x0
		}
		return
	}
	fillCells(r.cells[x0:x1], c)
	if x1 > r.used {
		r.used = x1
	}
}

// fillCells sets every cell in cells to c. Short runs are stored directly;
// longer ones use doubling copies, which move memory in blocks.
func fillCells(cells []cell, c cell) {
	if len(cells) < 64 {
		for i := range cells {
			cells[i] = c
		}
		return
	}
	cells[0] = c
	for n := 1; n < len(cells); n *= 2 {
		copy(cells[n:], cells[:n])
	}
}

// writeASCII stores text as cells from x, each a copy of c with its
// character replaced.
func (r *Row) writeASCII(x int, text []byte, c cell) {
	// A block fill plus 4-byte character stores beats storing whole
	// 16-byte cells one at a time.
	cells := r.cells[x : x+len(text)]
	fillCells(cells, c)
	for i, b := range text {
		cells[i].Char = rune(b)
	}
	r.touch(x + len(text) - 1)
}

// hasCombining reports whether the cell at x carries combining text.
func (r *Row) hasCombining(x int) bool {
	i := r.cells[x].extra()
	return i != 0 && r.extras[i-1].Combining != ""
}
