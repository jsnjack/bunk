package vt10x

// Rewrap packs the stored cells of logical lines into rows of a new width:
// the cell-copying part of primary-screen reflow. It reads and writes
// compact cells, so it touches the side table only for cells that have
// entries.
//
// A placed cell keeps its character, colours, attributes and side-table
// data, without the soft-wrap bit. A continuation cell is rebuilt from its
// wide lead, without the lead's combining text. When a wide character does
// not fit, the row ends in a padding cell with default colours and the
// character starts the next row. A character wider than the row becomes
// U+FFFD.
type Rewrap struct {
	cols int
	row  Row // the row being built; cells from x on are not written yet
	x    int // next column of row
	rows int // rows emitted
	emit func(Row)

	// Side-table indices in row for entries of the source row being read:
	// lead[i] for entry i+1 as stored, plain[i] for it without combining
	// text. An index is valid while its gen matches cur.
	lead, plain       []uint16
	leadGen, plainGen []uint32
	cur               uint32
}

var (
	// rewrapBlank fills unwritten cells: no character, default colours.
	rewrapBlank = cell{FG: DefaultFG, BG: DefaultBG}
	// rewrapPad stands in for a wide character moved to the next row.
	rewrapPad = func() cell {
		c := cell{Char: ' ', FG: DefaultFG, BG: DefaultBG}
		c.setWidth(-2)
		return c
	}()
)

// NewRewrap returns a Rewrap that builds rows cols cells wide and passes
// each finished row to emit. A row may end in blank cells. It is valid
// only during the call; emit must copy it to keep it.
func NewRewrap(cols int, emit func(Row)) *Rewrap {
	return &Rewrap{cols: cols, emit: emit, row: Row{cells: make([]cell, cols), tail: rewrapBlank}}
}

// X returns the column the next cell goes to. It equals the width when the
// row is full and has not wrapped yet.
func (w *Rewrap) X() int { return w.x }

// Rows returns the number of rows emitted so far, which is also the index of
// the row being built.
func (w *Rewrap) Rows() int { return w.rows }

// Append places the cells of src in [from, to), skipping continuation and
// padding cells. It returns the index of the row that received the first
// placed cell, or -1 when none was placed.
func (w *Rewrap) Append(src Row, from, to int) int {
	w.nextSource()
	cells := src.cells[from:to]
	first := -1
	for first < 0 && len(cells) > 0 {
		if c := cells[0]; c.width() >= 0 {
			first, _, _ = w.put(&src, c)
		}
		cells = cells[1:]
	}
	// Cells are copied whole and then patched. Changing a field of a local
	// copy and then storing the whole cell defeats store-to-load
	// forwarding, which costs a stall on every cell.
	dst, x := w.row.cells, w.x
	for i := range cells {
		c := &cells[i]
		switch {
		case c.meta == narrowMeta && x < len(dst):
			// Most cells are one column wide and have no side-table data.
			dst[x] = *c
			if c.Mode&attrWrap != 0 {
				dst[x].Mode &^= attrWrap
			}
			x++
		case c.meta == wideMeta && x+1 < len(dst):
			dst[x], dst[x+1] = *c, *c
			dst[x+1].Char, dst[x+1].meta = 0, continuationMeta
			if c.Mode&attrWrap != 0 {
				dst[x].Mode &^= attrWrap
				dst[x+1].Mode &^= attrWrap
			}
			x += 2
		case c.meta&widthMask == narrowMeta && x < len(dst) && w.cached(c.extra()):
			// A narrow cell whose side-table entry is already in the row.
			dst[x] = *c
			dst[x].meta = narrowMeta | w.lead[c.extra()-1]<<widthBits
			if c.Mode&attrWrap != 0 {
				dst[x].Mode &^= attrWrap
			}
			x++
		case c.width() < 0:
		default:
			w.x = x
			w.put(&src, *c)
			x = w.x
		}
	}
	w.x = x
	return first
}

// cached reports whether the index of source entry i in the row being
// built is known.
func (w *Rewrap) cached(i int) bool {
	return i <= len(w.leadGen) && w.leadGen[i-1] == w.cur
}

// Stored meta of cells without side-table data: one or two columns wide,
// or the continuation of a wide character.
const (
	narrowMeta       = 1
	wideMeta         = 2
	continuationMeta = -1 & widthMask
)

// Put places the cell of src at x, which must have a width of zero or more.
// It returns the row and column the cell went to and the columns it takes.
func (w *Rewrap) Put(src Row, x int) (row, col, width int) {
	w.nextSource()
	return w.put(&src, src.cells[x])
}

// EndLine emits the row being built and starts the next one.
func (w *Rewrap) EndLine() { w.emitRow(w.x) }

// SkipLine counts a row the caller stores itself: a logical line that fits
// unchanged. It must be called at the start of a row, when X is 0.
func (w *Rewrap) SkipLine() { w.rows++ }

func (w *Rewrap) put(src *Row, c cell) (row, col, width int) {
	width = max(1, int(c.width()))
	i := c.extra()
	replaced := width > w.cols
	if replaced {
		c.Char = '\uFFFD'
		c.setWidth(1)
		width = 1
	}
	if w.x+width > w.cols {
		w.wrap()
	}
	row, col = w.rows, w.x
	c.Mode &^= attrWrap
	c.meta &= widthMask
	if i != 0 {
		var d int
		if replaced {
			d = w.plainIndex(src, i)
		} else {
			d = w.leadIndex(src, i)
		}
		c.meta |= uint16(d) << widthBits
	}
	w.row.cells[w.x] = c
	if width == 2 {
		c.Char = 0
		c.setWidth(-1)
		c.meta &= widthMask
		if i != 0 {
			c.meta |= uint16(w.plainIndex(src, i)) << widthBits
		}
		w.row.cells[w.x+1] = c
	} else if width > 2 {
		fillCells(w.row.cells[w.x+1:w.x+width], rewrapBlank)
	}
	w.x += width
	return row, col, width
}

// wrap ends the row being built because the next character does not fit.
// A wide character leaves a padding cell; the last cell carries the
// soft-wrap bit.
func (w *Rewrap) wrap() {
	x := w.x
	if x < w.cols {
		w.row.cells[x] = rewrapPad
		x++
	}
	fillCells(w.row.cells[x:], rewrapBlank)
	w.row.cells[w.cols-1].Mode |= attrWrap
	w.emitRow(w.cols)
}

// emitRow passes the first n cells of the row being built to emit and
// starts the next row. Cells past n are not passed on: they may hold
// cells of earlier rows.
func (w *Rewrap) emitRow(n int) {
	r := &w.row
	w.emit(Row{cells: r.cells[:n], extras: r.extras, used: n, tail: rewrapBlank})
	r.clearExtras()
	w.x = 0
	w.rows++
	w.bump() // the new row has an empty side table
}

// nextSource forgets the side-table indices of the previous source row.
func (w *Rewrap) nextSource() { w.bump() }

func (w *Rewrap) bump() {
	w.cur++
	if w.cur == 0 { // wrapped around: no stale generation may match
		clear(w.leadGen)
		clear(w.plainGen)
		w.cur = 1
	}
}

// leadIndex returns the index in the row being built of source entry i.
func (w *Rewrap) leadIndex(src *Row, i int) int {
	if i > len(w.lead) {
		w.grow(len(src.extras))
	}
	if w.leadGen[i-1] == w.cur {
		return int(w.lead[i-1])
	}
	d := w.row.extraIndex(src.extras[i-1])
	if d != 0 {
		w.lead[i-1], w.leadGen[i-1] = uint16(d), w.cur
	}
	return d
}

// plainIndex is leadIndex for source entry i without its combining text,
// as a continuation cell or a replaced character carries it. It returns 0
// when nothing else is left.
func (w *Rewrap) plainIndex(src *Row, i int) int {
	if i > len(w.plain) {
		w.grow(len(src.extras))
	}
	if w.plainGen[i-1] == w.cur {
		return int(w.plain[i-1])
	}
	e := src.extras[i-1]
	e.Combining = ""
	d := 0
	if e.UL != DefaultUL || e.Link != 0 || e.Image != nil {
		d = w.row.extraIndex(e)
	}
	if d != 0 || e == (cellExtra{UL: DefaultUL}) {
		w.plain[i-1], w.plainGen[i-1] = uint16(d), w.cur
	}
	return d
}

func (w *Rewrap) grow(n int) {
	for len(w.lead) < n {
		w.lead = append(w.lead, 0)
		w.plain = append(w.plain, 0)
		w.leadGen = append(w.leadGen, 0)
		w.plainGen = append(w.plainGen, 0)
	}
}

// ReflowEnd returns the row's length without trailing cells that reflow
// drops: cells with no character, or blanks with the default background,
// no attributes and no hyperlink. Continuation and padding cells, image
// samples and combining text always count.
func (r Row) ReflowEnd() int {
	end := len(r.cells)
	for end > 0 && !r.reflowContent(end-1) {
		end--
	}
	return end
}

func (r Row) reflowContent(x int) bool {
	c := &r.cells[x]
	if c.width() < 0 {
		return true
	}
	var link uint16
	if i := c.extra(); i != 0 {
		e := &r.extras[i-1]
		if e.Image != nil || e.Combining != "" {
			return true
		}
		link = e.Link
	}
	return c.Char != 0 && (c.Char != ' ' || c.BG != DefaultBG || c.Mode&^attrWrap != 0 || link != 0)
}

// ReflowFit reports whether reflow leaves the row unchanged when the row is
// a whole logical line. It returns n = ReflowEnd() when Rewrap would place
// exactly the row's first n cells into any row at least n wide, and -1
// otherwise: a soft-wrap bit, a padding cell or a wide character without
// its usual continuation cell changes the cells.
func (r Row) ReflowFit() int {
	end := r.ReflowEnd()
	for x := 0; x < end; x++ {
		c := &r.cells[x]
		if c.Mode&attrWrap != 0 {
			return -1
		}
		switch c.width() {
		case 0, 1:
		case 2:
			if x+1 >= end || !r.continues(x) {
				return -1
			}
			x++
		default:
			return -1
		}
	}
	return end
}

// continues reports whether cell x+1 is the continuation Rewrap builds for
// the wide cell at x: no character, the same colours and attributes, and
// the side-table data without combining text.
func (r Row) continues(x int) bool {
	lead, cont := &r.cells[x], &r.cells[x+1]
	if cont.Char != 0 || cont.width() != -1 || cont.FG != lead.FG || cont.BG != lead.BG || cont.Mode != lead.Mode {
		return false
	}
	want, got := cellExtra{UL: DefaultUL}, cellExtra{UL: DefaultUL}
	if i := lead.extra(); i != 0 {
		want = r.extras[i-1]
		want.Combining = ""
	}
	if i := cont.extra(); i != 0 {
		got = r.extras[i-1]
	}
	return got == want
}
