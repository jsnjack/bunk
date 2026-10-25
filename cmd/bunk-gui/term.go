package main

import (
	"bunk/internal/session"
	"bunk/internal/vt10x"
)

// theme is bunk's "default" palette (config.go), so the GUI and the TUI look
// the same.
var theme = struct {
	fg, bg  uint32
	palette [16]uint32
}{
	fg: 0xFFFFFF, bg: 0x1C1C1F,
	palette: [16]uint32{
		0x241F31, 0xC01C28, 0x2EC27E, 0xF5C211, 0x1E78E4, 0x9841BB, 0x0AB9DC, 0xC0BFBC,
		0x5E5C64, 0xED333B, 0x57E389, 0xF8E45C, 0x51A1FF, 0xC061CB, 0x4FD2FD, 0xF6F5F4,
	},
}

// rgba packs 0xRRGGBB as RGBA8 with R in the low byte (GL_UNSIGNED_BYTE x4).
func rgba(rgb uint32) uint32 {
	return rgb>>16&0xff | rgb&0xff00 | (rgb&0xff)<<16 | 0xff<<24
}

// selState is the session's selection, normalized (start before end).
type selState struct {
	active     bool
	start, end session.SelPos
}

// preeditCell is one character of IME preedit text and its width in cells.
type preeditCell struct {
	r rune
	w int
}

// termView is the main thread's copy of what a pane shows: rows copied from
// vt10x or the scrollback ring, plus what changed since the last frame.
// Only the main thread touches it.
type termView struct {
	cols, rows int
	lines      []vt10x.Row
	vtDirty    []bool // scratch for ConsumeDirtyInto
	dirty      []bool // rows whose instances must be rebuilt

	cur        vt10x.Cursor
	curVisible bool
	// The cursor as last drawn into the instances; drawnY is -1 if hidden.
	drawnX, drawnY, drawnShape int
	pending                    int // dirty rows not yet converted

	// Scrollback view: row 0 shows virtual row vBase (scrollback rows come
	// first, then live rows). sbOff is 0 in the live view.
	sbOff, sbCount, vBase int
	sel                   selState

	// IME preedit text, drawn at the cursor. Set by the main thread.
	preedit       []preeditCell
	preeditCursor int // cells from the start of the preedit
	preeditDirty  bool

	colorGen uint64
	colorsOK bool
	pal      [256]uint32 // RGBA8, with OSC 4 overrides applied
	defFG    uint32
	defBG    uint32
	defCur   uint32
	curSet   bool // OSC 12 set a cursor colour

	links linkState // the link under the pointer while Ctrl is held (links.go)
}

func newTermView() *termView { return &termView{drawnY: -1} }

// pull copies what changed in the session and reports whether anything must
// be redrawn. The caller holds the session lock; this is the only work done
// under it.
//
//   - Live view: only the rows vt10x marked dirty are copied.
//   - Scrollback view: when the offset, the ring or any live row changed,
//     every visible row is copied from the ring or the screen.
//   - While the application holds a synchronized update open (DEC 2026) the
//     live view copies nothing: the previous frame stays on screen.
//
// It does not allocate unless the terminal size changed.
func (v *termView) pull(s *session.Session) bool {
	t := s.Term
	off := s.ScrollOffset
	if off == 0 && t.Mode()&vt10x.ModeSync != 0 {
		return false
	}
	cols, rows := t.Size()
	all := false
	if cols != v.cols || rows != v.rows {
		v.cols, v.rows = cols, rows
		v.lines = make([]vt10x.Row, rows)
		v.dirty = make([]bool, rows)
		v.drawnY = -1
		v.pending = 0
		all = true
	}
	if gen := t.ColorGen(); gen != v.colorGen || !v.colorsOK {
		v.loadColors(t)
		v.colorGen = gen
		all = true
	}
	count := s.Scrollback.Count
	if off != v.sbOff || (off > 0 && count != v.sbCount) {
		all = true
	}
	sel := selState{active: s.SelActive}
	if sel.active {
		sel.start, sel.end = s.SelNorm()
	}
	if sel != v.sel {
		v.sel = sel
		all = true
	}
	var any bool
	v.vtDirty, any = t.ConsumeDirtyInto(v.vtDirty)
	if off > 0 && any {
		all = true
	}
	v.sbOff, v.sbCount, v.vBase = off, count, count-off
	if any || all {
		v.clearHover() // the cells under the pointer changed
		for y := 0; y < rows; y++ {
			if !all && !v.vtDirty[y] {
				continue
			}
			if vRow := v.vBase + y; vRow < count {
				r := s.Scrollback.Row(vRow)
				v.lines[y].CopyFrom(r, r.Len())
			} else {
				t.CopyRow(vRow-count, &v.lines[y])
			}
			v.markDirty(y)
		}
	}
	v.cur = t.Cursor()
	v.curVisible = t.CursorVisible() && off == 0
	// The cursor is drawn into its row's instances, so a moved, hidden or
	// reshaped cursor dirties the row it was on and the row it is on now.
	y := -1
	if v.curVisible {
		y = v.cur.Y
	}
	if y != v.drawnY || v.cur.X != v.drawnX || v.cur.Shape != v.drawnShape {
		v.markDirty(v.drawnY)
		v.markDirty(y)
	}
	if v.preeditDirty {
		v.preeditDirty = false
		v.markDirty(v.cur.Y)
	}
	return v.pending > 0
}

// invalidate marks every row for rebuilding (new grid or atlas).
func (v *termView) invalidate() {
	for y := range v.dirty {
		v.markDirty(y)
	}
}

func (v *termView) markDirty(y int) {
	if y >= 0 && y < v.rows && !v.dirty[y] {
		v.dirty[y] = true
		v.pending++
	}
}

// loadColors resolves the palette and default colours, including dynamic
// overrides (OSC 4/10/11/12). It runs only when ColorGen changes.
func (v *termView) loadColors(t vt10x.Terminal) {
	for i := 0; i < 256; i++ {
		v.pal[i] = rgba(paletteRGB(i))
		if o, ok := t.ColorOverride(vt10x.Color(i)); ok && o < vt10x.DefaultFG {
			v.pal[i] = rgba(uint32(o))
		}
	}
	v.defFG, v.defBG = rgba(theme.fg), rgba(theme.bg)
	if o, ok := t.ColorOverride(vt10x.DefaultFG); ok && o < vt10x.DefaultFG {
		v.defFG = rgba(uint32(o))
	}
	if o, ok := t.ColorOverride(vt10x.DefaultBG); ok && o < vt10x.DefaultFG {
		v.defBG = rgba(uint32(o))
	}
	v.defCur, v.curSet = 0, false
	if o, ok := t.ColorOverride(vt10x.DefaultCursor); ok && o < vt10x.DefaultFG {
		v.defCur, v.curSet = rgba(uint32(o)), true
	}
	v.colorsOK = true
}

// paletteRGB returns the theme colour for 0-15 and the xterm colour cube and
// grey ramp for 16-255.
func paletteRGB(i int) uint32 {
	if i < 16 {
		return theme.palette[i]
	}
	if i < 232 {
		n := i - 16
		steps := [6]uint32{0, 95, 135, 175, 215, 255}
		return steps[n/36]<<16 | steps[(n/6)%6]<<8 | steps[n%6]
	}
	g := uint32(8 + (i-232)*10)
	return g<<16 | g<<8 | g
}

// color maps a vt10x colour to RGBA8, as bunk's vtColor does for tcell.
func (v *termView) color(c vt10x.Color) uint32 {
	switch {
	case c < 256:
		return v.pal[c]
	case c < vt10x.DefaultFG: // truecolor, packed r<<16 | g<<8 | b
		return rgba(uint32(c))
	case c == vt10x.DefaultBG:
		return v.defBG
	case c == vt10x.DefaultCursor:
		return v.defCur
	}
	return v.defFG
}

// halfway blends two RGBA8 colours 50/50 (SGR 2 dim, as bunk does).
func halfway(a, b uint32) uint32 {
	return (a>>1)&0x7f7f7f7f + (b>>1)&0x7f7f7f7f | 0xff<<24
}

// selCols returns the selected column range of visible row y, or c0 > c1.
func (v *termView) selCols(y, cols int) (c0, c1 int) {
	s := &v.sel
	vRow := v.vBase + y
	if !s.active || vRow < s.start.Row || vRow > s.end.Row {
		return 1, 0
	}
	c0, c1 = 0, cols-1
	if vRow == s.start.Row {
		c0 = s.start.Col
	}
	if vRow == s.end.Row {
		c1 = s.end.Col
	}
	return c0, c1
}

// convert rebuilds the instances of every dirty row and draws the cursor and
// IME preedit. It reads only the main thread's copy, so the session lock is
// not held. Steady-state calls do not allocate; a glyph seen for the first
// time is rasterized once.
func (v *termView) convert(r *Renderer) (rows int) {
	cols := min(v.cols, r.cols)
	curRebuilt := false
	if r.img.cols != r.cols || r.img.nrows != r.rows {
		r.img.resize(r.cols, r.rows)
	}
	for y := 0; y < min(v.rows, r.rows); y++ {
		if !v.dirty[y] {
			continue
		}
		v.dirty[y] = false
		rows++
		curRebuilt = curRebuilt || y == v.cur.Y
		r.rowDirty[y] = true
		line := &v.lines[y]
		inst := r.inst[y*r.cols : y*r.cols+cols]
		n := min(cols, line.Len())
		images := r.img.rowCells(y)
		hadImages := r.img.rows != nil && len(r.img.rows[y]) > 0
		for x := 0; x < n; x++ {
			ch, fgc, bgc, mode, width, extra := line.CellAt(x)
			fg, bg := v.color(fgc), v.color(bgc)
			if extra {
				if img := line.Image(x); img != nil {
					// Its bitmap's sub-rectangle is drawn over the
					// background; without it, its samples as a half block.
					if r.img.entry(img) != nil {
						if images == nil {
							images = r.img.firstCells(y)
						}
						inst[x] = Instance{FG: fg, BG: bg}
						images = append(images, imgCell{id: img.Placement.Bitmap,
							inst: imgInst{Cell: uint32(x) | uint32(y)<<16, Src: uint32(img.Col) | uint32(img.Row)<<16}})
					} else {
						inst[x] = Instance{Glyph: flagHalf, FG: halfColor(img.Top, bg), BG: halfColor(img.Bottom, bg)}
					}
					continue
				}
			}
			if mode&vt10x.AttrDim != 0 {
				fg = halfway(fg, bg)
			}
			var glyph uint32
			if mode&vt10x.AttrInvisible == 0 {
				comb := ""
				if extra {
					comb = line.Combining(x)
				}
				if width >= 0 && (ch > ' ' || comb != "") {
					wide := width == 2 && x+1 < cols
					glyph = r.atlas.Lookup(ch, comb, mode&vt10x.AttrBold != 0, wide)
				}
				if mode&(vt10x.AttrUnderline|vt10x.AttrStrikethrough|vt10x.AttrOverline) != 0 {
					if mode&vt10x.AttrUnderline != 0 {
						glyph |= flagUnderline
					}
					if mode&vt10x.AttrStrikethrough != 0 {
						glyph |= flagStrike
					}
					if mode&vt10x.AttrOverline != 0 {
						glyph |= flagOverline
					}
				}
			}
			// vt10x already swapped FG/BG for SGR 7 (reverse) when it stored
			// the cell, exactly as bunk's renderPane relies on.
			inst[x] = Instance{Glyph: glyph, FG: fg, BG: bg}
		}
		// Stored scrollback rows drop trailing blanks: the rest is default.
		for x := n; x < cols; x++ {
			inst[x] = Instance{FG: v.defFG, BG: v.defBG}
		}
		if c0, c1 := v.selCols(y, cols); c0 <= c1 {
			for x := max(0, c0); x <= min(c1, cols-1); x++ {
				inst[x].FG, inst[x].BG = inst[x].BG, inst[x].FG
			}
		}
		v.underlineHover(y, inst)
		if r.img.rows != nil {
			r.img.rows[y] = images
		}
		if hadImages || len(images) > 0 {
			r.img.dirty = true
		}
	}
	v.pending = 0
	v.drawnY, v.drawnX, v.drawnShape = -1, v.cur.X, v.cur.Shape
	if v.curVisible && v.cur.Y >= 0 && v.cur.Y < min(v.rows, r.rows) && v.cur.X >= 0 && v.cur.X < cols {
		if curRebuilt {
			x := v.cur.X
			if len(v.preedit) > 0 {
				x = v.drawPreedit(r, cols)
			}
			v.drawCursor(r, x)
		}
		v.drawnY = v.cur.Y
	}
	return rows
}

// drawPreedit overlays the IME preedit text at the cursor, underlined, and
// returns the column of the preedit cursor.
func (v *termView) drawPreedit(r *Renderer, cols int) int {
	y, x := v.cur.Y, v.cur.X
	curX := x
	cells := 0
	for i, c := range v.preedit {
		if i == v.preeditCursor {
			curX = x
		}
		if x+c.w > cols {
			break
		}
		wide := c.w == 2
		inst := &r.inst[y*r.cols+x]
		*inst = Instance{Glyph: r.atlas.Lookup(c.r, "", false, wide) | flagUnderline, FG: v.defFG, BG: v.defBG}
		if wide {
			r.inst[y*r.cols+x+1] = Instance{Glyph: flagUnderline, FG: v.defFG, BG: v.defBG}
		}
		x += c.w
		cells = i + 1
	}
	if v.preeditCursor >= cells {
		curX = x
	}
	return min(curX, cols-1)
}

// drawCursor marks the cursor cell at column x. Block cursors invert the
// cell (cursor colour as background); bar and underline cursors are drawn
// by the background shader in the cell's foreground colour. DECSCUSR blink
// states are drawn steady.
func (v *termView) drawCursor(r *Renderer, x int) {
	y := v.cur.Y
	i := &r.inst[y*r.cols+x]
	switch v.cur.Shape {
	case 3, 4:
		i.Glyph |= flagCursorLine
	case 5, 6:
		i.Glyph |= flagCursorBar
	default:
		cur := i.FG
		if v.curSet {
			cur = v.defCur
		}
		i.FG, i.BG = i.BG, cur
		if i.Glyph&flagWide != 0 && x+1 < r.cols {
			r.inst[y*r.cols+x+1].BG = cur
		}
	}
	r.rowDirty[y] = true
}
