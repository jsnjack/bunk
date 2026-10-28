package main

import (
	"fmt"
	"math"
	"unicode/utf8"

	"github.com/diamondburned/gotk4/pkg/cairo"
	"github.com/diamondburned/gotk4/pkg/pango"
	"github.com/diamondburned/gotk4/pkg/pangocairo"

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
)

// maxAtlasSide caps the atlas texture (GL_MAX_TEXTURE_SIZE is 16384 here).
const maxAtlasSide = 8192

type clusterKey struct {
	r    rune
	comb string // combining suffix; compared by value, so lookups do not allocate
	bold bool
	wide bool // drawn across two cells: a wide character, or a narrow one that overflows
}

// rasterizer draws text into an R8 slot. dst starts at the slot's top-left
// pixel; stride is the atlas width. It reports whether the text's ink
// reached past the slot's right edge, so the slot holds it fitted.
type rasterizer interface {
	draw(text string, bold bool, cells int, dst []byte, stride int) (overflows bool)
}

// Atlas is a grayscale glyph atlas with cell-sized slots. A wide glyph takes
// two neighbouring slots, and so does a narrow one drawn across two cells. Glyphs are rasterized on first use and never
// evicted; the texture grows by doubling its height. Slots are handed out in
// order, so a renderer that shares the atlas uploads only the slots past the
// last one it saw. All panes at one scale share one atlas.
type Atlas struct {
	Font         string
	Scale        float64 // device pixels per logical pixel
	CellW, CellH int     // device pixels
	Baseline     int     // device pixels from the cell top
	// Decoration lines, device pixels from the cell top.
	ULPos, Thick, StrikePos int

	SlotsPerRow int
	slotRows    int
	W, H        int
	Pixels      []byte // W*H, tightly packed, the CPU copy of the texture

	next  int
	ascii [2][128]uint32
	other map[clusterKey]uint32

	raster     rasterizer
	line       int // width of a light box drawing line
	Rasterized int // glyphs the font drew so far
	Drawn      int // glyphs boxdraw drew so far
	Replaced   int // glyphs drawn as U+FFFD (colour emoji)
	Full       int // lookups that found no free slot
}

func newAtlas(cellW, cellH int, r rasterizer) *Atlas {
	a := &Atlas{CellW: cellW, CellH: cellH, SlotsPerRow: 64, raster: r, line: boxdraw.LineWidth(cellW), other: make(map[clusterKey]uint32)}
	a.slotRows = 16
	a.W, a.H = a.SlotsPerRow*cellW, a.slotRows*cellH
	a.Pixels = make([]byte, a.W*a.H)
	a.next = 1 // slot 0 means "nothing to draw"
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
	slot := a.alloc(cells)
	v := uint32(slot)
	if slot != 0 {
		x, y := (slot%a.SlotsPerRow)*a.CellW, (slot/a.SlotsPerRow)*a.CellH
		dst := a.Pixels[y*a.W+x:]
		if comb == "" && boxdraw.Draw(r, dst, a.W, cells*a.CellW, a.CellH, a.line) {
			// Box drawing, blocks, braille and the like fill the cell
			// exactly, so neighbouring cells join without seams.
			a.Drawn++
		} else {
			text := string(r) + comb
			if isColorEmoji(r, comb) {
				// Colour glyphs need an RGBA atlas; draw a placeholder for now.
				text = "�"
				a.Replaced++
			}
			if a.raster.draw(text, bold, cells, dst, a.W) && !wide {
				v |= flagOverflow
			}
			a.Rasterized++
		}
		if wide {
			v |= flagWide
		}
	} else {
		a.Full++
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

// alloc returns the first of `cells` free neighbouring slots, growing the
// atlas when needed, or 0 when it cannot grow further.
func (a *Atlas) alloc(cells int) int {
	if cells == 2 && a.next%a.SlotsPerRow == a.SlotsPerRow-1 {
		a.next++ // a wide glyph must not straddle two slot rows
	}
	for a.next+cells > a.SlotsPerRow*a.slotRows {
		if (a.slotRows*2)*a.CellH > maxAtlasSide {
			return 0
		}
		a.slotRows *= 2
		h := a.slotRows * a.CellH
		px := make([]byte, a.W*h)
		copy(px, a.Pixels)
		a.Pixels, a.H = px, h
	}
	slot := a.next
	a.next += cells
	return slot
}

// isColorEmoji is a coarse test for glyphs that only exist as colour emoji.
func isColorEmoji(r rune, comb string) bool {
	if r >= 0x1F000 && r <= 0x1FAFF {
		return true
	}
	for len(comb) > 0 {
		c, n := utf8.DecodeRuneInString(comb)
		if c == 0xFE0F { // VS16: emoji presentation
			return true
		}
		comb = comb[n:]
	}
	return false
}

// pangoRaster renders glyphs with Pango/cairo into a reusable A8 surface.
// The surface holds two cells with a cell of margin on each side, where
// ink left outside a slot would show.
type pangoRaster struct {
	cellW, cellH, baseline int
	surf                   *cairo.Surface
	cr                     *cairo.Context
	layouts                [2]*pango.Layout // regular, bold
}

func (p *pangoRaster) draw(text string, bold bool, cells int, dst []byte, stride int) (overflows bool) {
	l := p.layouts[0]
	if bold {
		l = p.layouts[1]
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
	src, sstride := p.surf.Data(), p.surf.Stride()
	for y := 0; y < p.cellH; y++ {
		copy(dst[y*stride:y*stride+w], src[y*sstride+p.cellW:y*sstride+p.cellW+w])
	}
	return overflows
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

	surf := cairo.CreateImageSurface(cairo.FormatA8, 4*cellW, cellH)
	cr := cairo.Create(surf)
	cr.SetSourceRGBA(1, 1, 1, 1)
	pr := &pangoRaster{cellW: cellW, cellH: cellH, baseline: baseline, surf: surf, cr: cr}
	pr.layouts[0] = newFontLayout(cr, desc, scale)
	pr.layouts[1] = newFontLayout(cr, boldDesc, scale)

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
