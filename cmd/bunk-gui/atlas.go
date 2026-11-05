package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"unicode/utf8"

	"github.com/diamondburned/gotk4/pkg/cairo"
	"github.com/diamondburned/gotk4/pkg/pango"
	"github.com/diamondburned/gotk4/pkg/pangocairo"
	"github.com/rivo/uniseg"

	"bunk/internal/boxdraw"
)

// Instance.Glyph layout: the low 16 bits are the atlas slot (0 = nothing to
// draw), the rest are flags the shaders read.
const (
	slotMask       = 0xFFFF
	flagWide       = 1 << 16 // the glyph spans two cells and two atlas slots
	flagUnderline  = 1 << 17
	flagStrike     = 1 << 18
	flagOverline   = 1 << 19
	flagCursorBar  = 1 << 20
	flagCursorLine = 1 << 21
	flagHalf       = 1 << 22 // an image cell without its bitmap: FG fills the top half
	// flagOverflow marks a narrow glyph whose ink reaches past its cell, so
	// its slot holds it shrunk or shifted to fit. termView draws it across
	// two cells instead when the next one is blank; the shaders ignore it.
	flagOverflow = 1 << 23
	// flagColor marks a slot of the colour page: the glyph draws its own
	// colours (an emoji) instead of the cell's foreground.
	flagColor = 1 << 24
)

// maxAtlasSide caps the atlas texture (GL_MAX_TEXTURE_SIZE is 16384 here).
const maxAtlasSide = 8192

type clusterKey struct {
	r    rune
	comb string // combining suffix; compared by value, so lookups do not allocate
	bold bool
	wide bool // drawn across two cells: a wide character, or a narrow one that overflows
}

// rasterizer draws glyphs for the atlas. draw renders text into a buffer
// of its own, cells wide, and reports whether the drawing has colours of
// its own (a colour emoji) and whether the text's ink reached past the
// slot's right edge, so the slot holds it fitted. copyOut then copies that
// drawing into a slot: dst starts at the slot's top-left pixel and stride
// is the page width in pixels. A monochrome drawing gives one coverage byte
// per pixel, a colour one premultiplied RGBA.
type rasterizer interface {
	draw(text string, bold bool, cells int) (color, overflows bool)
	copyOut(dst []byte, stride int, color bool)
}

// page is one texture of the atlas: cell-sized slots, SlotsPerRow to a
// row, bpp bytes per pixel. It grows by doubling its height. Slots are
// handed out in order, so a renderer uploads only the slots past the last
// one it saw.
type page struct {
	W, H   int
	Pixels []byte // W*H*bpp, tightly packed, the CPU copy of the texture
	bpp    int
	rows   int // slot rows
	next   int // the next free slot; slot 0 means "nothing to draw"
}

// Atlas is a glyph atlas with cell-sized slots in two pages: coverage
// (R8) for text, which the shaders tint with the cell's foreground, and
// premultiplied RGBA (Color) for glyphs with colours of their own, such
// as emoji. A wide glyph takes two neighbouring slots, and so does a
// narrow one drawn across two cells. Glyphs are rasterized on first use
// and never evicted. All panes at one scale share one atlas.
type Atlas struct {
	Font         string
	Scale        float64 // device pixels per logical pixel
	CellW, CellH int     // device pixels
	Baseline     int     // device pixels from the cell top
	// Decoration lines, device pixels from the cell top.
	ULPos, Thick, StrikePos int

	SlotsPerRow int
	page             // coverage glyphs
	Color       page // colour glyphs (flagColor)

	ascii [2][128]uint32
	other map[clusterKey]uint32

	raster     rasterizer
	line       int // width of a light box drawing line
	Rasterized int // glyphs the font drew so far
	Drawn      int // glyphs boxdraw drew so far
	Colored    int // glyphs drawn in their own colours (Color page)
	Full       int // lookups that found no free slot
}

func newAtlas(cellW, cellH int, r rasterizer) *Atlas {
	a := &Atlas{CellW: cellW, CellH: cellH, SlotsPerRow: 64, raster: r, line: boxdraw.LineWidth(cellW), other: make(map[clusterKey]uint32)}
	a.page = newPage(a.SlotsPerRow*cellW, cellH, 16, 1)
	a.Color = newPage(a.SlotsPerRow*cellW, cellH, 2, 4)
	return a
}

// Lookup returns the slot and flags for a grapheme drawn in one cell, or in
// two with wide, rasterizing it on first use. Steady-state lookups do not
// allocate or call into Pango.
func (a *Atlas) Lookup(r rune, comb string, bold, wide bool) uint32 {
	b := 0
	if bold {
		b = 1
	}
	if r < 128 && comb == "" && !wide {
		if v := a.ascii[b][r]; v != 0 {
			return v
		}
	} else if v, ok := a.other[clusterKey{r, comb, bold, wide}]; ok {
		return v
	}
	return a.add(r, comb, bold, wide)
}

func (a *Atlas) add(r rune, comb string, bold, wide bool) uint32 {
	cells := 1
	if wide {
		cells = 2
	}
	var v uint32
	if comb == "" && boxdraw.Has(r) {
		// Box drawing, blocks, braille and the like fill the cell
		// exactly, so neighbouring cells join without seams.
		if v = uint32(a.alloc(&a.page, cells)); v != 0 {
			boxdraw.Draw(r, a.slotPixels(&a.page, v), a.W, cells*a.CellW, a.CellH, a.line)
			a.Drawn++
		}
	} else {
		color, overflows := a.raster.draw(string(r)+comb, bold, cells)
		pg := &a.page
		if color {
			pg = &a.Color
		}
		if slot := a.alloc(pg, cells); slot != 0 {
			v = uint32(slot)
			a.raster.copyOut(a.slotPixels(pg, v), pg.W, color)
			a.Rasterized++
			if color {
				v |= flagColor
				a.Colored++
			}
			if overflows && !wide {
				v |= flagOverflow
			}
		}
	}
	if v == 0 {
		a.Full++
	} else if wide {
		v |= flagWide
	}
	if r < 128 && comb == "" && !wide {
		b := 0
		if bold {
			b = 1
		}
		a.ascii[b][r] = v
	} else {
		a.other[clusterKey{r, comb, bold, wide}] = v
	}
	return v
}

// slotPixels returns the bytes of pg from slot v's top-left pixel on.
func (a *Atlas) slotPixels(pg *page, v uint32) []byte {
	slot := int(v & slotMask)
	x, y := (slot%a.SlotsPerRow)*a.CellW, (slot/a.SlotsPerRow)*a.CellH
	return pg.Pixels[(y*pg.W+x)*pg.bpp:]
}

func newPage(w, cellH, rows, bpp int) page {
	return page{W: w, H: rows * cellH, Pixels: make([]byte, w*rows*cellH*bpp), bpp: bpp, rows: rows, next: 1}
}

// alloc returns the first of `cells` free neighbouring slots of pg,
// growing it when needed, or 0 when it cannot grow further.
func (a *Atlas) alloc(pg *page, cells int) int {
	if cells == 2 && pg.next%a.SlotsPerRow == a.SlotsPerRow-1 {
		pg.next++ // a wide glyph must not straddle two slot rows
	}
	for pg.next+cells > a.SlotsPerRow*pg.rows {
		if (pg.rows*2)*a.CellH > maxAtlasSide {
			return 0
		}
		pg.rows *= 2
		pg.H = pg.rows * a.CellH
		px := make([]byte, pg.W*pg.H*pg.bpp)
		copy(px, pg.Pixels)
		pg.Pixels = px
	}
	slot := pg.next
	pg.next += cells
	return slot
}

// symbolFamilies draw the pictographs that the terminal font lacks. The
// fallback fontconfig picks for a monospace font is another monospace
// font, which squeezes a pictograph into its cell: Adwaita Mono's ⚠ is a
// tall, narrow triangle. These fonts draw pictographs in their own
// proportions, and fitInk spreads a wide one over a blank cell after it or
// shrinks it into its cell. Other symbols, such as ⎿ ✓ ❯, keep the
// monospace fallback, whose glyphs are drawn for a cell.
//
// The fonts are named because no generic family helps: after a monospace
// font, fontconfig ranks monospace fonts first even for "sans-serif".
// Fedora installs the Noto fonts (the RPM recommends them); Symbola and
// DejaVu Sans cover systems without them. With none installed, the
// fallback is fontconfig's, as before.
const symbolFamilies = "Noto Sans Symbols 2,Noto Sans Symbols,Symbola,DejaVu Sans"

// pictograph reports whether r has the Extended_Pictographic property:
// uniseg joins r, ZWJ and an emoji into one cluster only then (GB11).
func pictograph(r rune) bool {
	var buf [3 * utf8.UTFMax]byte
	text := utf8.AppendRune(utf8.AppendRune(utf8.AppendRune(buf[:0], r), '\u200d'), '\U0001f600')
	_, rest, _, _ := uniseg.FirstGraphemeCluster(text, -1)
	return len(rest) == 0
}

// pangoRaster renders glyphs with Pango/cairo into a reusable ARGB32
// surface, white on transparent, so a coverage glyph comes out grey in
// every channel and a colour font's glyph keeps its colours. The surface
// holds two cells with a cell of margin on each side, where ink left
// outside a slot would show.
type pangoRaster struct {
	cellW, cellH, baseline int
	surf                   *cairo.Surface
	cr                     *cairo.Context
	layouts                [2]*pango.Layout // regular, bold
	// symbols are layouts for a lone pictograph, regular and bold: the
	// terminal font first, then symbolFamilies, then fontconfig's fallback.
	symbols [2]*pango.Layout
	cells   int // the width of the last drawing
}

func (p *pangoRaster) draw(text string, bold bool, cells int) (color, overflows bool) {
	b := 0
	if bold {
		b = 1
	}
	l := p.layouts[b]
	if r, n := utf8.DecodeRuneInString(text); n == len(text) && pictograph(r) {
		l = p.symbols[b]
	}
	p.cr.SetOperator(cairo.OperatorClear)
	p.cr.Paint()
	p.cr.SetOperator(cairo.OperatorOver)
	l.SetText(text)
	w := cells * p.cellW
	ink, _ := l.PixelExtents()
	scale, dx, overflows := fitInk(ink.X(), ink.X()+ink.Width(), w, max(1, p.cellW/8))
	y := float64(p.baseline) - float64(l.Baseline())/pango.SCALE
	// A shrunk glyph keeps its vertical centre.
	cy := y + float64(ink.Y()) + float64(ink.Height())/2
	p.cr.Save()
	p.cr.Translate(float64(p.cellW)+dx, cy*(1-scale))
	p.cr.Scale(scale, scale)
	p.cr.MoveTo(0, y)
	pangocairo.ShowLayout(p.cr, l)
	p.cr.Restore()
	p.surf.Flush()
	p.cells = cells
	src, sstride := p.surf.Data(), p.surf.Stride()
	for y := 0; y < p.cellH && !color; y++ {
		row := src[y*sstride+4*p.cellW : y*sstride+4*(p.cellW+w)]
		for i := 0; i < len(row); i += 4 {
			if isColored(binary.NativeEndian.Uint32(row[i:])) {
				color = true
				break
			}
		}
	}
	return color, overflows
}

// isColored reports whether a premultiplied ARGB32 pixel drawn in white
// is not grey: its colour channels differ from its alpha.
func isColored(px uint32) bool {
	a := int(px >> 24)
	for _, c := range [3]int{int(px>>16) & 0xff, int(px>>8) & 0xff, int(px) & 0xff} {
		if c-a > 2 || a-c > 2 {
			return true
		}
	}
	return false
}

func (p *pangoRaster) copyOut(dst []byte, stride int, color bool) {
	src, sstride := p.surf.Data(), p.surf.Stride()
	w := p.cells * p.cellW
	for y := 0; y < p.cellH; y++ {
		row := src[y*sstride+4*p.cellW : y*sstride+4*(p.cellW+w)]
		if !color {
			out := dst[y*stride : y*stride+w]
			for x := range out {
				out[x] = byte(binary.NativeEndian.Uint32(row[4*x:]) >> 24)
			}
			continue
		}
		out := dst[4*y*stride : 4*(y*stride+w)]
		for x := 0; x < w; x++ {
			px := binary.NativeEndian.Uint32(row[4*x:])
			out[4*x], out[4*x+1], out[4*x+2], out[4*x+3] = byte(px>>16), byte(px>>8), byte(px), byte(px>>24)
		}
	}
}

// fitInk places a glyph whose ink spans [x0, x1) at its natural position
// in a slot w pixels wide: drawn at x, it lands at x*scale + dx. Ink that
// stays within tol of the slot keeps its place, so the terminal font's
// glyphs draw as they always did. Wider ink, which a fallback font's
// symbol often has, moves into the slot, and shrinks when it is wider than
// the slot. overflows reports ink past the right edge, which room to the
// right would show at full size.
func fitInk(x0, x1, w, tol int) (scale, dx float64, overflows bool) {
	overflows = x1 > w+tol
	switch {
	case x0 >= -tol && x1 <= w+tol:
		return 1, 0, overflows
	case x1-x0 <= w:
		return 1, float64(min(max(0, -x0), w-x1)), overflows
	}
	scale = float64(w) / float64(x1-x0)
	return scale, -scale * float64(x0), overflows
}

// BuildAtlas measures font at the given device scale and returns an atlas
// with printable ASCII (regular and bold) already rasterized.
func BuildAtlas(font string, scale float64) (*Atlas, error) {
	desc := pango.FontDescriptionFromString(font)
	boldDesc := desc.Copy()
	boldDesc.SetWeight(pango.WeightBold)

	probe := cairo.CreateImageSurface(cairo.FormatA8, 8, 8)
	layout := newFontLayout(cairo.Create(probe), desc, scale)
	metrics := layout.Context().Metrics(desc, nil)
	ascent := float64(metrics.Ascent()) / pango.SCALE
	descent := float64(metrics.Descent()) / pango.SCALE
	layout.SetText("M")
	_, logical := layout.PixelExtents()
	cellW := logical.Width()
	cellH := int(math.Ceil(ascent + descent))
	baseline := int(math.Ceil(ascent))
	if cellW <= 0 || cellH <= 0 {
		return nil, fmt.Errorf("atlas: bad metrics for %q: %dx%d", font, cellW, cellH)
	}

	surf := cairo.CreateImageSurface(cairo.FormatARGB32, 4*cellW, cellH)
	cr := cairo.Create(surf)
	cr.SetSourceRGBA(1, 1, 1, 1)
	pr := &pangoRaster{cellW: cellW, cellH: cellH, baseline: baseline, surf: surf, cr: cr}
	pr.layouts[0] = newFontLayout(cr, desc, scale)
	pr.layouts[1] = newFontLayout(cr, boldDesc, scale)
	for i, d := range []*pango.FontDescription{desc, boldDesc} {
		sym := d.Copy()
		families := symbolFamilies
		if f := d.Family(); f != "" {
			families = f + "," + families
		}
		sym.SetFamily(families)
		pr.symbols[i] = newFontLayout(cr, sym, scale)
	}

	a := newAtlas(cellW, cellH, pr)
	a.Font, a.Scale, a.Baseline = font, scale, baseline
	a.Thick = max(1, int(math.Round(float64(metrics.UnderlineThickness())/pango.SCALE)))
	a.ULPos = min(cellH-a.Thick, baseline-int(math.Round(float64(metrics.UnderlinePosition())/pango.SCALE)))
	a.StrikePos = baseline - int(math.Round(float64(metrics.StrikethroughPosition())/pango.SCALE))
	for r := rune(0x21); r < 0x7f; r++ {
		a.Lookup(r, "", false, false)
		a.Lookup(r, "", true, false)
	}
	return a, nil
}

func newFontLayout(cr *cairo.Context, desc *pango.FontDescription, scale float64) *pango.Layout {
	layout := pangocairo.CreateLayout(cr)
	ctx := layout.Context()
	pangocairo.ContextSetResolution(ctx, 96*scale)
	// Not pangocairo.ContextSetFontOptions: it crashes in gotk4 v0.4.1.
	setFontOptionsC(ctx, int(cairo.AntialiasGray), int(cairo.HintStyleSlight), int(cairo.HintMetricsOn))
	layout.ContextChanged()
	layout.SetFontDescription(desc)
	return layout
}
